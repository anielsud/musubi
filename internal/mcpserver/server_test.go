package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/sensitivity"
)

func synthetic() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..", "testdata", "synthetic")
}

// open builds the synthetic corpus into a fresh dataset and returns its
// engine; extra is appended to the config.
func open(t *testing.T, extra string) *engine.Engine {
	t.Helper()
	dir, syn := t.TempDir(), synthetic()
	base, err := os.ReadFile(filepath.Join(syn, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Microsoft 365 off: it is on by default, and with the developer's
	// sign-in in the Keychain an update would sync their real calendar and
	// chats into the synthetic inbox linked below (it did, 2026-11-11).
	// Tomoe context off: it writes calendar.json into the synthetic
	// corpus's Tomoe sessions.
	cfg := fmt.Sprintf("data_dir = %q\ntomoe_sessions = %q\n", dir, filepath.Join(syn, "tomoe")) + string(base) +
		"\n[embed]\nprovider = \"hash\"\n[llm]\nprovider = \"none\"\n[m365]\nenabled = false\ntomoe_context = false\n" + extra
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(syn, "inbox"), filepath.Join(dir, "inbox")); err != nil {
		t.Fatal(err)
	}
	e, err := engine.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	if _, err := e.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return e
}

func connect(t *testing.T, e *engine.Engine) *mcp.ClientSession {
	t.Helper()
	ms, err := New(e, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := ms.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call returns a tool's summary line and decoded body.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, map[string]any) {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("%s: %s", name, r.Content[0].(*mcp.TextContent).Text)
	}
	if len(r.Content) != 2 {
		t.Fatalf("%s: want a summary and a body, got %d parts", name, len(r.Content))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(r.Content[1].(*mcp.TextContent).Text), &body); err != nil {
		t.Fatal(err)
	}
	return r.Content[0].(*mcp.TextContent).Text, body
}

func callErr(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	if !r.IsError {
		t.Fatalf("%s %v: want an error", name, args)
	}
	return r.Content[0].(*mcp.TextContent).Text
}

func TestToolsOffered(t *testing.T) {
	e := open(t, "")
	cs := connect(t, e)
	ts, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tl := range ts.Tools {
		got[tl.Name] = tl
	}
	for _, n := range []string{"search", "gather", "threads", "thread", "meetings", "meeting", "source", "todos", "prep",
		"judge", "correct_statement", "undo_correction", "mark_todo", "undo_todo_mark"} {
		if got[n] == nil {
			t.Errorf("tool %s missing", n)
		}
	}
	if !got["search"].Annotations.ReadOnlyHint || got["judge"].Annotations.ReadOnlyHint {
		t.Error("read tools must be marked read-only and write tools not")
	}
	if p := got["search"].InputSchema.(map[string]any)["properties"].(map[string]any); p["for"] == nil {
		t.Error("search should take for")
	}

	e.Cfg.MCP.Write, e.Cfg.MCP.Tools = false, []string{"search", "judge"}
	ts, err = connect(t, e).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts.Tools) != 1 || ts.Tools[0].Name != "search" {
		t.Errorf("allow-list and write=false: got %d tools", len(ts.Tools))
	}
	e.Cfg.MCP.Enabled = false
	if _, err := New(e, "test"); err == nil {
		t.Error("mcp.enabled = false should refuse to start")
	}
}

// Every passage carries a block whose share agrees with its label, nothing
// is hidden in the default (owner) setting, and "for" marks what fits.
func TestSearchLabelsEverything(t *testing.T) {
	e := open(t, "")
	cs := connect(t, e)
	owner, err := e.Scope("owner")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := e.Search(context.Background(), owner, "budget", 25, nil)
	if err != nil {
		t.Fatal(err)
	}
	sum, body := call(t, cs, "search", map[string]any{"query": "budget", "limit": 25, "for": []string{"external"}})
	ps := body["passages"].([]any)
	if len(ps) != len(direct) || len(ps) == 0 {
		t.Fatalf("got %d passages, the owner's scope finds %d", len(ps), len(direct))
	}
	if !strings.Contains(sum, "Sensitivity of") || !strings.Contains(sum, "fit the named audience") {
		t.Errorf("summary %q", sum)
	}
	shares := map[string]bool{}
	for _, p := range ps {
		s := p.(map[string]any)["sensitivity"].(map[string]any)
		lvl, _ := sensitivity.ParseLevel(s["level"].(string))
		var names []string
		cs, _ := s["categories"].([]any)
		for _, c := range cs {
			names = append(names, c.(string))
		}
		cats, _ := sensitivity.ParseCategories(names)
		if want := sensitivity.ShareOf(sensitivity.Label{Level: lvl, Categories: cats}); string(want) != s["share"] {
			t.Errorf("share %v, want %s", s["share"], want)
		}
		if s["fits"] == nil {
			t.Fatal("for was given but fits is missing")
		}
		if s["fits"].(bool) && s["share"] != "public" {
			t.Errorf("a %s passage fits an external audience", s["share"])
		}
		shares[s["share"].(string)] = true
	}
	if !shares["party-only"] {
		t.Errorf("the synthetic corpus should yield party-only passages for this query; got %v", shares)
	}
}

func TestMaxShareCaps(t *testing.T) {
	e := open(t, "[mcp]\nmax_share = \"internal\"\n")
	cs := connect(t, e)
	_, body := call(t, cs, "search", map[string]any{"query": "budget", "limit": 25})
	for _, p := range body["passages"].([]any) {
		if sh := p.(map[string]any)["sensitivity"].(map[string]any)["share"]; sh != "internal" && sh != "public" {
			t.Errorf("max_share internal returned a %v passage", sh)
		}
	}
}

func TestSourcePagesAndAudits(t *testing.T) {
	e := open(t, "")
	cs := connect(t, e)
	_, body := call(t, cs, "search", map[string]any{"query": "budget", "limit": 1})
	id := body["passages"].([]any)[0].(map[string]any)["itemId"].(string)
	_, src := call(t, cs, "source", map[string]any{"itemId": id})
	if len(src["chunks"].([]any)) == 0 || src["party"] == nil {
		t.Errorf("source: %v chunks, party %v", len(src["chunks"].([]any)), src["party"])
	}
	if msg := callErr(t, cs, "source", map[string]any{"itemId": "no-such-item"}); !strings.Contains(msg, "no item") {
		t.Errorf("unknown item: %q", msg)
	}
	counts, err := e.Store.AuditCounts("mcp:owner")
	if err != nil {
		t.Fatal(err)
	}
	if counts["search"] != 1 || counts["source"] != 2 {
		t.Errorf("audited %v, want 1 search and 2 source calls", counts)
	}
}

func TestInputsChecked(t *testing.T) {
	cs := connect(t, open(t, ""))
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"search", map[string]any{"query": ""}},
		{"search", map[string]any{"query": "x", "since": "last week"}},
		{"search", map[string]any{"query": "x", "for": []string{"Dana"}}},
		{"thread", map[string]any{"id": 0}},
		{"judge", map[string]any{"statementId": 999999, "verdict": "verified"}},
		{"judge", map[string]any{"statementId": 1, "verdict": "maybe"}},
		{"mark_todo", map[string]any{"statementId": -3, "mark": "done"}},
		{"correct_statement", map[string]any{"statementId": 999999, "text": "x"}},
	} {
		callErr(t, cs, c.tool, c.args)
	}
}
