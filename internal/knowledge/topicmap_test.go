package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
)

func TestLouvain(t *testing.T) {
	// Two triangles joined by one weak link: two communities.
	es := []wedge{{0, 1, 3}, {1, 2, 3}, {0, 2, 3}, {3, 4, 3}, {4, 5, 3}, {3, 5, 3}, {2, 3, 0.2}}
	c := louvain(7, es)
	if c[0] != c[1] || c[1] != c[2] || c[3] != c[4] || c[4] != c[5] || c[0] == c[3] {
		t.Fatalf("communities %v", c)
	}
	if c[6] == c[0] || c[6] == c[3] {
		t.Errorf("unlinked node joined a community: %v", c)
	}
}

func TestThreadMapAndCompare(t *testing.T) {
	f := newCardFixture(t)
	if err := f.st.ClearThreads(); err != nil {
		t.Fatal(err)
	}
	th := &Threader{Store: f.st, LLM: &scriptedThreads{}, Chunk: 70, Parallel: 2}
	items := []ThreadItem{
		{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)},
		{ID: "m:3", Start: time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)},
		{ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)},
	}
	for _, it := range items {
		if _, err := th.Thread(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.PlaceClaimsInThreads(); err != nil {
		t.Fatal(err)
	}
	v := Views{Store: f.st, Scope: sensitivity.Owner(), Now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}

	// Before linking, threads have no centroids: the map has no meaning
	// layout (every topic would sit at one point).
	m, err := v.Map()
	if err != nil {
		t.Fatal(err)
	}
	if m.Meaning {
		t.Fatal("meaning layout without centroids")
	}
	if n, err := LinkThreads(f.st, nil); err != nil || n != 2 {
		t.Fatalf("linked %d threads: %v", n, err)
	}
	if m, err = v.Map(); err != nil {
		t.Fatal(err)
	}
	if !m.Meaning || len(m.Topics) != 2 || len(m.Items) != 3 {
		t.Fatalf("map: meaning %v, %d topics, %d items", m.Meaning, len(m.Topics), len(m.Items))
	}
	for _, tp := range m.Topics {
		if tp.Label == "Juniper programme" && (tp.Statements < 8 || len(tp.Activity) != 2) {
			t.Errorf("Juniper: %d statements over %d items", tp.Statements, len(tp.Activity))
		}
	}
	// Juniper and Atlas are unalike and never discussed together.
	if len(m.Edges) != 0 {
		t.Errorf("edges %+v", m.Edges)
	}
}
