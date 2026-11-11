package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anielsud/musubi/internal/config"
)

// A group's choice, written and loaded back, is the model it names, with
// the gateway's key and headers in place of the old provider's.
func TestModelChoiceRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`[gateway]
url = "https://gw.test"
account = "acct1"
api_key = "keychain:gw"
token_id = "tok1"

[knowledge.threads.model]
provider = "openai"  # GLM on Baseten
url = "https://inference.baseten.test"
model = "zai-org/GLM-5.3-Flash"
api_key = "keychain:baseten-api-key"
headers = { "x-old" = "1" }

[knowledge.label.classifier]
provider = "jev"
url = "https://typesafe.test"
api_key = "keychain:jev"
min_p = 0.5
`), 0o600)
	g := config.Gateway{URL: "https://gw.test", Account: "acct1", APIKey: "keychain:gw", TokenID: "tok1"}
	models := []GatewayModel{
		{ID: "claude-haiku-5-5", Kind: "chat", Provider: "anthropic", PriceIn: 0.1, PriceOut: 0.5},
		{ID: "typesafe/jev", Kind: "decision", Provider: "jev"},
		{ID: "@cf/cloudflare/clef", Kind: "decision", Provider: "clef", PriceIn: 0.24},
	}
	var edits []config.Edit
	e, err := slotEdits("knowledge.threads.model", "chat", "gw:claude-haiku-5-5", "medium", g, models)
	if err != nil {
		t.Fatal(err)
	}
	edits = append(edits, e...)
	e, _ = slotEdits("knowledge.threads.model.fallback", "chat", "local:qwen3.8:27b", "medium", g, models)
	edits = append(edits, e...)
	e, _ = slotEdits("knowledge.label.classifier", "decision", "gw:typesafe/jev", "", g, models)
	edits = append(edits, e...)
	if err := config.EditFile(p, edits); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	th := c.Knowledge.Threads.Model
	if th.Provider != "anthropic" || th.URL != "https://gw.test/anthropic-shim" || th.Model != "claude-haiku-5-5" ||
		th.APIKey != "keychain:gw" || th.Headers["x-old"] != "" || th.Headers["cf-aig-metadata"] == "" ||
		th.ReasoningEffort != "medium" || th.PriceIn != 0.1 {
		t.Errorf("threads: %+v", th)
	}
	if f := th.Fallback; f == nil || f.Provider != "ollama" || f.Model != "qwen3.8:27b" || f.APIKey != "" {
		t.Errorf("fallback: %+v", f)
	}
	l := c.Knowledge.Label.Classifier
	if l.Provider != "jev" || l.URL != g.CatalogURL() || l.APIKey != "keychain:gw" || l.MinP != 0.5 || l.Headers["cf-aig-gateway-id"] != "ai-gw" {
		t.Errorf("labels: %+v", l)
	}
	if ch, _, ef := describe(&c.Knowledge.Threads.Model, g); ch != "gw:claude-haiku-5-5" || ef != "medium" {
		t.Errorf("describe threads: %s %s", ch, ef)
	}
	if ch, _, _ := describe(&c.Knowledge.Label.Classifier, g); ch != "gw:typesafe/jev" {
		t.Errorf("describe labels: %s", ch)
	}
	if ch, txt, _ := describe(&config.LLM{Provider: "openai", URL: "https://inference.baseten.test", Model: "glm"}, g); ch != "custom" || txt == "" {
		t.Errorf("describe custom: %s %q", ch, txt)
	}
	// A decision slot can't take a chat model, nor a chat slot a decision one.
	if _, err := slotEdits("live.relevance", "decision", "gw:claude-haiku-5-5", "", g, models); err == nil {
		t.Error("chat model in a decision slot")
	}
	if _, err := slotEdits("ask.model", "chat", "gw:@cf/cloudflare/clef", "", g, models); err == nil {
		t.Error("decision model in a chat slot")
	}
}
