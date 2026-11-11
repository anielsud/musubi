package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVia(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`
[providers.acme]
format = "openai"
url = "https://acme.test"
headers = { "x-team" = "a" }

[knowledge.threads.model]
via = "acme"
model = "m1"
headers = { "x-team" = "mine" }

[knowledge.threads.model.fallback]
via = "acme"
model = "m2"
`), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	th := c.Knowledge.Threads.Model
	if th.Provider != "openai" || th.URL != "https://acme.test" || th.APIKey != "keychain:musubi-provider-acme" || th.Headers["x-team"] != "mine" {
		t.Errorf("threads: %+v", th)
	}
	if f := th.Fallback; f == nil || f.URL != "https://acme.test" || f.Model != "m2" || f.Headers["x-team"] != "a" {
		t.Errorf("fallback: %+v", f)
	}
	os.WriteFile(p, []byte("[ask.model]\nvia = \"nope\"\n"), 0o600)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown provider: %v", err)
	}
}

// The recommended setup, applied over an older config, loads as intended
// and keeps what it doesn't cover.
func TestRecommended(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`data_dir = "/tmp/d"  # mine

[owner]
name = "Pat Example"

[gateway]
token_id = "tok9"

[knowledge.threads.model]
provider = "openai"
url = "https://old.test"
model = "glm"
api_key = "keychain:old"
headers = { "x-old" = "1" }
hedge_after = 30
`), 0o600)
	edits, err := RecommendedEdits()
	if err != nil {
		t.Fatal(err)
	}
	if err := EditFile(p, edits); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	th := c.Knowledge.Threads.Model
	if th.Provider != "anthropic" || th.Model != "claude-haiku-5-5" || th.ReasoningEffort != "medium" || th.APIKey != "keychain:ai-gateway-token" ||
		th.Headers["x-old"] != "" || th.HedgeAfter != 30 {
		t.Errorf("threads: %+v", th)
	}
	if f := th.Fallback; f == nil || f.URL != "https://inference.baseten.co" || f.APIKey != "keychain:baseten-api-key" || f.Model != "zai-org/GLM-5.3-Flash" {
		t.Errorf("fallback: %+v", f)
	}
	if c.Owner.Name != "Pat Example" || c.DataDir != "/tmp/d" || c.Gateway.TokenID != "tok9" || !c.Knowledge.AllowHosted {
		t.Errorf("kept: owner %q data %q token %q hosted %v", c.Owner.Name, c.DataDir, c.Gateway.TokenID, c.Knowledge.AllowHosted)
	}
	if c.Ask.Fallback.Model != "qwen3.8:27b" || c.Ask.Model.Fallback != nil || c.Knowledge.Label.Classifier.Provider != "jev" {
		t.Errorf("ask %+v / labels %+v", c.Ask, c.Knowledge.Label.Classifier)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `data_dir = "/tmp/d"  # mine`) {
		t.Error("an unrelated line changed")
	}
	// Applying it again changes nothing.
	again, _ := os.ReadFile(p)
	EditFile(p, edits)
	if after, _ := os.ReadFile(p); string(after) != string(again) {
		t.Error("not idempotent")
	}
}
