package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Recorded model replies and embeddings, so tests that depend on a model
// run deterministically without one (CI has no model). In record mode a
// miss is passed to the real model and saved; in replay mode a miss is an
// error. The files live beside the test corpus and are committed.
//
// Keys cover the model name and the full request, so any prompt change is a
// miss; re-record with `make eval-record`.

type cacheFile struct {
	mu      sync.Mutex
	path    string
	entries map[string]string
	used    map[string]bool
	dirty   bool
}

func loadCache(path string) (*cacheFile, error) {
	c := &cacheFile{path: path, entries: map[string]string{}, used: map[string]bool{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return c, json.Unmarshal(b, &c.entries)
}

func (c *cacheFile) get(k string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[k]
	if ok {
		c.used[k] = true
	}
	return v, ok
}

func (c *cacheFile) put(k, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[k] = v
	c.used[k] = true
	c.dirty = true
}

// save writes the entries this run used, sorted, so re-recording drops
// replies to prompts that no longer exist and gives small diffs.
func (c *cacheFile) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty && len(c.used) == len(c.entries) {
		return nil
	}
	keys := make([]string, 0, len(c.used))
	for k := range c.used {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf []byte
	buf = append(buf, "{\n"...)
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(c.entries[k])
		buf = append(buf, "  "...)
		buf = append(buf, kb...)
		buf = append(buf, ": "...)
		buf = append(buf, vb...)
		if i < len(keys)-1 {
			buf = append(buf, ',')
		}
		buf = append(buf, '\n')
	}
	buf = append(buf, "}\n"...)
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	c.dirty = false
	return os.WriteFile(c.path, buf, 0o644)
}

func key(parts ...any) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// CachedLLM replays (and in record mode, records) chat replies.
type CachedLLM struct {
	Inner  LLM // nil in replay-only mode
	Model  string
	Record bool
	file   *cacheFile
}

func NewCachedLLM(path, model string, inner LLM, record bool) (*CachedLLM, error) {
	f, err := loadCache(path)
	if err != nil {
		return nil, err
	}
	return &CachedLLM{Inner: inner, Model: model, Record: record, file: f}, nil
}

func (c *CachedLLM) Local() bool       { return true }
func (c *CachedLLM) ModelName() string { return c.Model }
func (c *CachedLLM) Save() error       { return c.file.save() }

func (c *CachedLLM) Complete(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	k := key(c.Model, msgs, opt)
	if v, ok := c.file.get(k); ok {
		if onToken != nil {
			onToken(v)
		}
		return v, nil
	}
	if !c.Record || c.Inner == nil {
		return "", fmt.Errorf("no recorded reply for this request (key %s); run `make eval-record`", k)
	}
	v, err := c.Inner.Complete(ctx, msgs, opt, onToken)
	if err != nil {
		return "", err
	}
	c.file.put(k, v)
	return v, nil
}

// CachedEmbedder replays (and records) embeddings.
type CachedEmbedder struct {
	Inner  Embedder
	Name   string
	Record bool
	file   *cacheFile
}

func NewCachedEmbedder(path, model string, inner Embedder, record bool) (*CachedEmbedder, error) {
	f, err := loadCache(path)
	if err != nil {
		return nil, err
	}
	return &CachedEmbedder{Inner: inner, Name: model, Record: record, file: f}, nil
}

func (c *CachedEmbedder) Model() string { return c.Name }
func (c *CachedEmbedder) Local() bool   { return true }
func (c *CachedEmbedder) Save() error   { return c.file.save() }

func (c *CachedEmbedder) Embed(ctx context.Context, texts []string, query bool) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var missIdx []int
	var miss []string
	for i, t := range texts {
		if v, ok := c.file.get(key(c.Name, query, t)); ok {
			out[i] = decodeB64(v)
			continue
		}
		missIdx = append(missIdx, i)
		miss = append(miss, t)
	}
	if len(miss) == 0 {
		return out, nil
	}
	if !c.Record || c.Inner == nil {
		return nil, fmt.Errorf("no recorded embedding for %q; run `make eval-record`", clip(miss[0], 60))
	}
	vs, err := c.Inner.Embed(ctx, miss, query)
	if err != nil {
		return nil, err
	}
	for j, i := range missIdx {
		out[i] = vs[j]
		c.file.put(key(c.Name, query, miss[j]), encodeB64(vs[j]))
	}
	return out, nil
}

// Embeddings are stored as base64 float16-rounded float32 to keep the
// committed file small; retrieval is insensitive at that precision.
func encodeB64(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		u := math.Float32bits(x) &^ 0x1fff // drop low mantissa bits
		b[4*i], b[4*i+1], b[4*i+2], b[4*i+3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decodeB64(s string) []float32 {
	b, _ := base64.StdEncoding.DecodeString(s)
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24)
	}
	return v
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
