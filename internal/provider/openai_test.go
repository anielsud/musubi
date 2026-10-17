package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"true}\"},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", finish)
	}))
	defer srv.Close()
	t.Setenv("TEST_KEY", "sekret")
	// "127.0.0.1" marks a server local; a hostname that resolves to it doesn't.
	url := strings.Replace(srv.URL, "127.0.0.1", "localtest.me", 1)
	if _, err := net.LookupHost("localtest.me"); err != nil {
		t.Skip("needs localtest.me to resolve to 127.0.0.1")
	}
	llm, err := NewLLM(config.LLM{Provider: "openai", URL: url, Model: "m", APIKey: "env:TEST_KEY", ReasoningEffort: "medium"})
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
}
