package knowledge

import (
	"math"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// A role question's best thread is the older role thread; the comp thread
// its latest turn went to scores just past the margin and is added only
// with Newer, being later than every thread found.
func TestFindNewer(t *testing.T) {
	at := func(cos float64) []float32 { return []float32{float32(cos), float32(math.Sqrt(1 - cos*cos))} }
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	f := &ThreadFinder{
		rows: []store.ThreadIndexRow{
			{ID: 1, Name: "Dana's remit", Centroid: at(0.90)},
			{ID: 2, Name: "Dana's comp", Centroid: at(0.80)},
			{ID: 3, Name: "Older budget", Centroid: at(0.79)},
		},
		latest: map[int64]time.Time{1: day(25), 2: day(29), 3: day(10)},
	}
	q := []float32{1, 0}
	if got := f.Find(sensitivity.Owner(), q, "where is it", 3); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("without Newer: %+v", got)
	}
	f.Newer = true
	got := f.Find(sensitivity.Owner(), q, "where is it", 3)
	if len(got) != 2 || got[1].ID != 2 || !got[1].Newer {
		t.Fatalf("with Newer: %+v", got)
	}
	f.NewerFirst = true
	if got := f.Find(sensitivity.Owner(), q, "where is it", 3); len(got) != 2 || got[0].ID != 2 {
		t.Fatalf("with NewerFirst: %+v", got)
	}
	f.NewerFirst = false
	// A later thread too far from the best isn't added.
	f.latest[3] = day(30)
	f.rows[1].Centroid = at(0.70)
	f.rows[2].Centroid = at(0.70)
	if got := f.Find(sensitivity.Owner(), q, "where is it", 3); len(got) != 1 {
		t.Fatalf("far thread added: %+v", got)
	}
}
