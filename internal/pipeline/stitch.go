package pipeline

import (
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

// Stitching (knowledge.stitch): a Tomoe recording of a meeting that has a
// Teams transcript takes the transcript's lines for the stretches it
// missed: before the owner joined, after they left, between two parts of
// a split recording. The recording's own lines stand wherever it heard.
// The stitched copy is used in memory for chunking and everything after;
// the stored items are untouched, so turning it off restores them.

// stitchSlack: a transcript line starting within this of a recording's
// span was heard by it (the two clocks differ by a second or two).
const stitchSlack = 5 * time.Second

// stitchLine is a transcript line a recording missed, at absolute times.
type stitchLine struct {
	at, end time.Time
	seg     item.Segment
}

// stitchFills plans the stitching: for each Tomoe recording, the Teams
// lines it missed, and which Teams copy they came from. Recordings and
// transcripts are grouped by the calendar event they're linked to
// (links: meeting → event); the transcript must share most of its words
// with what the recordings heard (item.SameMeetingWords), or it's another
// conversation in the same slot.
func stitchFills(items []item.Item, links map[string]string) (map[string][]stitchLine, map[string]string) {
	recs, trans := map[string][]*item.Item{}, map[string][]*item.Item{}
	for i := range items {
		it := &items[i]
		ev := links[it.ID]
		if ev == "" || it.Kind != item.KindMeeting {
			continue
		}
		switch {
		case it.Source == "tomoe":
			recs[ev] = append(recs[ev], it)
		case it.Source == "teams" && strings.HasPrefix(it.Ref, "teams-"):
			trans[ev] = append(trans[ev], it)
		}
	}
	fills, from := map[string][]stitchLine{}, map[string]string{}
	for ev, rs := range recs {
		ts := trans[ev]
		if len(ts) == 0 {
			continue
		}
		sort.Slice(rs, func(a, b int) bool { return rs[a].OccurredAt.Before(rs[b].OccurredAt) })
		// The transcript most of the recordings' time overlaps.
		var t *item.Item
		var best time.Duration
		for _, c := range ts {
			var d time.Duration
			for _, r := range rs {
				lo, hi := maxTime(r.OccurredAt, c.OccurredAt), minTime(r.EndedAt, c.EndedAt)
				if hi.After(lo) {
					d += hi.Sub(lo)
				}
			}
			if d > best {
				t, best = c, d
			}
		}
		if t == nil {
			continue
		}
		longest := rs[0]
		for _, r := range rs {
			if r.EndedAt.Sub(r.OccurredAt) > longest.EndedAt.Sub(longest.OccurredAt) {
				longest = r
			}
		}
		if item.WordAgreement(longest, t) < item.SameMeetingWords {
			continue
		}
		for i, sg := range t.Segments {
			at := t.OccurredAt.Add(secs(sg.Start))
			end := t.OccurredAt.Add(secs(sg.End))
			if sg.End <= sg.Start {
				end = at
				if i+1 < len(t.Segments) {
					end = t.OccurredAt.Add(secs(t.Segments[i+1].Start))
				}
			}
			heard := false
			for _, r := range rs {
				if !at.Before(r.OccurredAt.Add(-stitchSlack)) && !at.After(r.EndedAt.Add(stitchSlack)) {
					heard = true
					break
				}
			}
			if heard || strings.TrimSpace(sg.Text) == "" {
				continue
			}
			// The gap before a recording goes to it; after the last, to
			// the last.
			to := rs[len(rs)-1]
			for _, r := range rs {
				if r.OccurredAt.After(at) {
					to = r
					break
				}
			}
			line := sg
			line.From = "teams"
			fills[to.ID] = append(fills[to.ID], stitchLine{at: at, end: end, seg: line})
			from[to.ID] = t.ID
		}
	}
	return fills, from
}

// stitched is the recording with the missed lines in place: it starts
// at the earliest of either, its own lines shifted to match.
func stitched(it item.Item, lines []stitchLine) item.Item {
	if len(lines) == 0 {
		return it
	}
	start, end := it.OccurredAt, it.EndedAt
	for _, l := range lines {
		start, end = minTime(start, l.at), maxTime(end, l.end)
	}
	shift := it.OccurredAt.Sub(start).Seconds()
	segs := make([]item.Segment, 0, len(it.Segments)+len(lines))
	for _, s := range it.Segments {
		s.Start += shift
		if s.End > 0 {
			s.End += shift
		}
		segs = append(segs, s)
	}
	for _, l := range lines {
		s := l.seg
		s.Start, s.End = l.at.Sub(start).Seconds(), l.end.Sub(start).Seconds()
		segs = append(segs, s)
	}
	sort.SliceStable(segs, func(a, b int) bool { return segs[a].Start < segs[b].Start })
	it.Segments, it.OccurredAt, it.EndedAt = segs, start, end
	return it
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// stitches is what stitching does this run: the stitched copy of each
// recording it applies to, and each Teams copy stitched in (→ the
// recording, which stands for it). It applies to a recording not yet
// threaded, or already stitched once (stamp "stitch/<id>"), so a threaded
// meeting is never re-chunked by a transcript arriving late. One extracted
// and summarised but not threaded is: redoing those is cheap.
// mark records the stamp for those newly stitched.
func (r *Runner) stitches(items []item.Item, mark bool) (map[string]item.Item, map[string]string, error) {
	if !r.Cfg.Knowledge.Stitch {
		return nil, nil, nil
	}
	links, err := r.Store.LinksFrom("occurred_as")
	if err != nil {
		return nil, nil, err
	}
	fills, from := stitchFills(items, links)
	if len(fills) == 0 {
		return nil, nil, nil
	}
	threaded, err := r.Store.ThreadedItems()
	if err != nil {
		return nil, nil, err
	}
	out, into := map[string]item.Item{}, map[string]string{}
	for i := range items {
		it := items[i]
		lines := fills[it.ID]
		if len(lines) == 0 {
			continue
		}
		key := "stitch/" + it.ID
		was, _ := r.Store.GlobalStamp(key)
		if was == "" {
			if threaded[it.ID] != "" {
				continue
			}
			if mark {
				if err := r.Store.SetGlobalStamp(key, from[it.ID]); err != nil {
					return nil, nil, err
				}
			}
		}
		out[it.ID] = stitched(it, lines)
		if _, ok := into[from[it.ID]]; !ok {
			into[from[it.ID]] = it.ID
		}
	}
	return out, into, nil
}

// withStitches makes each stitched-in Teams copy a duplicate of the
// recording standing for it (and never the other way round).
func withStitches(dups, into map[string]string) map[string]string {
	if len(into) == 0 {
		return dups
	}
	if dups == nil {
		dups = map[string]string{}
	}
	for t, rec := range into {
		if dups[rec] == t {
			delete(dups, rec)
		}
		dups[t] = rec
	}
	// Whatever stood in for a stitched-in copy now goes to the recording.
	for k, v := range dups {
		if rec, ok := into[v]; ok && k != rec {
			dups[k] = rec
		}
	}
	return dups
}

// awaitingTranscript is the Tomoe recordings of Teams meetings (their
// event has a join link) not yet threaded whose Teams transcript hasn't
// arrived, within knowledge.stitch_wait_hours of the meeting's end:
// threaded now, they couldn't be stitched once it does.
func (r *Runner) awaitingTranscript(items []item.Item, links map[string]string) map[string]bool {
	kc := r.Cfg.Knowledge
	if !kc.Stitch || kc.StitchWaitHours <= 0 || !r.Cfg.M365.Enabled || !r.Cfg.M365.Transcripts {
		return nil
	}
	byID := make(map[string]*item.Item, len(items))
	have := map[string]bool{}
	for i := range items {
		it := &items[i]
		byID[it.ID] = it
		if it.Source == "teams" && strings.HasPrefix(it.Ref, "teams-") && links[it.ID] != "" {
			have[links[it.ID]] = true
		}
	}
	threaded, _ := r.Store.ThreadedItems()
	wait := time.Duration(kc.StitchWaitHours * float64(time.Hour))
	now := time.Now()
	out := map[string]bool{}
	for i := range items {
		it := &items[i]
		ev := byID[links[it.ID]]
		if it.Source != "tomoe" || it.Kind != item.KindMeeting || ev == nil || ev.Native["joinUrl"] == "" || have[ev.ID] {
			continue
		}
		end := maxTime(it.EndedAt, ev.EndedAt)
		if now.Sub(end) >= wait {
			continue
		}
		if threaded[it.ID] == "" {
			out[it.ID] = true
		}
	}
	return out
}
