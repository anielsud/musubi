package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/mcpdrop"
	"github.com/anielsud/musubi/internal/source/tomoe"
	"github.com/anielsud/musubi/internal/store"
)

func corpusDir() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..", "testdata", "synthetic")
}

// Build ingests and processes the synthetic corpus into a fresh database.
func build(t *testing.T) (*Runner, *store.Store) {
	t.Helper()
	dir := corpusDir()
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rs, err := raw.Open(cfg.RawDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Cfg: cfg, Store: st, Raw: rs, Embedders: []provider.Embedder{provider.Hash{Dim: 64}},
		CuratedPath: filepath.Join(dir, "registry.toml"),
		Connectors:  []source.Connector{tomoe.Connector{Dir: filepath.Join(dir, "tomoe")}, mcpdrop.Connector{Dir: filepath.Join(dir, "inbox")}}}
	run(t, r)
	return r, st
}

func run(t *testing.T, r *Runner) Stats {
	t.Helper()
	var s Stats
	if err := r.Ingest(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if err := r.Process(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSecondRunDoesNothing(t *testing.T) {
	r, _ := build(t)
	s := run(t, r)
	if s.Changed != 0 || len(s.Ran) != 0 {
		t.Fatalf("second run changed %d items and ran %v; want nothing", s.Changed, s.Ran)
	}
}

func TestEveryItemLabelled(t *testing.T) {
	_, st := build(t)
	items, err := st.AllItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if _, _, err := st.ItemLabel(it.ID); err != nil {
			t.Errorf("%s: %v", it.ID, err)
		}
	}
}

func TestMeetingsLinkToEvents(t *testing.T) {
	_, st := build(t)
	links, err := st.LinksFrom("occurred_as")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"tomoe:11111111-0000-4000-8000-000000000001": "calendar:e1",
		"tomoe:11111111-0000-4000-8000-000000000002": "calendar:e2",
		"tomoe:11111111-0000-4000-8000-000000000007": "calendar:e7",
	}
	for m, e := range want {
		if links[m] != e {
			t.Errorf("%s linked to %q, want %s", m, links[m], e)
		}
	}
	if l, ok := links["tomoe:11111111-0000-4000-8000-000000000004"]; ok {
		t.Errorf("ad-hoc call should have no event, got %s", l)
	}
}

func TestForgetRemovesEverythingAndStaysForgotten(t *testing.T) {
	r, st := build(t)
	const id = "mail:m1"
	q := store.SearchQuery{Text: "calibration performance review", K: 50, Mode: store.ModeKeyword}
	if hits, _ := st.Search(sensitivity.Owner(), q); !hasItem(hits, id) {
		t.Fatal("precondition: m1 searchable")
	}
	h, used, err := st.Forget(id)
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		r.Raw.Delete(h)
	}
	if hits, _ := st.Search(sensitivity.Owner(), q); hasItem(hits, id) {
		t.Fatal("m1 still searchable after forget")
	}
	if c, _ := st.Chunks(id); len(c) != 0 {
		t.Fatalf("%d chunks left", len(c))
	}
	if m, _ := st.Mentions(id); len(m) != 0 {
		t.Fatalf("%d mentions left", len(m))
	}
	if _, err := r.Raw.Get(h); err == nil {
		t.Fatal("raw blob still present")
	}
	run(t, r) // the source still has it; ingest must not bring it back
	if hits, _ := st.Search(sensitivity.Owner(), q); hasItem(hits, id) {
		t.Fatal("m1 re-ingested after forget")
	}
}

func hasItem(hits []store.Hit, id string) bool {
	for _, h := range hits {
		if h.ItemID == id {
			return true
		}
	}
	return false
}

func TestAllDayBlocksAreNotLinked(t *testing.T) {
	_, st := build(t)
	links, _ := st.LinksFrom("occurred_as")
	for m, e := range links {
		if e == "calendar:e9" {
			t.Errorf("%s linked to the all-day block", m)
		}
	}
	if links["tomoe:11111111-0000-4000-8000-000000000004"] != "" {
		t.Error("ad-hoc call inside an all-day block got linked")
	}
}
