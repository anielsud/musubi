package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

func TestLearnedAliases(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s.UpsertItem(item.Item{ID: "m:1", Source: "t", Ref: "m:1", Kind: item.KindMeeting, Title: "x", OccurredAt: at, Segments: []item.Segment{{Text: "x"}}})
	s.ReplaceChunks("m:1", "v", "h", []Chunk{{Seq: 0, SegFrom: 0, SegTo: 1, Text: "x"}})
	cs, _ := s.Chunks("m:1")
	s.ReplaceStatements("m:1", "v", "h", []Statement{
		{ChunkID: cs[0].ID, Line: 1, Kind: "fact", Text: "The Daka office is hiring.", SaidAt: at, Grounded: true},
		{ChunkID: cs[0].ID, Line: 2, Kind: "fact", Text: "Dhaka has 16 engineers.", SaidAt: at, Grounded: true},
	})
	if err := s.ProposeAlias(LearnedAlias{Keep: "Dhaka", Variant: "Daka", Kind: "spelling", Score: 3}); err != nil {
		t.Fatal(err)
	}
	as, _ := s.LearnedAliases("proposed")
	if len(as) != 1 {
		t.Fatalf("aliases %+v", as)
	}
	im, err := s.ImpactOf(as[0])
	if err != nil || im.Statements != 1 || im.Items != 1 || len(im.Sample) != 1 {
		t.Fatalf("impact %+v %v", im, err)
	}
	if sp, _ := s.ConfirmedSpellings(); len(sp) != 0 {
		t.Fatal("a proposed alias applied")
	}
	s.DecideAlias(as[0].ID, true, "owner")
	if sp, _ := s.ConfirmedSpellings(); sp["daka"] != "Dhaka" {
		t.Errorf("confirmed spellings %v", sp)
	}
	// Proposing again leaves the decision alone.
	s.ProposeAlias(LearnedAlias{Keep: "Dhaka", Variant: "Daka", Kind: "spelling", Score: 9})
	if as, _ := s.LearnedAliases("confirmed"); len(as) != 1 || as[0].Score != 3 {
		t.Errorf("decided alias changed: %+v", as)
	}
}
