package eval

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/mcpdrop"
	"github.com/anielsud/musubi/internal/source/tomoe"
	"github.com/anielsud/musubi/internal/store"
)

// TestSyntheticCorpus is the CI gate: no leaks, and classification and
// resolution exactly as the cases say. Retrieval with the hash embedder is
// reported, not gated; real embedders are compared with `musubi eval`.
func TestSyntheticCorpus(t *testing.T) {
	_, f, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(f), "..", "..", "testdata", "synthetic")
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rs, _ := raw.Open(cfg.RawDir())
	emb := provider.Hash{Dim: 256}
	r := &pipeline.Runner{Cfg: cfg, Store: st, Raw: rs, Embedders: []provider.Embedder{emb}, CuratedPath: filepath.Join(dir, "registry.toml"),
		Connectors: []source.Connector{tomoe.Connector{Dir: filepath.Join(dir, "tomoe")}, mcpdrop.Connector{Dir: filepath.Join(dir, "inbox")}}}
	var s pipeline.Stats
	ctx := context.Background()
	if err := r.Ingest(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err := r.Process(ctx, &s); err != nil {
		t.Fatal(err)
	}
	suite, err := Load(filepath.Join(dir, "cases.toml"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Harness{Store: st, Embedder: emb, InternalDomains: cfg.Owner.InternalDomains, Modes: []store.SearchMode{store.ModeHybrid, store.ModeKeyword, store.ModeVector}}
	rep, err := h.Run(ctx, suite)
	if err != nil {
		t.Fatal(err)
	}
	if testing.Verbose() {
		rep.Print(os.Stdout, nil)
	}
	if n := rep.Leaks(); n != 0 {
		rep.Print(os.Stdout, nil)
		t.Fatalf("%d leaks", n)
	}
	for _, k := range []string{"classify.pass", "resolve.pass", "graph.pass"} {
		if v := rep.Summary[k]; v != 1 {
			rep.Print(os.Stdout, nil)
			t.Errorf("%s = %.3f, want 1", k, v)
		}
	}
}
