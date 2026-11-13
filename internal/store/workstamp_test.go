package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

// The work queue is recomputed only when WorkStamp moves, so it must move
// with each change that can alter the queue.
func TestWorkStampMoves(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stamp := func() string {
		v, err := s.WorkStamp()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	a := stamp()
	if b := stamp(); a != b {
		t.Fatalf("unchanged store, stamps %q and %q", a, b)
	}
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s.UpsertItem(item.Item{ID: "m:1", Source: "t", Ref: "m:1", Kind: item.KindMeeting, Title: "x", OccurredAt: at, Segments: []item.Segment{{Text: "x"}}})
	b := stamp()
	if b == a {
		t.Error("an item was added; the stamp didn't move")
	}
	if err := s.PutManifest("t", "m:1", "h2/v", "text/plain"); err != nil {
		t.Fatal(err)
	}
	if c := stamp(); c == b {
		t.Error("an item was fetched again; the stamp didn't move")
	}
}
