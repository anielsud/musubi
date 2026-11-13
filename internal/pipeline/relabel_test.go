package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/tomoe"
	"github.com/anielsud/musubi/internal/store"
)

// relabelLLM extracts, from every passage, one statement held by the
// speaker of its first line and naming them, and reads meetings into
// digests that name their speakers; it counts its calls.
type relabelLLM struct{ calls atomic.Int32 }

func (*relabelLLM) Local() bool       { return true }
func (*relabelLLM) ModelName() string { return "relabel-fake" }
func (l *relabelLLM) Complete(_ context.Context, msgs []provider.Message, opt provider.ChatOptions, _ func(string)) (string, error) {
	l.calls.Add(1)
	switch opt.Kind {
	case "digest-summary":
		return `{"overview":"Corner Tile and Bo Example planned the launch.","sections":[{"subject":"Launch","summary":"Corner Tile proposed May.","people":["Corner Tile","Bo Example"],"lines":[[1,2]],"sensitive":[]}]}`, nil
	case "digest-todos":
		return `{"todos":[{"text":"Corner Tile will send the plan.","owner":["Corner Tile"],"requester":"Bo Example","agreed":"agreed","due_text":"","due_date":"","evidence":[{"line":1,"quote":"x"}]}]}`, nil
	}
	user := msgs[len(msgs)-1].Content
	if strings.Contains(user, "Corner Tile:") {
		return `{"statements":[{"line":1,"kind":"fact","text":"Corner Tile wants the launch moved to May.","holder":"Corner Tile","certainty":0.9,"quote":""},
			{"line":2,"kind":"question","text":"Bo Example asked whether May works.","holder":"Bo Example","certainty":0.9,"quote":""}]}`, nil
	}
	return `{"statements":[]}`, nil
}

type relabelSeg struct {
	Speaker string  `json:"speaker"`
	Text    string  `json:"text"`
	Start   float64 `json:"start_time"`
	End     float64 `json:"end_time"`
}

