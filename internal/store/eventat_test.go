package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

// Overlapping invitations: the live title is the one the owner accepted,
// never one they declined, even when it started earlier.
func TestEventAtPrefersAccepted(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	for _, e := range []struct {
		id, title, resp string
		start           time.Time
	}{
		{"e1", "Roadmap Review", "accepted", at},
		{"e2", "Company All-Hands", "notResponded", at.Add(5 * time.Minute)},
		{"e3", "Vendor Call", "declined", at.Add(10 * time.Minute)},
	} {
		it := item.Item{ID: "m365-calendar:" + e.id, Source: "m365-calendar", Ref: e.id, Kind: item.KindEvent, Title: e.title,
			OccurredAt: e.start, EndedAt: e.start.Add(time.Hour), Native: map[string]string{"response": e.resp}}
		if _, err := s.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.EventAt(at.Add(20 * time.Minute))
	if err != nil || got != "Roadmap Review" {
		t.Fatalf("EventAt = %q, %v", got, err)
	}
}
