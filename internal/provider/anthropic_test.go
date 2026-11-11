package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/anielsud/musubi/internal/config"
)

// sse writes an Anthropic Messages stream: the text in two pieces, then
// the stop reason.
func sse(w http.ResponseWriter, text, stop string) {
	w.Header().Set("Content-Type", "text/event-stream")
	half := len(text) / 2
	fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":900000,\"cache_read_input_tokens\":100000,\"output_tokens\":1}}}\n\n")
	fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n")
	for _, p := range []string{text[:half], text[half:]} {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", b)
	}
	fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":2000000}}\n\n", stop)
	fmt.Fprintf(w, "data: {\"type\":\"message_stop\"}\n\n")
}

// A Claude endpoint: the key and configured headers are sent, sampling
// settings aren't, thinking is adaptive at the configured effort (medium
// when unset), the schema is made strict, and the call is metered.
func TestAnthropic(t *testing.T) {
	var body map[string]any
	var key, version, meta, path string
	reply, stop := `{"ok":true}`, "end_turn"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key, version, meta = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"), r.Header.Get("cf-aig-metadata")
		body = nil
		json.NewDecoder(r.Body).Decode(&body)
		sse(w, reply, stop)
	}))
	defer srv.Close()
	t.Setenv("TEST_KEY", "sekret")
	ledger := filepath.Join(t.TempDir(), "spend.jsonl")
	t.Setenv("MUSUBI_SPEND_LEDGER", ledger)
	c := config.LLM{Provider: "anthropic", URL: srv.URL + "/", Model: "claude-haiku-5-5", APIKey: "env:TEST_KEY",
		Headers: map[string]string{"cf-aig-metadata": `{"token_id":"t1"}`}, PriceIn: 0.1, PriceOut: 0.5}
	llm, err := NewLLM(c)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "hi"}}
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)
	out, err := llm.Complete(context.Background(), msgs, ChatOptions{Schema: schema, Temperature: 0.7, TopP: 0.9, Effort: "low"}, nil)
	if err != nil || out != reply {
		t.Fatalf("reply %q %v", out, err)
	}
	if path != "/v1/messages" || key != "sekret" || version == "" || meta != `{"token_id":"t1"}` {
		t.Errorf("path %q key %q version %q meta %q", path, key, version, meta)
	}
	if _, ok := body["temperature"]; ok {
		t.Error("temperature sent")
	}
	if _, ok := body["top_p"]; ok {
		t.Error("top_p sent")
	}
	oc := body["output_config"].(map[string]any)
	// The call asked for low; nothing configured, so the call's hint stands.
	if oc["effort"] != "low" || body["system"] != "rules" || body["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Errorf("effort %v system %v thinking %v", oc["effort"], body["system"], body["thinking"])
	}
	sc := oc["format"].(map[string]any)["schema"].(map[string]any)
	if sc["additionalProperties"] != false {
		t.Errorf("schema not strict: %v", sc)
	}
	// 1M in × $0.10 + 2M out × $0.50.
	if got := ledgerTotal(ledger); got < 1.09 || got > 1.11 {
		t.Errorf("ledger total %v, want 1.10", got)
	}

	// A configured level wins over the call's hint; none at all is medium.
	c.ReasoningEffort = "high"
	llm, _ = NewLLM(c)
	llm.Complete(context.Background(), msgs, ChatOptions{Schema: schema, Effort: "low"}, nil)
	if e := body["output_config"].(map[string]any)["effort"]; e != "high" {
		t.Errorf("configured effort: %v", e)
	}
	c.ReasoningEffort = ""
	llm, _ = NewLLM(c)
	llm.Complete(context.Background(), msgs, ChatOptions{Schema: schema}, nil)
	if e := body["output_config"].(map[string]any)["effort"]; e != "medium" {
		t.Errorf("default effort: %v", e)
	}

	// JSON without a schema: a fenced reply is unwrapped.
	reply = "```json\n{\"a\":1}\n```"
	if out, err := llm.Complete(context.Background(), msgs, ChatOptions{JSON: true}, nil); err != nil || out != `{"a":1}` {
		t.Errorf("fenced JSON: %q %v", out, err)
	}

	// Cut off at the model's limit: an ErrCutOff, which callers may skip.
	reply, stop = "{", "max_tokens"
	if _, err := llm.Complete(context.Background(), msgs, ChatOptions{}, nil); !errors.Is(err, ErrCutOff) {
		t.Errorf("cut off: %v", err)
	}
}
