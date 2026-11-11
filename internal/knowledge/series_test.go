package knowledge

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// The previous meeting of a series is found through the calendar: the
// latest earlier event with the subject (a cancelled one's prefix aside),
// and of its recordings the one with a recap.
func TestLastOfSeries(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	day := func(n int) time.Time { return time.Date(2026, 3, n, 11, 0, 0, 0, time.UTC) }
	ev := func(id, title string, at time.Time) item.Item {
		return item.Item{ID: id, Source: "m365-calendar", Ref: id, Kind: item.KindEvent, Title: title, OccurredAt: at, EndedAt: at.Add(time.Hour)}
	}
	rec := func(id, src string, at time.Time) item.Item {
		return item.Item{ID: id, Source: src, Ref: id, Kind: item.KindMeeting, Title: "Teams Meeting", OccurredAt: at, EndedAt: at.Add(time.Hour), Segments: []item.Segment{{Text: "said"}}}
	}
	items := []item.Item{
		ev("e1", "Weekly ELT Meeting", day(2)), ev("e2", "Canceled: Weekly ELT Meeting", day(9)), ev("e3", "Weekly ELT Meeting", day(16)),
		ev("other", "Hiring sync", day(12)),
		rec("r1", "tomoe", day(2)), rec("r2a", "tomoe", day(9)), rec("r2b", "teams", day(9)), rec("r9", "tomoe", day(12)),
	}
	for _, it := range items {
		if _, err := s.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceLinks("occurred_as", []store.Link{
		{ItemID: "r1", Rel: "occurred_as", OtherID: "e1", Score: 1}, {ItemID: "r2a", Rel: "occurred_as", OtherID: "e2", Score: 1},
		{ItemID: "r2b", Rel: "occurred_as", OtherID: "e2", Score: 1}, {ItemID: "r9", Rel: "occurred_as", OtherID: "other", Score: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if id, at := LastOfSeries(s, items[2], items); id != "r2a" || !at.Equal(day(9)) {
		t.Errorf("without recaps: %q %v (want the Tomoe copy of the 9th)", id, at)
	}
	cs, _ := s.Chunks("r2b")
	if len(cs) == 0 {
		if err := s.ReplaceChunks("r2b", "v", "h", []store.Chunk{{Seq: 0, SegTo: 1, Text: "said"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceLabels("r2b", "v", "h", sensitivity.Label{}, nil, []string{"owner@corp.example"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDigest("r2b", "d1", "h", "i", store.Digest{Overview: "o"}); err != nil {
		t.Fatal(err)
	}
	if id, _ := LastOfSeries(s, items[2], items); id != "r2b" {
		t.Errorf("with a recap on the Teams copy: %q", id)
	}
	if id, _ := LastOfSeries(s, items[0], items); id != "" {
		t.Errorf("first of its series: %q", id)
	}
}
