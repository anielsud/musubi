package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// TestStatementsNear checks statement search: nearest first, the scope
// applied (a confidential item is hidden from the shared screen), the
// excluded item left out, and the passage read back under the scope.
func TestStatementsNear(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	vecs := map[string][]float32{"a": {1, 0, 0}, "b": {0.8, 0.6, 0}, "c": {0, 0, 1}, "secret": {0.95, 0.31, 0}, "now": {1, 0.01, 0}}
	levels := map[string]sensitivity.Level{"secret": sensitivity.Confidential}
	for n, name := range []string{"a", "b", "c", "secret", "now"} {
		id := "t:" + name
		it := item.Item{ID: id, Source: "t", Ref: name, Kind: item.KindMeeting, Title: "meeting " + name, OccurredAt: time.Unix(int64(n+1)*86400, 0),
			Segments: []item.Segment{{Text: "said " + name}}}
		if _, err := s.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceChunks(id, "v", "h", []Chunk{{Seq: 0, SegTo: 1, Text: "said " + name}}); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceLabels(id, "v", "h", sensitivity.Label{Level: levels[name]}, nil, []string{"owner@corp.example"}); err != nil {
			t.Fatal(err)
		}
		cs, _ := s.Chunks(id)
		st := Statement{ChunkID: cs[0].ID, Kind: "fact", Text: "statement " + name, Holder: "Lee", SaidAt: it.OccurredAt, Status: "stated"}
		if err := s.ReplaceStatements(id, "v", "h", []Statement{st}); err != nil {
			t.Fatal(err)
		}
		un, _ := s.UnlinkedStatements()
		if _, err := s.NewClaim(un[0], vecs[name]); err != nil {
			t.Fatal(err)
		}
		if err := s.PutStatementVec(un[0].ID, "m", vecs[name]); err != nil {
			t.Fatal(err)
		}
	}
	q := []float32{1, 0, 0}
	order := func(sc sensitivity.Scope) string {
		hs, err := s.StatementsNear(sc, q, "m", 10, []string{"t:now"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WithPassages(sc, hs); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(hs); i++ {
			if hs[i].Sim > hs[i-1].Sim {
				t.Errorf("%s: not most similar first", sc.Name)
			}
		}
		best := map[string]float64{}
		for _, h := range hs {
			if h.Passage == nil || h.Passage.ItemID != h.ItemID {
				t.Errorf("%s: no passage for %s", sc.Name, h.Text)
			}
			best[h.Text] = h.Sim
		}
		var out string
		for _, name := range []string{"a", "secret", "b", "c", "now"} {
			if _, ok := best["statement "+name]; ok {
				out += name + " "
			}
		}
		// Sorted by similarity: a (1) > secret (0.95) > b (0.8) > c (0).
		var sims []float64
		for _, name := range []string{"a", "secret", "b", "c"} {
			if v, ok := best["statement "+name]; ok {
				sims = append(sims, v)
			}
		}
		for i := 1; i < len(sims); i++ {
			if sims[i] > sims[i-1] {
				t.Errorf("%s: similarities out of order: %v", sc.Name, sims)
			}
		}
		return fmt.Sprint(out)
	}
	if got := order(sensitivity.Owner()); got != "a secret b c " {
		t.Errorf("owner: %q", got)
	}
	if got := order(sensitivity.SharedScreen()); got != "a b c " {
		t.Errorf("shared screen: %q (the confidential statement must not show)", got)
	}
}
