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

// A correction is kept to the side: it survives re-extraction (found again
// by the text it was extracted with) and can be undone.
func TestCorrectionsSurviveReextraction(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s.UpsertItem(item.Item{ID: "m:1", Source: "t", Ref: "m:1", Kind: item.KindMeeting, Title: "x", OccurredAt: at, Segments: []item.Segment{{Text: "x"}}})
	s.ReplaceChunks("m:1", "v", "h", []Chunk{{Seq: 0, SegFrom: 0, SegTo: 1, Text: "x"}})
	cs, _ := s.Chunks("m:1")
	extract := func() int64 {
		s.ReplaceStatements("m:1", "v", "h", []Statement{
			{ChunkID: cs[0].ID, Line: 1, Kind: "fact", Text: "The Daka office opens in May.", Who: []string{"Lee"}, SaidAt: at, Grounded: true},
			{ChunkID: cs[0].ID, Line: 2, Kind: "fact", Text: "Unrelated.", SaidAt: at, Grounded: true},
		})
		sts, _ := s.AllStatements()
		for _, x := range sts {
			if x.Line == 1 {
				return x.ID
			}
		}
		return 0
	}
	id := extract()
	if err := s.CorrectStatement(id, "The Dhaka office opens in May.", []string{"Dana Ruiz"}, "owner"); err != nil {
		t.Fatal(err)
	}
	id2 := extract() // re-extracted: rows replaced (ids may be reused), the same extracted text
	got, _ := s.StatementsByID([]int64{id2})
	if got[id2].Text != "The Dhaka office opens in May." || got[id2].Holder != "Dana Ruiz" {
		t.Fatalf("correction lost on re-extraction: %+v", got[id2])
	}
	var cid int64
	s.db.QueryRow(`SELECT id FROM corrections WHERE field = 'text'`).Scan(&cid)
	if _, err := s.UndoCorrection(cid); err != nil {
		t.Fatal(err)
	}
	got, _ = s.StatementsByID([]int64{id2})
	if got[id2].Text != "The Daka office opens in May." {
		t.Errorf("undo didn't restore the extracted text: %q", got[id2].Text)
	}
}

func TestAliasConfirmedForSomeUses(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "t.db"))
	defer s.Close()
	s.ProposeAlias(LearnedAlias{Keep: "Myles", Variant: "Miles", Kind: "spelling"})
	as, _ := s.LearnedAliases("proposed")
	if err := s.ConfirmAliasIn(as[0].ID, []int64{7, 9}, []int64{7, 8, 9}, "owner"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.ConfirmedSpellings(); len(g) != 0 {
		t.Errorf("confirmed for two uses, applied everywhere: %v", g)
	}
	sc, _ := s.ScopedSpellings()
	if sc[7]["miles"] != "Myles" || sc[9]["miles"] != "Myles" || sc[8] != nil {
		t.Errorf("scoped %v", sc)
	}
	// Reviewed: 7, 8, 9; a later use (10) isn't, so it comes back.
	as, _ = s.LearnedAliases("confirmed")
	if r := as[0].Reviewed(); !r[8] || r[10] {
		t.Errorf("reviewed %v", r)
	}
	// Reviewing 10 later, confirmed: it joins.
	s.ConfirmAliasIn(as[0].ID, []int64{10}, []int64{10}, "owner")
	sc, _ = s.ScopedSpellings()
	if sc[7] == nil || sc[10]["miles"] != "Myles" {
		t.Errorf("after a second review %v", sc)
	}
}

func TestAddAlias(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "t.db"))
	defer s.Close()
	id, err := s.AddAlias("Steele", "Sheetal", "owner")
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	if again, _ := s.AddAlias("Steele", "Sheetal", "owner"); again != id {
		t.Errorf("added twice: %d, %d", id, again)
	}
	s.ConfirmAliasIn(id, []int64{3}, []int64{3, 4}, "owner")
	sc, _ := s.ScopedSpellings()
	if sc[3]["steele"] != "Sheetal" || sc[4] != nil {
		t.Errorf("scoped %v", sc)
	}
}
