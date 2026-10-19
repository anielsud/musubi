package provider

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/config"
)

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"` // system, user, assistant
	Content string `json:"content"`
}

type ChatOptions struct {
	// JSON constrains the reply to a JSON value.
	JSON bool
	// Schema, when set, constrains the reply to this JSON schema (structured
	// output): fewer malformed replies, which lets smaller models do the job.
	Schema      json.RawMessage `json:",omitempty"`
	Temperature float64
	MaxTokens   int
	// Think turns on the model's reasoning trace before it answers. The
	// fields below are omitted when unset so recorded replies stay keyed
	// as before.
	Think bool `json:",omitempty"`
	// TopP and TopK, when set, sample instead of decoding greedily (Qwen's
	// thinking mode loops when greedy).
	TopP float64 `json:",omitempty"`
	TopK int     `json:",omitempty"`
	// Timeout overrides callTimeout for calls known to run long (a
	// thinking pass over a long excerpt takes minutes).
	Timeout time.Duration `json:",omitempty"`
	// Context, when set, is the context window to load the model with
	// (Ollama; llama-server fixes it at start-up).
	Context int `json:",omitempty"`
	// Session groups calls that share a prompt start (one meeting's
	// segments, one item's threading). An OpenAI-compatible server is sent
	// it as x-session-affinity, which Baseten uses to route the calls to one
	// replica, where the shared start is served from cache at a discount.
	// Routing only: not part of a recorded reply's key.
	Session string `json:"-"`
	// Kind names the call (assign, reconcile, consolidate, merge, status)
	// in the spend ledger, to see which calls cost the time.
	Kind string `json:"-"`
	// Effort asks a hosted reasoning model for this reasoning effort ("low"),
	// over the configured one. Not part of a recorded reply's key.
	Effort string `json:"-"`
}

// Chat is a local chat model served by Ollama.
type Chat struct {
	URL, Model string
	client     *http.Client
}

// NewChat returns nil when the LLM is turned off.
func NewChat(c config.LLM) (*Chat, error) {
	switch c.Provider {
	case "", "none":
		return nil, nil
	case "ollama":
		return &Chat{URL: strings.TrimRight(c.URL, "/"), Model: c.Model, client: &http.Client{Timeout: 10 * time.Minute}}, nil
	}
	return nil, fmt.Errorf("unknown llm provider %q", c.Provider)
}

// NewLLM builds any configured chat model: Ollama, or an OpenAI-compatible
// server (llama.cpp's llama-server). nil when turned off.
func NewLLM(c config.LLM) (LLM, error) {
	if c.Provider == "openai" {
		return NewOpenAI(c), nil
	}
	ch, err := NewChat(c)
	if ch == nil || err != nil {
		return nil, err
	}
	return ch, nil
}

// LLM is what stages need from a chat model; Chat and Cached implement it.
type LLM interface {
	Complete(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error)
	Local() bool
	ModelName() string
}

func (c *Chat) ModelName() string { return c.Model }

func (c *Chat) Local() bool {
	return strings.Contains(c.URL, "127.0.0.1") || strings.Contains(c.URL, "localhost")
}

// callTimeout bounds one non-streamed call. Normal calls take 5–30 s; the
// local server occasionally crawls at a few seconds per token for minutes
// (seen while the CPU was saturated, where schema-constrained sampling runs),
// and a retry usually comes back at full speed.
const callTimeout = 2 * time.Minute

// Complete sends the conversation and returns the reply. If onToken is set,
// the reply is streamed to it as it is generated.
func (c *Chat) Complete(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	if onToken != nil {
		// A retry would repeat tokens already shown.
		return c.complete(ctx, msgs, opt, onToken)
	}
	return withRetry(ctx, "ollama", opt, func(ctx context.Context) (string, error) { return c.complete(ctx, msgs, opt, nil) })
}

// withRetry runs one non-streamed call under the call timeout, once more if
// it timed out.
func withRetry(ctx context.Context, name string, opt ChatOptions, call func(context.Context) (string, error)) (string, error) {
	limit := callTimeout
	if opt.Timeout > 0 {
		limit = opt.Timeout
	}
	var out string
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, limit)
		out, err = call(cctx)
		timedOut := cctx.Err() == context.DeadlineExceeded
		cancel()
		if err == nil || ctx.Err() != nil || !timedOut {
			return out, err
		}
	}
	return out, fmt.Errorf("%s: no reply within %s, twice: %w", name, limit, err)
}

func (c *Chat) complete(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	body := map[string]any{
		"model":    c.Model,
		"messages": msgs,
		"stream":   onToken != nil,
		// Reasoning traces would double latency for answers this short,
		// unless the caller asks for them.
		"think":      opt.Think,
		"keep_alive": "30m",
		"options":    map[string]any{"temperature": opt.Temperature, "num_ctx": cmp.Or(opt.Context, 8192)},
	}
	if opt.TopP > 0 {
		body["options"].(map[string]any)["top_p"] = opt.TopP
	}
	if opt.TopK > 0 {
		body["options"].(map[string]any)["top_k"] = opt.TopK
	}
	if opt.MaxTokens > 0 {
		body["options"].(map[string]any)["num_predict"] = opt.MaxTokens
	}
	if opt.JSON {
		body["format"] = "json"
	}
	if len(opt.Schema) > 0 {
		body["format"] = opt.Schema
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		return "", fmt.Errorf("ollama %s: %s", resp.Status, e.Error)
	}
	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var chunk struct {
			Message struct{ Content string } `json:"message"`
			Done    bool                     `json:"done"`
			Error   string                   `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &chunk); err != nil {
			continue
		}
		if chunk.Error != "" {
			return out.String(), fmt.Errorf("ollama: %s", chunk.Error)
		}
		out.WriteString(chunk.Message.Content)
		if onToken != nil && chunk.Message.Content != "" {
			onToken(chunk.Message.Content)
		}
		if chunk.Done {
			break
		}
	}
	return out.String(), sc.Err()
}
