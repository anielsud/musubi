package knowledge

import (
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// seriesTitle is an event's subject as its series is known by.
func seriesTitle(t string) string {
	t = strings.TrimSpace(t)
	if len(t) > 10 && strings.EqualFold(t[:10], "canceled: ") {
		t = strings.TrimSpace(t[10:])
	}
	return strings.ToLower(t)
}

// LastOfSeries is the recording of the latest event before ev with its
// subject (one with a recap first, then Tomoe's), and when it happened;
// "" when no earlier occurrence was recorded.
func LastOfSeries(st *store.Store, ev item.Item, items []item.Item) (string, time.Time) {
	links, err := st.LinksFrom("occurred_as")
	if err != nil {
		return "", time.Time{}
	}
	byID := make(map[string]*item.Item, len(items))
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	recs := map[string][]*item.Item{}
	for m, e := range links {
		if r := byID[m]; r != nil {
			recs[e] = append(recs[e], r)
		}
	}
	want := seriesTitle(ev.Title)
	var best *item.Item
	var bestEv time.Time
	for _, e := range items {
		if e.Kind != item.KindEvent || e.ID == ev.ID || !e.OccurredAt.Before(ev.OccurredAt) || seriesTitle(e.Title) != want || len(recs[e.ID]) == 0 {
			continue
		}
		if best != nil && !e.OccurredAt.After(bestEv) {
			continue
		}
		var pick *item.Item
		score := -1
		for _, r := range recs[e.ID] {
			s := 0
			if d, _ := st.MeetingDigest(sensitivity.Owner(), r.ID); d != nil {
				s += 2
			}
			if r.Source == "tomoe" {
				s++
			}
			if s > score {
				pick, score = r, s
			}
		}
		best, bestEv = pick, e.OccurredAt
	}
	if best == nil {
		return "", time.Time{}
	}
	return best.ID, best.OccurredAt
}
