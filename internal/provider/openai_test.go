package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anielsud/musubi/internal/config"
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
