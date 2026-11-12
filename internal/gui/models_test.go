package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/config"
)

// A group's choice, written and loaded back, is the model it names, with
// the provider's key and headers in place of an earlier provider's.
func TestModelChoiceRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`[gateway]
url = "https://gw.test"
account = "acct1"
api_key = "keychain:gw"
token_id = "tok1"

[providers.baseten]
url = "https://bt.test"
api_key = "keychain:bt"

[knowledge.threads.model]
provider = "openai"  # GLM on Baseten
url = "https://inference.baseten.test"
model = "zai-org/GLM-5.3-Flash"
api_key = "keychain:baseten-api-key"
headers = { "x-old" = "1" }
price_in = 0.15

[knowledge.label.classifier]
provider = "jev"
url = "https://typesafe.test"
api_key = "keychain:jev"
min_p = 0.5
`), 0o600)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	models := []GatewayModel{
		{ID: "claude-haiku-5-5", Kind: "chat", Provider: "anthropic", PriceIn: 0.1, PriceOut: 0.5},
		{ID: "typesafe/jev", Kind: "decision", Provider: "jev"},
		{ID: "@cf/cloudflare/clef", Kind: "decision", Provider: "clef", PriceIn: 0.24},
	}
	var edits []config.Edit
	for _, c := range []struct{ sec, kind, ch, effort string }{
		{"knowledge.threads.model", "chat", "gateway|anthropic|claude-haiku-5-5", "medium"},
		{"knowledge.threads.model.fallback", "chat", "baseten|anthropic|zai-org/GLM-5.3-Flash", "low"},
		{"knowledge.label.classifier", "decision", "gateway|jev|typesafe/jev", ""},
		{"ask.fallback", "chat", "ollama|ollama|qwen3:8b", ""},
	} {
		e, err := slotEdits(&cfg, c.sec, c.kind, c.ch, c.effort, models)
		if err != nil {
			t.Fatal(c.sec, err)
		}
		edits = append(edits, e...)
	}
	if err := config.EditFile(p, edits); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.Load(p); err != nil {
		t.Fatal(err)
	}
	th := cfg.Knowledge.Threads.Model
	if th.Via != "gateway" || th.Provider != "anthropic" || th.URL != "https://gw.test/anthropic-shim" || th.Model != "claude-haiku-5-5" ||
		th.APIKey != "keychain:gw" || th.Headers["x-old"] != "" || th.Headers["cf-aig-metadata"] == "" || th.ReasoningEffort != "medium" || th.PriceIn != 0.1 {
		t.Errorf("threads: %+v", th)
	}
	if f := th.Fallback; f == nil || f.Via != "baseten" || f.Provider != "anthropic" || f.URL != "https://bt.test" || f.APIKey != "keychain:bt" || f.ReasoningEffort != "low" {
		t.Errorf("fallback: %+v", f)
	}
	l := cfg.Knowledge.Label.Classifier
	if l.Provider != "jev" || l.URL != cfg.Gateway.CatalogURL() || l.APIKey != "keychain:gw" || l.MinP != 0.5 || l.Headers["cf-aig-gateway-id"] != "ai-gw" {
		t.Errorf("labels: %+v", l)
	}
	g := cfg.Gateway
	for sec, want := range map[string]string{"knowledge.threads.model": "gateway|anthropic|claude-haiku-5-5", "knowledge.threads.model.fallback": "baseten|anthropic|zai-org/GLM-5.3-Flash",
		"knowledge.label.classifier": "gateway|jev|typesafe/jev", "ask.fallback": "ollama|ollama|qwen3:8b"} {
		if ch, _, _ := describe(modelAt(&cfg, sec), g); ch != want {
			t.Errorf("describe %s: %s", sec, ch)
		}
	}
	// Used by: what's selected, fallbacks marked, embeddings as search.
	u := usage(&cfg, g)
	if strings.Join(u["gateway|claude-haiku-5-5"], ",") != "Threading" || strings.Join(u["baseten|zai-org/GLM-5.3-Flash"], ",") != "Threading (fallback)" ||
		strings.Join(u["ollama|qwen3:8b"], ",") != "Ask (fallback),Live suggestions" || len(u["ollama|nomic-embed-text"]) != 1 {
		t.Errorf("usage: %v", u)
	}
	// A slot only takes its kind.
	for _, c := range []struct{ sec, kind, ch string }{
		{"live.relevance", "decision", "gateway|anthropic|claude-haiku-5-5"},
		{"ask.model", "chat", "gateway|clef|@cf/cloudflare/clef"},
		{"live.relevance", "decision", "baseten|openai|x"},
		{"llm", "local", "gateway|anthropic|claude-haiku-5-5"},
		{"ask.model", "chat", "nowhere|openai|x"},
	} {
		if _, err := slotEdits(&cfg, c.sec, c.kind, c.ch, "", models); err == nil {
			t.Errorf("%s took %s", c.sec, c.ch)
		}
	}
}

// Re-choosing a provider's model keeps the prices set for it.
func TestProviderPricesKept(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = map[string]config.Provider{"baseten": {URL: "https://bt.test"}}
	cfg.Ask.Model = config.LLM{Via: "baseten", Provider: "openai", Model: "m", PriceIn: 0.15}
	e, _ := slotEdits(&cfg, "ask.model", "chat", "baseten|anthropic|m", "", nil)
	for _, x := range e {
		if x.Key == "price_in" {
			t.Errorf("price touched: %+v", x)
		}
	}
	e, _ = slotEdits(&cfg, "ask.model", "chat", "baseten|openai|other", "", nil)
	found := false
	for _, x := range e {
		found = found || (x.Key == "price_in" && x.Delete)
	}
	if !found {
		t.Error("another model's price kept")
	}
}
