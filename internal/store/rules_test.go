package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

func rulesStore(t *testing.T) (*Store, []int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s.UpsertItem(item.Item{ID: "m:1", Source: "t", Ref: "m:1", Kind: item.KindMeeting, Title: "x", OccurredAt: at, Segments: []item.Segment{{Text: "x"}}})
	s.ReplaceChunks("m:1", "v", "h", []Chunk{{Seq: 0, SegFrom: 0, SegTo: 1, Text: "x"}})
	cs, _ := s.Chunks("m:1")
	s.ReplaceStatements("m:1", "v", "h", []Statement{
		{ChunkID: cs[0].ID, Line: 1, Kind: "fact", Text: "Ilka owns the rollout.", SaidAt: at, Grounded: true},
		{ChunkID: cs[0].ID, Line: 2, Kind: "fact", Text: "Ilka said the date holds.", SaidAt: at, Grounded: true},
		{ChunkID: cs[0].ID, Line: 3, Kind: "fact", Text: "The Ilkan team ships.", SaidAt: at, Grounded: true},
	})
	ids, err := s.StatementsUsing("ilka")
	if err != nil || len(ids) != 2 {
		t.Fatalf("statements using: %v %v", ids, err)
	}
	return s, ids
}

// A rule is set outright: always, some uses, both right, or back to
// review; each reads as its mode, and the corrector applies it.
func TestAliasRules(t *testing.T) {
	s, ids := rulesStore(t)
	id, err := s.AddAlias("Ilka", "Ilke", "owner")
	if err != nil {
		t.Fatal(err)
	}
	mode := func() LearnedAlias {
		as, _ := s.LearnedAliases("")
		for _, a := range as {
			if a.ID == id {
				return a
			}
		}
		t.Fatal("alias gone")
		return LearnedAlias{}
	}
	text := func(sid int64) string {
		c, err := s.Corrector()
		if err != nil {
			t.Fatal(err)
		}
		sts, _ := s.StatementsByID([]int64{sid})
		st := sts[sid]
		c.Fix(&st)
		return st.Text
	}
	if err := s.SetAliasRule(id, RuleThese, []int64{ids[0]}, ids, "owner"); err != nil {
		t.Fatal(err)
	}
	if a := mode(); a.Mode() != RuleThese || len(a.Only()) != 1 {
		t.Fatalf("these: %+v", a)
	}
	if text(ids[0]) != "Ilke owns the rollout." || text(ids[1]) != "Ilka said the date holds." {
		t.Errorf("these applied as %q, %q", text(ids[0]), text(ids[1]))
	}
	// Narrowing to the other use replaces the first, not adds to it.
	s.SetAliasRule(id, RuleThese, []int64{ids[1]}, ids, "owner")
	if text(ids[0]) != "Ilka owns the rollout." || text(ids[1]) != "Ilke said the date holds." {
		t.Errorf("edited these applied as %q, %q", text(ids[0]), text(ids[1]))
	}
	s.SetAliasRule(id, RuleAlways, nil, nil, "owner")
	if a := mode(); a.Mode() != RuleAlways || text(ids[0]) != "Ilke owns the rollout." {
		t.Errorf("always: %+v %q", a, text(ids[0]))
	}
	sts, _ := s.StatementsByID(ids)
	if sts[ids[0]].Text != "Ilka owns the rollout." {
		t.Error("stored text rewritten")
	}
	s.SetAliasRule(id, RuleApart, nil, nil, "owner")
	if a := mode(); a.Mode() != RuleApart || text(ids[0]) != "Ilka owns the rollout." {
		t.Errorf("apart: %+v", a)
	}
	s.SetAliasRule(id, RuleReview, nil, nil, "owner")
	if a := mode(); a.Mode() != RuleReview {
		t.Errorf("review: %+v", a)
	}
	if err := s.SetAliasRule(id, RuleThese, nil, nil, "owner"); err == nil {
		t.Error("a rule for no uses accepted")
	}
}

// Before the replaced status, answering a proposal with another right form
// marked it rejected at the moment the new alias was made; opening the
// store again calls those replaced, and leaves a real rejection alone.
func TestReplacedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.ProposeAlias(LearnedAlias{Keep: "Ilke", Variant: "Ilka", Kind: "spelling"})
	s.ProposeAlias(LearnedAlias{Keep: "Ondra", Variant: "Ondro", Kind: "spelling"})
	as, _ := s.LearnedAliases("proposed")
	byVariant := map[string]int64{}
	for _, a := range as {
		byVariant[a.Variant] = a.ID
	}
	nid, _ := s.AddAlias("Ilka", "Ilko", "owner")
	s.DecideAlias(nid, true, "owner")
	s.DecideAlias(byVariant["Ilka"], false, "owner")
	s.db.Exec(`UPDATE learned_aliases SET at = at - 3600 WHERE id = ?`, byVariant["Ondro"])
	s.DecideAlias(byVariant["Ondro"], false, "owner")
	// As a build before the migration ledger left it: the fix ran on
	// every open then, and runs once on the first open with the ledger.
	s.db.Exec(`DELETE FROM migrations WHERE id = 'aliases-replaced'`)
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, _ := s.LearnedAliases("")
	for _, a := range all {
		switch a.Variant + "→" + a.Keep {
		case "Ilka→Ilke":
			if a.Mode() != RuleReplace || a.ReplacedBy() != nid {
				t.Errorf("not replaced: %+v", a)
			}
		case "Ilka→Ilko":
			if a.Mode() != RuleAlways {
				t.Errorf("new rule: %+v", a)
			}
		case "Ondro→Ondra":
			if a.Mode() != RuleApart {
				t.Errorf("a real rejection changed: %+v", a)
			}
		}
	}
}

func TestCanonicalPossessive(t *testing.T) {
	sp := Spellings{"ilka": "Ilke"}
	if got := sp.Canonical("Ilka's team and ilka, not Ilkas."); got != "Ilke's team and Ilke, not Ilkas." {
		t.Errorf("got %q", got)
	}
}
