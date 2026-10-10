// Package engine wires config, store, providers and the pipeline together,
// so the CLI and the desktop app run the same code.
package engine

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/mcpdrop"
	"github.com/anielsud/musubi/internal/source/tomoe"
	"github.com/anielsud/musubi/internal/store"
)

type Engine struct {
	Cfg      config.Config
	Store    *store.Store
	Embedder provider.Embedder // nil: keyword search only
	Chat     *provider.Chat    // nil: no LLM

	updating sync.Mutex
}

func Open(cfgPath string) (*Engine, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	emb, err := provider.New(cfg.Embed)
	if err != nil {
		st.Close()
		return nil, err
	}
	chat, err := provider.NewChat(cfg.LLM)
	if err != nil {
		st.Close()
		return nil, err
	}
	return &Engine{Cfg: cfg, Store: st, Embedder: emb, Chat: chat}, nil
}

func (e *Engine) Close() error { return e.Store.Close() }

// Runner builds a pipeline runner over the configured sources.
func (e *Engine) Runner(embs []provider.Embedder, log io.Writer) (*pipeline.Runner, error) {
	rs, err := raw.Open(e.Cfg.RawDir())
	if err != nil {
		return nil, err
	}
	var conns []source.Connector
	if e.Cfg.TomoeSessions != "" {
		conns = append(conns, tomoe.Connector{Dir: e.Cfg.TomoeSessions})
	}
	conns = append(conns, mcpdrop.Connector{Dir: e.Cfg.InboxDir()})
	if embs == nil && e.Embedder != nil {
		embs = []provider.Embedder{e.Embedder}
	}
	return &pipeline.Runner{Cfg: e.Cfg, Store: e.Store, Raw: rs, Connectors: conns, Embedders: embs, CuratedPath: e.Cfg.Registry(), Log: log}, nil
}

// Update ingests and processes everything new. Concurrent calls wait.
func (e *Engine) Update(ctx context.Context, log io.Writer) (pipeline.Stats, error) {
	e.updating.Lock()
	defer e.updating.Unlock()
	var st pipeline.Stats
	r, err := e.Runner(nil, log)
	if err != nil {
		return st, err
	}
	if err := r.Ingest(ctx, &st); err != nil {
		return st, err
	}
	err = r.Process(ctx, &st)
	return st, err
}

// Search embeds the query (if an embedder is configured) and runs a scoped
// hybrid search.
func (e *Engine) Search(ctx context.Context, sc sensitivity.Scope, text string, k int, exclude []string) ([]store.Hit, error) {
	q := store.SearchQuery{Text: text, K: k, Mode: store.ModeHybrid, Exclude: exclude}
	if e.Embedder != nil {
		v, err := e.Embedder.Embed(ctx, []string{text}, true)
		if err != nil {
			// Keyword search still works without the model server.
			q.Mode = store.ModeKeyword
		} else {
			q.Vec, q.Model = v[0], e.Embedder.Model()
		}
	}
	return e.Store.Search(sc, q)
}

// Scope parses a scope name with the owner's internal domains.
func (e *Engine) Scope(name string) (sensitivity.Scope, error) {
	return sensitivity.ParseScope(name, e.Cfg.Owner.InternalDomains)
}

// OwnerEntity is the owner's entity id, left out of graph neighbours.
func (e *Engine) OwnerEntity() string {
	if e.Cfg.Owner.Email == "" {
		return ""
	}
	return "person:" + strings.ToLower(e.Cfg.Owner.Email)
}

// EnsureOllama starts `ollama serve` if the configured server isn't
// answering and the config allows it. It returns whether it is up.
func (e *Engine) EnsureOllama(ctx context.Context) bool {
	url := e.Cfg.Embed.URL
	if e.Chat != nil {
		url = e.Chat.URL
	}
	up := func() bool {
		c := http.Client{Timeout: time.Second}
		resp, err := c.Get(strings.TrimRight(url, "/") + "/api/tags")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	if up() {
		return true
	}
	if !e.Cfg.LLM.StartOllama {
		return false
	}
	bin := ""
	for _, p := range []string{"/opt/homebrew/bin/ollama", "/usr/local/bin/ollama", "/usr/bin/ollama"} {
		if _, err := exec.LookPath(p); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		return false
	}
	cmd := exec.Command(bin, "serve")
	if err := cmd.Start(); err != nil {
		return false
	}
	go cmd.Wait()
	for i := 0; i < 20 && ctx.Err() == nil; i++ {
		time.Sleep(500 * time.Millisecond)
		if up() {
			return true
		}
	}
	return false
}
