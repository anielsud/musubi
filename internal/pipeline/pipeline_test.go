package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
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

// Overlapping invitations: the recording goes to the event its title names,
// and failing that to the one the owner accepted over one left unanswered.
func TestLinkPrefersTitleThenResponse(t *testing.T) {
	at := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	ev := func(id, title, resp string) item.Item {
		return item.Item{ID: id, Kind: item.KindEvent, Title: title, OccurredAt: at, EndedAt: at.Add(time.Hour), Native: map[string]string{"response": resp}}
	}
	rec := func(id, title string) item.Item {
		return item.Item{ID: id, Kind: item.KindMeeting, Title: title, OccurredAt: at.Add(2 * time.Minute), EndedAt: at.Add(50 * time.Minute)}
	}
	items := []item.Item{
		ev("calendar:all-hands", "Company All-Hands", "notResponded"),
		ev("calendar:roadmap", "Roadmap Review", "accepted"),
		ev("calendar:vendor", "Vendor Call", "declined"),
		rec("tomoe:a", "Microsoft Teams — Company All-Hands"),
		rec("tomoe:b", "Teams Meeting 2026-03-04 10:02"),
		rec("tomoe:c", "Microsoft Teams — Kai Moreno"), // an ad-hoc call during the events
	}
	_, links := linkMeetings(items)
	if links["tomoe:a"].id != "calendar:all-hands" {
		t.Errorf("titled recording linked to %q", links["tomoe:a"].id)
	}
	if links["tomoe:b"].id != "calendar:roadmap" {
		t.Errorf("untitled recording linked to %q, want the accepted event", links["tomoe:b"].id)
	}
	if l, ok := links["tomoe:c"]; ok {
		t.Errorf("ad-hoc call titled for someone else linked to %q", l.id)
	}
}
