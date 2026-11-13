package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/m365"
)

func TestOverlappingTeamsCopy(t *testing.T) {
	at := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	m := &item.Item{OccurredAt: at, EndedAt: at.Add(40 * time.Minute)}
	short := &item.Item{ID: "short", OccurredAt: at.Add(35 * time.Minute), EndedAt: at.Add(50 * time.Minute)} // 5 min shared
	main := &item.Item{ID: "main", OccurredAt: at.Add(time.Minute), EndedAt: at.Add(45 * time.Minute)}
	if got := item.Overlapping(m, []*item.Item{short, main}); got == nil || got.ID != "main" {
		t.Errorf("got %v", got)
	}
	if got := item.Overlapping(m, []*item.Item{short}); got != nil {
		t.Errorf("a copy sharing under a quarter of the recording was taken: %v", got.ID)
	}
}

// Agreement counts only what Teams heard while Tomoe recorded: a recording
// of part of a meeting still matches its Teams copy, and another meeting
// in the same slot doesn't.
func TestWordAgreement(t *testing.T) {
	at := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	seg := func(start float64, text string) item.Segment { return item.Segment{Start: start, Text: text} }
	rec := &item.Item{OccurredAt: at.Add(10 * time.Minute), EndedAt: at.Add(20 * time.Minute),
		Segments: []item.Segment{seg(5, "pricing proposal for the renewal"), seg(60, "budget numbers next quarter")}}
	teams := &item.Item{OccurredAt: at, EndedAt: at.Add(40 * time.Minute), Segments: []item.Segment{
		seg(60, "welcome everyone agenda roadmap"), // before the recording
		seg(610, "pricing proposal for the renewal"), seg(670, "budget numbers next quarter"),
		seg(1500, "hiring update offsite logistics"), // after it
	}}
	if a := item.WordAgreement(rec, teams); a < 0.99 {
		t.Errorf("partial recording of the same meeting: %.2f", a)
	}
	other := &item.Item{OccurredAt: at, EndedAt: at.Add(40 * time.Minute), Segments: []item.Segment{seg(650, "security review findings remediation"), seg(700, "budget timeline")}}
	if a := item.WordAgreement(rec, other); a > 0.4 {
		t.Errorf("another meeting: %.2f", a)
	}
}

func TestFirstSyncReadsFurtherBack(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	e := &Engine{Cfg: cfg}
	if got := e.pastDays(); got != 42 {
		t.Errorf("first sync reads %d days back, want 42", got)
	}
	dir := filepath.Join(cfg.InboxDir(), m365.CalendarSource)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ev.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.pastDays(); got != 30 {
		t.Errorf("later syncs read %d days back, want 30", got)
	}
	e.Cfg.M365.FirstDays = 0
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if got := e.pastDays(); got != 30 {
		t.Errorf("first_days = 0 reads %d days back, want 30", got)
	}
}
