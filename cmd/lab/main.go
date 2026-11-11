// Command lab serves each step of Live's pipeline as JSON on localhost, for
// the prompt playground. Development tool: not part of the app, not
// installed. It reads and writes nothing except through the configured
// store and models.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/live"
)

// wire records every outbound HTTP call (Ollama, Jev, the hosted model,
// the embedder): the exact request and response bodies, for the playground.
type wireEntry struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int    `json:"status"`
	Req    string `json:"req"`
	Resp   string `json:"resp"`
	Ms     int64  `json:"ms"`
}

var (
	wireMu  sync.Mutex
	wireLog []*wireEntry
)

const wireMax = 120_000

type recBody struct {
	io.ReadCloser
	e   *wireEntry
	buf bytes.Buffer
}

func (r *recBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	wireMu.Lock()
	if room := wireMax - r.buf.Len(); room > 0 {
		r.buf.Write(p[:min(n, room)])
	}
	r.e.Resp = r.buf.String()
	wireMu.Unlock()
	return n, err
}

type recTransport struct{ base http.RoundTripper }

func (t recTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	e := &wireEntry{Method: req.Method, URL: req.URL.String()}
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(b))
		e.Req = string(b[:min(len(b), wireMax)])
	}
	t0 := time.Now()
	resp, err := t.base.RoundTrip(req)
	e.Ms = time.Since(t0).Milliseconds()
	if resp != nil {
		e.Status = resp.StatusCode
		resp.Body = &recBody{ReadCloser: resp.Body, e: e}
	}
	if err != nil {
		e.Resp = "error: " + err.Error()
	}
	wireMu.Lock()
	wireLog = append(wireLog, e)
	wireMu.Unlock()
	return resp, err
}

