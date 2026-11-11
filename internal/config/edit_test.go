package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# My dataset
data_dir = "/tmp/data"  # where it lives

[knowledge.threads.model]
provider = "openai"   # Baseten, for now
url = "https://example.test"
model = "glm"

# the labeller
[knowledge.label.classifier]
provider = "jev"
`

func TestEditText(t *testing.T) {
	out, err := EditText(sample, []Edit{
		{Section: "knowledge.threads.model", Key: "provider", Value: "anthropic"},
		{Section: "knowledge.threads.model", Key: "reasoning_effort", Value: "medium"},
		{Section: "knowledge.threads.model", Key: "headers", Value: map[string]string{"b": "2", "cf-aig-metadata": `{"token_id":"t1"}`}},
		{Section: "knowledge.threads.model", Key: "url", Delete: true},
		{Section: "", Key: "data_dir", Value: "/tmp/other"},
		{Section: "gateway", Key: "account", Value: "acct"},
		{Section: "knowledge.digest", Key: "parallel", Value: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# My dataset\n",
		`data_dir = "/tmp/other"  # where it lives`,
		`provider = "anthropic"  # Baseten, for now`,
		"model = \"glm\"\nreasoning_effort = \"medium\"\nheaders = { \"b\" = \"2\", \"cf-aig-metadata\" = '{\"token_id\":\"t1\"}' }\n",
		"# the labeller\n[knowledge.label.classifier]\nprovider = \"jev\"",
		"\n[gateway]\naccount = \"acct\"\n",
		"\n[knowledge.digest]\nparallel = 4\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "example.test") {
		t.Error("deleted key still there")
	}
	// Every other line is as it was.
	if !strings.HasPrefix(out, "# My dataset\ndata_dir") {
		t.Errorf("start changed:\n%s", out)
	}

	// A value over several lines isn't guessed at.
	multi := "[live]\nterms = [\n  \"a\",\n]\n"
	if _, err := EditText(multi, []Edit{{Section: "live", Key: "terms", Value: "x"}}); err == nil {
		t.Error("multi-line value edited")
	}
	// Deleting what isn't there is no change.
	if out, _ := EditText(sample, []Edit{{Section: "nope", Key: "x", Delete: true}}); out != sample {
		t.Error("delete of a missing table changed the text")
	}
}

func TestTomlString(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       `"plain"`,
		`{"a":1}`:     `'{"a":1}'`,
		"it's \"q\"":  `"it's \"q\""`,
		"line\nbreak": `"line\u000Abreak"`,
		`C:\path`:     `'C:\path'`,
	} {
		if got := tomlString(in); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
}

// The file is changed only when the result loads, and the old one is kept.
func TestEditFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(sample), 0o600)
	if err := EditFile(p, []Edit{{Section: "knowledge.threads.model", Key: "model", Value: "claude-haiku-5-5"}}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.Knowledge.Threads.Model.Model != "claude-haiku-5-5" || c.Knowledge.Threads.Model.Provider != "openai" {
		t.Fatalf("loaded %+v %v", c.Knowledge.Threads.Model, err)
	}
	if b, _ := os.ReadFile(p + ".bak"); string(b) != sample {
		t.Error("backup isn't the file before the edit")
	}
	// A value of the wrong type for its key doesn't load: nothing changes.
	before, _ := os.ReadFile(p)
	if err := EditFile(p, []Edit{{Section: "knowledge.threads.model", Key: "price_in", Value: "cheap"}}); err == nil {
		t.Error("an edit that doesn't load was written")
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Error("file changed by a refused edit")
	}
}
