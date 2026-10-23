package pipeline

import (
	"sort"
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
			if a.Source == b.Source || dup[b.ID] != "" || !overlapping(a, b) {
				continue
			}
			keep, drop := a, b
			if rank(b) < rank(a) {
				keep, drop = b, a
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
	ae, be := recordingEnd(a), recordingEnd(b)
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

// recordingEnd is when a recording ends: its end time, else its last line's.
func recordingEnd(it *item.Item) time.Time {
	if !it.EndedAt.IsZero() && it.EndedAt.After(it.OccurredAt) {
		return it.EndedAt
	}
	end := 0.0
	for _, s := range it.Segments {
		end = max(end, s.End, s.Start)
	}
	return it.OccurredAt.Add(time.Duration(end * float64(time.Second)))
}
