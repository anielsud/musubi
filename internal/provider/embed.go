// Package provider holds the model interfaces stages call, with local
// implementations. Which provider may see which data is decided by scope,
// not here.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/anielsud/musubi/internal/config"
)

type Embedder interface {
	// Model names the vectors; it keys them in the store.
	Model() string
	// Embed returns one unit vector per text. query selects the query-side
	// prompt for models trained with asymmetric prompts.
	Embed(ctx context.Context, texts []string, query bool) ([][]float32, error)
	// Local reports whether text stays on this machine.
	Local() bool
}

// New builds the configured embedder; nil means embedding is off.
func New(c config.Embed) (Embedder, error) {
	switch c.Provider {
	case "", "none":
		return nil, nil
	case "hash":
		return Hash{Dim: 256}, nil
	case "ollama":
		return &Ollama{URL: strings.TrimRight(c.URL, "/"), Name: c.Model, client: &http.Client{Timeout: 5 * time.Minute}}, nil
	}
	return nil, fmt.Errorf("unknown embed provider %q", c.Provider)
}

// Ollama calls a local Ollama server's /api/embed.
type Ollama struct {
	URL, Name string
	client    *http.Client
}

func (o *Ollama) Model() string { return o.Name }
func (o *Ollama) Local() bool {
	return strings.Contains(o.URL, "127.0.0.1") || strings.Contains(o.URL, "localhost")
}

// Prompts the models were trained with. Without them retrieval quality drops
// noticeably for both.
func (o *Ollama) prefix(query bool) string {
	switch {
	case strings.HasPrefix(o.Name, "nomic-embed-text"):
		if query {
			return "search_query: "
		}
		return "search_document: "
	case strings.HasPrefix(o.Name, "qwen3-embedding"):
		if query {
			return "Instruct: Given a question, retrieve passages from meeting transcripts, emails and chats that answer it\nQuery: "
		}
	}
	return ""
}

func (o *Ollama) Embed(ctx context.Context, texts []string, query bool) ([][]float32, error) {
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = o.prefix(query) + t
	}
	body, _ := json.Marshal(map[string]any{"model": o.Name, "input": in, "truncate": true})
	req, err := http.NewRequestWithContext(ctx, "POST", o.URL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama (is `ollama serve` running?): %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama returned %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	for _, v := range out.Embeddings {
		normalize(v)
	}
	return out.Embeddings, nil
}

// Hash is a deterministic bag-of-words embedder for tests: no model, no
// network, stable across runs. It only captures word overlap.
type Hash struct{ Dim int }

func (h Hash) Model() string { return fmt.Sprintf("hash-%d", h.Dim) }
func (h Hash) Local() bool   { return true }

func (h Hash) Embed(_ context.Context, texts []string, _ bool) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, h.Dim)
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			f := fnv.New32a()
			f.Write([]byte(w))
			x := f.Sum32()
			sign := float32(1)
			if x&1 == 1 {
				sign = -1
			}
			v[int(x>>1)%h.Dim] += sign
		}
		normalize(v)
		out[i] = v
	}
	return out, nil
}

func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
}
