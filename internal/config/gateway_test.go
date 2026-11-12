package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Models on the gateway take its key and headers; their own win; models
// elsewhere are left alone.
func TestGatewayApplies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`
[gateway]
url = "https://gw.test"
account = "acct1"
api_key = "keychain:gw-token"
token_id = "tok1"

[knowledge.threads.model]
provider = "anthropic"
url = "https://gw.test/anthropic-shim"
model = "claude-haiku-5-5"

[knowledge.status.model]
provider = "openai"
url = "https://gw.test/workers-ai-shim"
api_key = "env:OWN"
headers = { "cf-aig-skip-cache" = "false" }

[ask.model]
provider = "openai"
url = "https://inference.example.test"
api_key = "keychain:other"

[knowledge.label.classifier]
provider = "jev"
url = "https://api.cloudflare.com/client/v4/accounts/acct1/ai/run"
`), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	th := c.Knowledge.Threads.Model
	if th.APIKey != "keychain:gw-token" || th.Headers["cf-aig-metadata"] != `{"harness":"musubi","setup_version":"app","token_id":"tok1"}` ||
		th.Headers["cf-aig-collect-log-payload"] != "false" || th.Headers["cf-aig-skip-cache"] != "true" || th.Headers["cf-aig-gateway-id"] != "" {
		t.Errorf("threads: %+v", th)
	}
	st := c.Knowledge.Status.Model
	if st.APIKey != "env:OWN" || st.Headers["cf-aig-skip-cache"] != "false" || st.Headers["cf-aig-metadata"] == "" {
		t.Errorf("status kept its own: %+v", st)
	}
	if a := c.Ask.Model; a.APIKey != "keychain:other" || len(a.Headers) != 0 {
		t.Errorf("ask is off the gateway: %+v", a)
	}
	l := c.Knowledge.Label.Classifier
	if l.APIKey != "keychain:gw-token" || l.Headers["cf-aig-gateway-id"] != "ai-gw" || l.Headers["cf-aig-skip-cache"] != "" {
		t.Errorf("catalog: %+v", l)
	}
}