func writeRelabelSession(t *testing.T, dir, id string, speakers ...string) []byte {
	t.Helper()
	texts := []string{"We should move the launch to May, the beta needs another month.", "Does May work for the sales team and the partners?",
		"It works if the pricing page is ready.", "Then May it is, I will write it up."}
	var segs []relabelSeg
	for i, sp := range speakers {
		segs = append(segs, relabelSeg{sp, texts[i%len(texts)], float64(i * 10), float64(i*10 + 8)})
	}
	b, _ := json.MarshalIndent(map[string]any{"id": id, "title": "Launch sync", "created_at": "2026-03-04T10:00:00.123456-05:00", "duration": 60.0, "segments": segs}, "", "  ")
	if err := os.MkdirAll(filepath.Join(dir, id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id, "session.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// A relabel rewrites every stored copy of the speaker label in one
// meeting, leaves the statements and threads as they are, and the next
// import of the rewritten session changes nothing and runs nothing; undo
// puts it all back.
func TestRelabelSpeaker(t *testing.T) {
	ctx := context.Background()
	sessions := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Owner = config.Owner{Name: "Jordan Pike", Email: "jordan.pike@northwind.example", InternalDomains: []string{"northwind.example"}}
	cfg.Knowledge.Gate, cfg.Knowledge.MinWorth = 0, 0
	// The fake model answers the statements prompt.
	cfg.Knowledge.Extract, cfg.Knowledge.Grouping = "statements", "topics"
	cfg.Knowledge.Label.Enabled, cfg.Knowledge.ProposeAliases, cfg.Knowledge.NameMeetings = false, false, false
	cfg.Knowledge.Digest.Enabled = true
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rs, err := raw.Open(cfg.RawDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Cfg: cfg, Store: st, Raw: rs, Embedders: []provider.Embedder{provider.Hash{Dim: 64}},
		CuratedPath: filepath.Join(t.TempDir(), "none.toml"), Connectors: []source.Connector{tomoe.Connector{Dir: sessions}}}
	orig := writeRelabelSession(t, sessions, "s-1", "Person 3 (Corner Tile)", "Person 4 (Bo Example)", "You", "Person 3 (Corner Tile)")
	writeRelabelSession(t, sessions, "s-2", "Person 2 (Corner Tile)", "You")
	run(t, r)
	llm := &relabelLLM{}
	know := func(plan bool) KnowledgeStats {
		return knowledgeRun(t, r, KnowledgeOptions{LLM: llm, DigestLLM: llm, Plan: plan})
	}
	know(false)
	db, err := sql.Open("sqlite3", "file:"+cfg.DBPath()+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	nStatements := count(t, db, `SELECT count(*) FROM statements`)
	if count(t, db, `SELECT count(*) FROM statements WHERE item_id = 'tomoe:s-1' AND holder = 'Corner Tile'`) == 0 {
		t.Fatal("fixture: no statement held by the old name")
	}

	// A thread holding s-1's statements, with a status written from them.
	rw, err := sql.Open("sqlite3", "file:"+cfg.DBPath()+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`INSERT INTO topics(id, label, summary, label_level, size) VALUES (900, 'Launch date', 'When to launch, as Corner Tile and Bo Example see it', 0, 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`INSERT INTO thread_items(topic_id, statement_id) SELECT 900, id FROM statements WHERE item_id = 'tomoe:s-1'`); err != nil {
		t.Fatal(err)
	}
	statusHash := func() string {
		th, err := st.ThreadStatements(900)
		if err != nil {
			t.Fatal(err)
		}
		fix, _ := st.Corrector()
		for i := range th {
			th[i].Text = fix.Text(th[i].ID, th[i].Text)
		}
		var label, summary string
		rw.QueryRow(`SELECT label, summary FROM topics WHERE id = 900`).Scan(&label, &summary)
		full, _ := statusInputs(label, summary, th)
		return full.Hash
	}
	if _, err := rw.Exec(`INSERT INTO thread_status(topic_id, variant, version, input_hash, body, level, categories, as_of) VALUES (900, 'full', ?, ?, '{"now":"Corner Tile wants May."}', 0, 0, 0)`,
		knowledge.StatusVersion, statusHash()); err != nil {
		t.Fatal(err)
	}
	nThreadItems := count(t, db, `SELECT count(*) FROM thread_items`)

	// The recorder relabels s-1's file; Musubi relabels its copies.
	relabelled := writeRelabelSession(t, sessions, "s-1", "Person 3 (Ana Example)", "Person 4 (Bo Example)", "You", "Person 3 (Ana Example)")
	calls := llm.calls.Load()
	// A check says it holds and writes nothing.
	if rep, err := r.RelabelSpeaker(ctx, RelabelRequest{ItemID: "tomoe:s-1", Session: relabelled, Check: true}); err != nil || rep.Lines != 2 {
		t.Fatalf("check: %+v, %v", rep, err)
	}
	if n := count(t, db, `SELECT count(*) FROM items WHERE id = 'tomoe:s-1' AND segments LIKE '%Corner Tile%'`); n != 1 {
		t.Error("the check wrote the item")
	}
	rep, err := r.RelabelSpeaker(ctx, RelabelRequest{ItemID: "tomoe:s-1", Session: relabelled, Batch: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls.Load() != calls {
		t.Error("the relabel called the model")
	}
	if rep.Lines != 2 || rep.Names["Corner Tile"] != "Ana Example" || rep.Passages == 0 || rep.Holders == 0 || rep.Texts == 0 || rep.EmbeddedPassages == 0 || rep.EmbeddedStatements == 0 {
		t.Errorf("report = %+v", rep)
	}
	for _, q := range []string{
		`SELECT count(*) FROM items WHERE id = 'tomoe:s-1' AND (segments LIKE '%Corner Tile%' OR participants LIKE '%Corner Tile%')`,
		`SELECT count(*) FROM chunks WHERE item_id = 'tomoe:s-1' AND (text LIKE '%Corner Tile%' OR speakers LIKE '%Corner Tile%')`,
		`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH '"Corner Tile"' AND rowid IN (SELECT id FROM chunks WHERE item_id = 'tomoe:s-1')`,
		`SELECT count(*) FROM statements WHERE item_id = 'tomoe:s-1' AND (holder LIKE '%Corner Tile%' OR text LIKE '%Corner Tile%' OR who LIKE '%Corner Tile%' OR search_text LIKE '%Corner Tile%')`,
		`SELECT count(*) FROM meeting_sections WHERE item_id = 'tomoe:s-1' AND (people LIKE '%Corner Tile%' OR text LIKE '%Corner Tile%')`,
		`SELECT count(*) FROM meeting_todos WHERE item_id = 'tomoe:s-1' AND (owners LIKE '%Corner Tile%' OR text LIKE '%Corner Tile%')`,
		`SELECT count(*) FROM meeting_digests WHERE item_id = 'tomoe:s-1' AND overview LIKE '%Corner Tile%'`,
		`SELECT count(*) FROM statements st WHERE item_id = 'tomoe:s-1' AND text LIKE '%Ana Example%' AND NOT EXISTS (SELECT 1 FROM statement_vecs v WHERE v.statement_id = st.id)`,
	} {
		if n := count(t, db, q); n != 0 {
			t.Errorf("%d rows: %s", n, q)
		}
	}
	var body, summary, hash string
	rw.QueryRow(`SELECT s.body, t.summary, s.input_hash FROM thread_status s JOIN topics t ON t.id = s.topic_id WHERE s.topic_id = 900`).Scan(&body, &summary, &hash)
	if !strings.Contains(body, "Ana Example") || !strings.Contains(summary, "Ana Example") || hash != statusHash() || rep.Statuses != 1 {
		t.Errorf("thread after the relabel: %q, %q, restamped %v", body, summary, hash == statusHash())
	}
	if n := count(t, db, `SELECT count(*) FROM statements WHERE item_id = 'tomoe:s-1' AND holder = 'Ana Example' AND text LIKE 'Ana Example %'`); n == 0 {
		t.Error("no statement held by the new name")
	}
	if n := count(t, db, `SELECT count(*) FROM meeting_todos WHERE item_id = 'tomoe:s-1' AND owners = '["Ana Example"]' AND requester = 'Bo Example'`); n != 1 {
		t.Errorf("%d to-dos owned by the new name", n)
	}
	// Scope: the other meeting keeps its label.
	if n := count(t, db, `SELECT count(*) FROM statements WHERE item_id = 'tomoe:s-2' AND holder = 'Corner Tile'`); n == 0 {
		t.Error("the other meeting was relabelled")
	}
	if a, b := count(t, db, `SELECT count(*) FROM statements`), count(t, db, `SELECT count(*) FROM thread_items`); a != nStatements || b != nThreadItems {
		t.Errorf("statements %d → %d, thread items %d → %d", nStatements, a, nThreadItems, b)
	}

	// The next import reads the rewritten file: no item changed, and no
	// passage stage runs (the registry and what reads it follow the new
	// speaker, as on any import of a new name).
	var s1 Stats
	if err := r.Ingest(ctx, &s1); err != nil {
		t.Fatal(err)
	}
	if s1.Changed != 0 {
		t.Errorf("import after the relabel: %d items changed", s1.Changed)
	}
	if err := r.Process(ctx, &s1); err != nil {
		t.Fatal(err)
	}
	if s1.Ran["chunk"] != 0 || s1.Ran["embed"] != 0 {
		t.Errorf("process after the relabel ran %v", s1.Ran)
	}
	if ks := know(true); ks.Planned.Extract != 0 || ks.Planned.Digest != 0 {
		t.Errorf("plan after the relabel: %+v", ks.Planned)
	}
	calls = llm.calls.Load()
	if ks := know(false); ks.Items != 0 || ks.Digested != 0 || llm.calls.Load() != calls {
		t.Errorf("knowledge after the relabel: %d extracted, %d digested, %d calls", ks.Items, ks.Digested, llm.calls.Load()-calls)
	}
	if ex, _ := st.StageStamp("extract", "tomoe:s-1"); ex == "" {
		t.Error("extract stamp lost")
	}
	var s2 Stats
	run2 := func() Stats {
		var s Stats
		if err := r.Ingest(ctx, &s); err != nil {
			t.Fatal(err)
		}
		if err := r.Process(ctx, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s2 = run2(); s2.Changed != 0 || len(s2.Ran) != 0 {
		t.Errorf("second update after the relabel: %d changed, ran %v", s2.Changed, s2.Ran)
	}
	if a := count(t, db, `SELECT count(*) FROM statements`); a != nStatements {
		t.Errorf("statements %d → %d after the update", nStatements, a)
	}

	// Undo, then the recorder's undo of the file: back as it was.
	if _, err := r.UndoRelabel(ctx, "b1", "tomoe:s-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "s-1", "session.json"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM statements WHERE item_id = 'tomoe:s-1' AND holder = 'Corner Tile' AND text LIKE 'Corner Tile %'`); n == 0 {
		t.Error("undo didn't restore the holders")
	}
	rw.QueryRow(`SELECT body, input_hash FROM thread_status WHERE topic_id = 900`).Scan(&body, &hash)
	if !strings.Contains(body, "Corner Tile") || hash != statusHash() {
		t.Errorf("status after the undo: %q, restamped %v", body, hash == statusHash())
	}
	if s := run2(); s.Changed != 0 || s.Ran["chunk"] != 0 {
		t.Errorf("update after the undo: %d changed, ran %v", s.Changed, s.Ran)
	}
	calls = llm.calls.Load()
	if ks := know(false); ks.Items != 0 || ks.Digested != 0 || llm.calls.Load() != calls {
		t.Errorf("knowledge after the undo: %d extracted, %d digested", ks.Items, ks.Digested)
	}
	if _, err := r.UndoRelabel(ctx, "b1", "tomoe:s-1"); err == nil {
		t.Error("undoing twice gave no error")
	}

	// A change beyond the labels is refused, and writes nothing.
	changed := strings.Replace(string(orig), "the beta needs another month", "the beta is ready", 1)
	changed = strings.ReplaceAll(changed, "Corner Tile", "Ana Example")
	if _, err := r.RelabelSpeaker(ctx, RelabelRequest{ItemID: "tomoe:s-1", Session: []byte(changed)}); err == nil {
		t.Error("a changed line was relabelled")
	}
	// By name, without the file.
	if rep, err := r.RelabelSpeaker(ctx, RelabelRequest{ItemID: "tomoe:s-2", Old: "Corner Tile", New: "Ana Example"}); err != nil || rep.Lines != 1 {
		t.Errorf("by name: %+v, %v", rep, err)
	}
}

type renamedLLM struct {
	*relabelLLM
	name string
}

func (l renamedLLM) ModelName() string { return l.name }

// A meeting extracted under an earlier version (kept until asked) stays
// kept after a relabel: its input is the same passages, relabelled.
func TestRelabelKeepsOlderExtraction(t *testing.T) {
	ctx := context.Background()
	sessions := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Owner = config.Owner{Name: "Jordan Pike", Email: "jordan.pike@northwind.example"}
	cfg.Knowledge.Gate, cfg.Knowledge.MinWorth = 0, 0
	// The fake model answers the statements prompt.
	cfg.Knowledge.Extract, cfg.Knowledge.Grouping = "statements", "topics"
	cfg.Knowledge.Label.Enabled, cfg.Knowledge.ProposeAliases, cfg.Knowledge.NameMeetings = false, false, false
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rs, _ := raw.Open(cfg.RawDir())
	r := &Runner{Cfg: cfg, Store: st, Raw: rs, Embedders: []provider.Embedder{provider.Hash{Dim: 64}},
		CuratedPath: filepath.Join(t.TempDir(), "none.toml"), Connectors: []source.Connector{tomoe.Connector{Dir: sessions}}}
	writeRelabelSession(t, sessions, "s-1", "Person 3 (Corner Tile)", "Person 4 (Bo Example)")
	run(t, r)
	base := &relabelLLM{}
	knowledgeRun(t, r, KnowledgeOptions{LLM: renamedLLM{base, "model-a"}})
	newer := renamedLLM{base, "model-b"}
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: newer, Plan: true}); ks.Planned.Extract != 0 || ks.Outdated.Extract != 1 {
		t.Fatalf("fixture: planned %d, outdated %d", ks.Planned.Extract, ks.Outdated.Extract)
	}
	b := writeRelabelSession(t, sessions, "s-1", "Person 3 (Ana Example)", "Person 4 (Bo Example)")
	if _, err := r.RelabelSpeaker(ctx, RelabelRequest{ItemID: "tomoe:s-1", Session: b}); err != nil {
		t.Fatal(err)
	}
	run(t, r)
	for _, plan := range []bool{true, false} {
		if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: newer, Plan: plan}); ks.Planned.Extract != 0 || ks.Items != 0 || ks.Outdated.Extract != 1 {
			t.Errorf("after the relabel (plan %v): planned %d, extracted %d, outdated %d", plan, ks.Planned.Extract, ks.Items, ks.Outdated.Extract)
		}
	}
}
