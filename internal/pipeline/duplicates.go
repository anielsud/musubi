package pipeline

import (
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

// One meeting can arrive twice: as Tomoe's session and as the meeting
// app's transcript. Only one becomes knowledge, or its statements and
// threads would count everything twice. Both stay stored (and searchable).

// sameMeetingStart and sameMeetingOverlap decide that two recordings are
// one meeting: started within this of each other, and overlapping for at
// least this share of the shorter one.
const (
	sameMeetingStart   = 10 * time.Minute
	sameMeetingOverlap = 0.5
)

// missedShare: a preferred copy with fewer words than this share of the
// other's missed the meeting (on 37 meetings both Tomoe and Bluedot
// recorded, six Tomoe copies held 0–71% of Bluedot's words; the rest
// 86–98%).
const missedShare = 0.8

// meetingDuplicates maps each meeting recording that another one stands in
// for to the one kept. Kept: one already extracted (its statements may be
// threaded already); else the first source in prefer; else the earlier.
func meetingDuplicates(items []item.Item, prefer []string, extracted func(id string) bool) map[string]string {
	var ms []*item.Item
	for i := range items {
		if items[i].Kind == item.KindMeeting {
			ms = append(ms, &items[i])
		}
	}
	sort.Slice(ms, func(a, b int) bool { return ms[a].OccurredAt.Before(ms[b].OccurredAt) })
	rank := func(it *item.Item) int {
		r := len(prefer)
		for i, s := range prefer {
			if it.Source == s {
				r = i
			}
		}
		if extracted(it.ID) {
			r -= 1000
		}
		return r
	}
	dup := map[string]string{}
	for i, a := range ms {
		if dup[a.ID] != "" {
			continue
		}
		for _, b := range ms[i+1:] {
			if b.OccurredAt.Sub(a.OccurredAt) > sameMeetingStart {
				break
			}
			if dup[b.ID] != "" || !overlapping(a, b) {
				continue
			}
			// Two recordings from one source are two meetings (or a split
			// recording), unless they're one transcript brought in twice (a
			// hand-imported Teams transcript and the synced one): then they
			// share most of their words. Tomoe's never are.
			if a.Source == b.Source && (a.Source == "tomoe" || item.WordAgreement(a, b) < item.SameMeetingWords) {
				continue
			}
			keep, drop := a, b
			if rank(b) < rank(a) {
				keep, drop = b, a
			}
			// The preferred copy missed the meeting (a broken capture): it
			// holds much less of what was said than the other.
			if !extracted(keep.ID) && float64(wordCount(keep)) < missedShare*float64(wordCount(drop)) {
				keep, drop = drop, keep
			}
			dup[drop.ID] = keep.ID
			if drop == a {
				break
			}
		}
	}
	return dup
}

// overlapping reports whether two recordings overlap for at least
// sameMeetingOverlap of the shorter one.
func overlapping(a, b *item.Item) bool {
	ae, be := a.End(), b.End()
	lo, hi := a.OccurredAt, ae
	if b.OccurredAt.After(lo) {
		lo = b.OccurredAt
	}
	if be.Before(hi) {
		hi = be
	}
	shorter := min(ae.Sub(a.OccurredAt), be.Sub(b.OccurredAt))
	return shorter > 0 && hi.Sub(lo) >= time.Duration(sameMeetingOverlap*float64(shorter))
}

func wordCount(it *item.Item) int {
	n := 0
	for _, s := range it.Segments {
		n += len(strings.Fields(s.Text))
	}
	return n
}
