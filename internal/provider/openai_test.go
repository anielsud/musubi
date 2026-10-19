package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"os"
	"sync"
	"time"
)

func TestOpenAI(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		if got["max_tokens"] == float64(1) {
			w.Write([]byte(`{"choices":[{"logprobs":{"content":[{"top_logprobs":[{"token":"yes","logprob":-0.105},{"token":" No","logprob":-2.3},{"token":"maybe","logprob":-5}]}]}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	llm, err := NewLLM(config.LLM{Provider: "openai", URL: srv.URL + "/", Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if !llm.Local() {
		t.Error("127.0.0.1 server not local")
	}
	out, err := llm.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}},
		ChatOptions{Schema: json.RawMessage(`{"type":"object"}`), Think: true, TopP: 0.95, TopK: 20, Temperature: 0.6}, nil)
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("complete: %q %v", out, err)
	}
	if got["chat_template_kwargs"].(map[string]any)["enable_thinking"] != true || got["top_k"] != float64(20) ||
		got["response_format"].(map[string]any)["type"] != "json_schema" {
		t.Errorf("request body: %v", got)
	}
	p, err := llm.(Decider).Decide(context.Background(), []Message{{Role: "user", Content: "q"}}, []string{"yes", "no"})
	if err != nil || p["yes"] < 0.85 || p["yes"] > 0.95 {
		t.Errorf("decide: %v %v", p, err)
	}
}

func TestOpenAISessionAffinity(t *testing.T) {
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("x-session-affinity"))
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	llm, err := NewLLM(config.LLM{Provider: "openai", URL: srv.URL + "/", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Message{{Role: "user", Content: "hi"}}
	for _, s := range []string{"points m:1", "points m:1", "points m:2", ""} {
		if _, err := llm.Complete(context.Background(), msgs, ChatOptions{Session: s}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The same session routes alike, another differently, and none sends
	// no header. The value is a hash, not the meeting's name.
	if headers[0] == "" || headers[0] != headers[1] || headers[0] == headers[2] || headers[3] != "" || headers[0] == "points m:1" {
		t.Errorf("headers %q", headers)
	}
}

// A hosted endpoint: the key is sent, replies stream, the schema is made
// strict, and a reply cut off at the token cap is asked again with more.
func TestOpenAIHosted(t *testing.T) {
	var calls int
	var lastMax float64
	var auth, effort string
	var strict bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		auth = r.Header.Get("Authorization")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		lastMax = body["max_tokens"].(float64)
		effort, _ = body["reasoning_effort"].(string)
		js := body["response_format"].(map[string]any)["json_schema"].(map[string]any)
		strict = js["strict"] == true && js["schema"].(map[string]any)["additionalProperties"] == false
		w.Header().Set("Content-Type", "text/event-stream")
		finish := "stop"
		if calls == 1 {
			finish = "length"
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"ok\\\":\"}}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"true}\"},\"finish_reason\":%q}]}\n\n", finish)
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000000,\"completion_tokens\":2000000}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("TEST_KEY", "sekret")
	ledger := filepath.Join(t.TempDir(), "spend.jsonl")
	t.Setenv("MUSUBI_SPEND_LEDGER", ledger)
	// "127.0.0.1" marks a server local; a hostname that resolves to it doesn't.
	url := strings.Replace(srv.URL, "127.0.0.1", "localtest.me", 1)
	if _, err := net.LookupHost("localtest.me"); err != nil {
		t.Skip("needs localtest.me to resolve to 127.0.0.1")
	}
	llm, err := NewLLM(config.LLM{Provider: "openai", URL: url, Model: "m", APIKey: "env:TEST_KEY", ReasoningEffort: "medium", PriceIn: 0.15, PriceOut: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if llm.Local() {
		t.Fatal("hosted endpoint taken for local")
	}
	out, err := llm.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}},
		ChatOptions{Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`), MaxTokens: 100, Effort: "low"}, nil)
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("reply %q %v", out, err)
	}
	if calls != 2 || lastMax != 2*hostedMinTokens || auth != "Bearer sekret" || effort != "low" || !strict {
		t.Errorf("calls %d, max %v, auth %q, effort %q, strict %v", calls, lastMax, auth, effort, strict)
	}
	// Both calls metered: 2 × (1M in × $0.15 + 2M out × $0.50).
	if got := ledgerTotal(ledger); got < 2.29 || got > 2.31 {
		t.Errorf("ledger total %v, want 2.30", got)
	}
	t.Setenv("MUSUBI_SPEND_CAP", "2")
	if _, err := llm.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, ChatOptions{}, nil); err == nil || !strings.Contains(err.Error(), "spend cap") {
		t.Errorf("past the cap: %v", err)
	}
}

// A call slower than hedge_after is sent again; the first reply wins and
// the slow one is cut off (and still metered, from what it streamed).
func TestOpenAIHedged(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		if n == 1 { // stalls after a chunk
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"slow\"}}]}\n\n")
			fl.Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fast\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	url := strings.Replace(srv.URL, "127.0.0.1", "localtest.me", 1)
	if _, err := net.LookupHost("localtest.me"); err != nil {
		t.Skip("needs localtest.me to resolve to 127.0.0.1")
	}
	ledger := filepath.Join(t.TempDir(), "spend.jsonl")
	t.Setenv("MUSUBI_SPEND_LEDGER", ledger)
	llm, _ := NewLLM(config.LLM{Provider: "openai", URL: url, Model: "m", HedgeAfter: 0.2, PriceIn: 1, PriceOut: 1})
	t0 := time.Now()
	out, err := llm.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, ChatOptions{}, nil)
	if err != nil || out != "fast" {
		t.Fatalf("got %q, %v", out, err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("waited for the slow call: %s", d)
	}
	time.Sleep(200 * time.Millisecond) // the loser's deferred record
	b, _ := os.ReadFile(ledger)
	if rows := strings.Count(string(b), "\n"); rows != 2 || !strings.Contains(string(b), `"finish":"cut"`) {
		t.Errorf("ledger:\n%s", b)
	}
}

// A reply that reasons to the cap without answering is asked again twice
// at once at the same cap (a loop is a sample, not a lack of room).
func TestOpenAIRunaway(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var caps []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls++
		n := calls
		caps = append(caps, body["max_tokens"].(float64))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hmm\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	url := strings.Replace(srv.URL, "127.0.0.1", "localtest.me", 1)
	if _, err := net.LookupHost("localtest.me"); err != nil {
		t.Skip("needs localtest.me to resolve to 127.0.0.1")
	}
	llm, _ := NewLLM(config.LLM{Provider: "openai", URL: url, Model: "m"})
	out, err := llm.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, ChatOptions{MaxTokens: 100}, nil)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || out != "ok" || calls < 2 {
		t.Fatalf("got %q, %v after %d calls", out, err, calls)
	}
	for _, c := range caps {
		if c != caps[0] {
			t.Errorf("caps %v: a runaway shouldn't double the cap", caps)
		}
	}
}
