package store

import (
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Linking a meeting to its calendar event changes its passages' title and
// nothing else: the passages must keep their rows, or the statements (and
// digests and vectors) pointing at them are deleted with them.
func TestRetitleKeepsStatements(t *testing.T) {
	s, id, cs := digestStore(t, sensitivity.Internal)
	at := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	if err := s.ReplaceStatements(id, "v", "h", []Statement{{ChunkID: cs[0].ID, Line: 1, Kind: "commitment", Status: "agreed", Text: "Robin Vale will send the plan", SaidAt: at}}); err != nil {
		t.Fatal(err)
	}
	retitled := make([]Chunk, len(cs))
	for i, c := range cs {
		c.Title = "Planning sync (calendar)"
		retitled[i] = c
	}
	if err := s.ReplaceChunks(id, "v", "h2", retitled); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Chunks(id)
	if len(after) != len(cs) || after[0].ID != cs[0].ID || after[0].Title != "Planning sync (calendar)" {
		t.Fatalf("chunks after retitle: %+v", after)
	}
	if sts, _ := s.ItemStatements(id); len(sts) != 1 {
		t.Fatalf("statements after retitle: %d, want 1", len(sts))
	}
	if st, _ := s.StageStamp("chunk", id); st != "v/h2" {
		t.Errorf("stamp %q", st)
	}
	// A real change of text still replaces the passages.
	changed := append([]Chunk(nil), retitled...)
	changed[0].Text = "Robin Vale: I will send the revised plan."
	if err := s.ReplaceChunks(id, "v", "h3", changed); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.Chunks(id); after[0].Text != changed[0].Text {
		t.Errorf("changed text not stored: %q", after[0].Text)
	}
	if sts, _ := s.ItemStatements(id); len(sts) != 0 {
		t.Errorf("statements of replaced passages: %d (a real re-split redoes them)", len(sts))
	}
}

// The full-text index follows a retitle: the new title is searchable.
func TestRetitleIsSearchable(t *testing.T) {
	s, id, cs := digestStore(t, sensitivity.Internal)
	retitled := make([]Chunk, len(cs))
	for i, c := range cs {
		c.Title = "Quarterly zeppelin review"
		retitled[i] = c
	}
	if err := s.ReplaceChunks(id, "v", "h2", retitled); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(sensitivity.Owner(), SearchQuery{Text: "zeppelin", K: 5, Mode: ModeKeyword})
	if err != nil || len(hits) == 0 {
		t.Fatalf("retitled passage not found: %v %v", hits, err)
	}
	if err := s.ReplaceChunks(id, "v", "h3", nil); err != nil {
		t.Fatal(err)
	}
}

// A meeting is shown under its calendar event's subject, else the name
// Musubi gave it, else its own title.
func TestDisplayTitle(t *testing.T) {
	s, id, _ := digestStore(t, sensitivity.Internal)
	title := func() string {
		got, err := s.ItemTitles(sensitivity.Owner(), []string{id})
		if err != nil {
			t.Fatal(err)
		}
		return got[id].Title
	}
	if got := title(); got != "Planning sync" {
		t.Fatalf("own title %q", got)
	}
	if err := s.SetItemTitle(id, "Plan hand-off", "generated", "h"); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "Plan hand-off" {
		t.Errorf("given name %q", got)
	}
	ev := item.Item{ID: "m365-calendar:e1", Source: "m365-calendar", Ref: "e1", Kind: item.KindEvent, Title: "Weekly planning", OccurredAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC)}
	if _, err := s.UpsertItem(ev); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLinks("occurred_as", []Link{{ItemID: id, Rel: "occurred_as", OtherID: ev.ID, Score: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "Weekly planning" {
		t.Errorf("linked event %q", got)
	}
}
