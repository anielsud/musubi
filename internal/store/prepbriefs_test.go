package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// Drafted talking points carry their sources' label, fail closed without
// sources, and are replaced in place.
func TestPrepBriefs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	if err := s.SavePrepBrief(PrepBrief{ItemID: "ev1", Input: "a", Model: "m", Text: "Raise the plan.", Sources: []Hit{{ItemID: "x", Label: sensitivity.Label{Level: sensitivity.Internal}}}, MadeAt: at}); err != nil {
		t.Fatal(err)
	}
	b, err := s.PrepBriefFor(sensitivity.Owner(), "ev1")
	if err != nil || b == nil || b.Text != "Raise the plan." || b.Input != "a" || len(b.Sources) != 1 || !b.MadeAt.Equal(at) {
		t.Fatalf("read back: %+v %v", b, err)
	}
	if b, _ := s.PrepBriefFor(sensitivity.SharedScreen(), "ev1"); b == nil {
		t.Error("an internal brief hidden on a shared screen")
	}
	if err := s.SavePrepBrief(PrepBrief{ItemID: "ev1", Input: "b", Model: "m", Text: "Raise the new plan.", MadeAt: at}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.PrepBriefFor(sensitivity.Owner(), "ev1"); b == nil || b.Input != "b" || b.Label.Level != sensitivity.Confidential {
		t.Errorf("replaced: %+v", b)
	}
	if b, _ := s.PrepBriefFor(sensitivity.SharedScreen(), "ev1"); b != nil {
		t.Error("a brief without sources shown on a shared screen")
	}
	if b, err := s.PrepBriefFor(sensitivity.Owner(), "none"); b != nil || err != nil {
		t.Errorf("missing: %+v %v", b, err)
	}
}