func main() {
	http.DefaultTransport = recTransport{base: http.DefaultTransport}
	cfgPath := flag.String("config", config.Path(), "config file")
	addr := flag.String("addr", "127.0.0.1:8766", "listen address (keep it on localhost)")
	flag.Parse()
	if err := loopbackOnly(*addr); err != nil {
		log.Fatal(err)
	}
	eng, err := engine.Open(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	defer eng.Close()
	var mu sync.Mutex // one step at a time, like Live's model lane
	var lab *live.Lab
	var asOf time.Time
	at := func(s string) (time.Time, error) {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return t, fmt.Errorf("asof: %w", err)
		}
		if lab == nil {
			if lab, err = live.NewLab(eng, t); err != nil {
				return t, err
			}
			asOf = t
		} else if !t.Equal(asOf) {
			if err = lab.SetAsOf(t); err != nil {
				return t, err
			}
			asOf = t
		}
		return t, nil
	}
	h := func(f func(ctx context.Context, body json.RawMessage) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body json.RawMessage
			if r.Method == "POST" {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
			}
			mu.Lock()
			defer mu.Unlock()
			wireMu.Lock()
			wireLog = nil
			wireMu.Unlock()
			t0 := time.Now()
			out, err := f(r.Context(), body)
			wireMu.Lock()
			wire := append([]*wireEntry(nil), wireLog...)
			wireMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "ms": time.Since(t0).Milliseconds(), "wire": wire})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"result": out, "ms": time.Since(t0).Milliseconds(), "wire": wire})
		}
	}
	scope := func(s string) string {
		if s == "" {
			return "owner"
		}
		return s
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/prompts", h(func(ctx context.Context, _ json.RawMessage) (any, error) { return live.LabPrompts(), nil }))
	mux.HandleFunc("/fast", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct{ Text, AsOf string }
		json.Unmarshal(b, &q)
		if _, err := at(q.AsOf); err != nil {
			return nil, err
		}
		return lab.Fast(q.Text), nil
	}))
	mux.HandleFunc("/rank", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf      string
			Sugs      []live.RankIn
			Owner     string
			OwnerMode string
			Recent    []string
			AutoBrief int
		}
		json.Unmarshal(b, &q)
		if _, err := at(q.AsOf); err != nil {
			return nil, err
		}
		if q.AutoBrief == 0 {
			q.AutoBrief = eng.Cfg.Live.AutoBrief
		}
		if q.Owner == "" {
			q.Owner = eng.Cfg.Owner.Name
		}
		d, dropped := lab.Rank(q.Sugs, q.Owner, q.OwnerMode, q.Recent, q.AutoBrief)
		return map[string]any{"decisions": d, "droppedAboutOwner": dropped, "autoBrief": q.AutoBrief}, nil
	}))
	mux.HandleFunc("/search", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf, Query, Scope string
			K                  int
		}
		json.Unmarshal(b, &q)
		if _, err := at(q.AsOf); err != nil {
			return nil, err
		}
		if q.K == 0 {
			q.K = 4
		}
		return lab.Search(ctx, q.Query, scope(q.Scope), q.K)
	}))
	mux.HandleFunc("/statements", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf, Query, Scope string
			K                  int
			Boost              map[string]float64
		}
		json.Unmarshal(b, &q)
		t, err := at(q.AsOf)
		if err != nil {
			return nil, err
		}
		if q.K == 0 {
			q.K = 8
		}
		out, err := lab.Statements(ctx, q.Query, scope(q.Scope), q.K, t, q.Boost)
		return map[string]any{"statements": out}, err
	}))
	mux.HandleFunc("/pool", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf, Title, Chunk, Scope, Question string
			Queries                             []string
			PerQuery, PerQueryStmts, Max, Open  int
			Boost                               map[string]float64
		}
		json.Unmarshal(b, &q)
		t, err := at(q.AsOf)
		if err != nil {
			return nil, err
		}
		if q.PerQuery == 0 && q.PerQueryStmts == 0 {
			q.PerQuery = 5
		}
		if q.Max == 0 {
			q.Max = 12
		}
		if q.Open == 0 {
			q.Open = 2
		}
		return lab.Pool(ctx, q.Title, q.Chunk, q.Queries, scope(q.Scope), q.PerQuery, q.PerQueryStmts, q.Max, q.Open, t, q.Question, q.Boost)
	}))
	mux.HandleFunc("/topic", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf, Query, Scope string
			Card               bool
		}
		json.Unmarshal(b, &q)
		t, err := at(q.AsOf)
		if err != nil {
			return nil, err
		}
		return lab.Topic(ctx, q.Query, scope(q.Scope), t, q.Card)
	}))
	mux.HandleFunc("/gate", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct{ AsOf, Query, Chunk, Summary, Question string }
		json.Unmarshal(b, &q)
		if _, err := at(q.AsOf); err != nil {
			return nil, err
		}
		return lab.Gate(ctx, q.Query, q.Chunk, q.Summary, q.Question)
	}))
	mux.HandleFunc("/llm", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf, Model, System, User string
			JSON                      bool
			Temp                      float64
			MaxTokens                 int
		}
		json.Unmarshal(b, &q)
		if _, err := at(q.AsOf); err != nil {
			return nil, err
		}
		out, err := lab.LLM(ctx, q.Model, q.System, q.User, q.JSON, q.Temp, q.MaxTokens)
		return map[string]any{"text": out}, err
	}))
	mux.HandleFunc("/pick", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf, Title, Window, Scope, Question string
			Queries                              []string
		}
		json.Unmarshal(b, &q)
		t, err := at(q.AsOf)
		if err != nil {
			return nil, err
		}
		return lab.Pick(ctx, q.Title, q.Window, q.Queries, t, scope(q.Scope), q.Question)
	}))
	mux.HandleFunc("/brief", h(func(ctx context.Context, b json.RawMessage) (any, error) {
		var q struct {
			AsOf, Title, People, Window, Asked, Before, Scope, Prompt string
			Topic                                                     int64
		}
		json.Unmarshal(b, &q)
		t, err := at(q.AsOf)
		if err != nil {
			return nil, err
		}
		return lab.CardBrief(ctx, q.Topic, q.Title, q.People, q.Asked, q.Before, q.Window, scope(q.Scope), t, q.Prompt)
	}))
	log.Printf("lab on http://%s (config %s)", *addr, *cfgPath)
	log.Fatal(http.ListenAndServe(*addr, localOnly(mux)))
}

// The lab serves meeting text and runs models with no sign-in, so it
// listens on loopback only.
func loopbackOnly(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("addr: %w", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("addr %q: the lab listens on localhost only", addr)
	}
	return nil
}

// localOnly refuses what a web page could send to a localhost server: a
// request from a browser origin, or one addressed to another host name
// (DNS rebinding). The lab's callers are local scripts.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) || r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
