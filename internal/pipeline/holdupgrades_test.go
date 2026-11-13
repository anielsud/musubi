package pipeline

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/stages"
	"github.com/anielsud/musubi/internal/store"
)

// countLLM extracts labelLLM's statements and counts the calls: each is
// money on a hosted model.
type countLLM struct {
	labelLLM
	calls *atomic.Int64
}

func (c countLLM) Complete(ctx context.Context, m []provider.Message, o provider.ChatOptions, f func(string)) (string, error) {
	c.calls.Add(1)
	return c.labelLLM.Complete(ctx, m, o, f)
}

// newChunker stands in a chunker whose passages differ from the current
// one's, as a chunker change would, until the test ends.
func newChunker(t *testing.T) {
	t.Helper()
	v, f := chunkVersion, chunkPassages
	chunkVersion = "c-next"
	chunkPassages = func(it item.Item, title string) []store.Chunk {
		cs := stages.Chunk(it, title)
		for i := range cs {
			cs[i].Text += "\n(cut again)"
		}
		return cs
	}
	t.Cleanup(func() { chunkVersion, chunkPassages = v, f })
}

func chunkStamps(t *testing.T, st *store.Store, prefix string) (n int) {
	t.Helper()
	items, _ := st.AllItems()
	for _, it := range items {
		if s, _ := st.StageStamp("chunk", it.ID); strings.HasPrefix(s, prefix+"/") {
			n++
		}
	}
	return n
}

// A new chunker (or translator) that re-cuts unchanged meetings doesn't
// re-extract them: they keep their passages and statements until the owner
// applies the upgrade. A meeting whose content changed is cut and
// extracted as before; hold_upgrades = false restores the old behaviour.
func TestNewChunkerWaitsForOwner(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.MinWorth = "statements", 0, 0
	r.Cfg.Knowledge.Label.Enabled, r.Cfg.Knowledge.NameMeetings = false, false
	var calls atomic.Int64
	o := KnowledgeOptions{LLM: countLLM{labelLLM{emptyLLM{"m1"}}, &calls}}
	extracted := knowledgeRun(t, r, o).Items
	statements, _ := st.AllStatements()
	if extracted < 3 || len(statements) == 0 {
		t.Fatalf("fixture: %d items, %d statements", extracted, len(statements))
	}

	newChunker(t)
	run(t, r)
	if got := r.waiting("passages"); got != extracted {
		t.Fatalf("%d meetings wait, want %d", got, extracted)
	}
	// Only items no model has read are cut again.
	if n := chunkStamps(t, st, stages.ChunkVersion); n != extracted {
		t.Fatalf("%d items keep their passages, want %d", n, extracted)
	}
	calls.Store(0)
	ks := knowledgeRun(t, r, o)
	if calls.Load() != 0 || ks.Items != 0 || ks.Outdated.Passages != extracted {
		t.Fatalf("after the upgrade: %d calls, %d extracted, %d waiting", calls.Load(), ks.Items, ks.Outdated.Passages)
	}
	if after, _ := st.AllStatements(); len(after) != len(statements) {
		t.Fatalf("statements %d → %d while waiting", len(statements), len(after))
	}
	// The work queue shows them, with a cost, and what applying them runs.
	q, err := r.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if u := q.Upgrades; u.Passages != extracted || u.USD <= 0 || strings.Join(u.Redo(), ",") != "extract" {
		t.Fatalf("queue upgrades %+v (redo %v)", u, u.Redo())
	}

	// The plan says what --redo extract would take.
	plan := knowledgeRun(t, r, KnowledgeOptions{LLM: o.LLM, Plan: true, Redo: []string{"extract"}, RedoNewest: 2})
	if plan.Outdated.Passages != extracted || plan.Planned.Extract != 2 || calls.Load() != 0 {
		t.Fatalf("plan: %d waiting, %d planned, %d calls", plan.Outdated.Passages, plan.Planned.Extract, calls.Load())
	}

	// A meeting whose content changed is cut and extracted as before.
	items := mustItems(t, st)
	var changed string
	for _, it := range items {
		if s, _ := st.StageStamp("extract", it.ID); s != "" {
			changed = it.ID
			break
		}
	}
	if err := st.SetChunkSource(changed, "raw-was:content-was"); err != nil {
		t.Fatal(err)
	}
	run(t, r)
	if s, _ := st.StageStamp("chunk", changed); !strings.HasPrefix(s, "c-next/") {
		t.Fatalf("changed meeting not cut again: %s", s)
	}
	if ks := knowledgeRun(t, r, o); ks.Items != 1 || r.waiting("passages") != extracted-1 {
		t.Fatalf("changed meeting: %d extracted, %d waiting", ks.Items, r.waiting("passages"))
	}

	// Applying the upgrade, newest first.
	calls.Store(0)
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: o.LLM, Redo: []string{"extract"}, RedoNewest: 1}); ks.Items != 1 || ks.Outdated.Passages != extracted-2 {
		t.Fatalf("--redo extract --newest 1: %d extracted, %d still waiting", ks.Items, ks.Outdated.Passages)
	}
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: o.LLM, Redo: []string{"extract"}}); ks.Items != extracted-2 || ks.Outdated.Passages != 0 {
		t.Fatalf("--redo extract: %d extracted, %d still waiting", ks.Items, ks.Outdated.Passages)
	}
	if n := chunkStamps(t, st, stages.ChunkVersion); n != 0 {
		t.Errorf("%d items still cut by the old chunker", n)
	}
	if s := run(t, r); len(s.Ran) != 0 {
		t.Errorf("after applying, an update ran %v", s.Ran)
	}
}

func TestHoldUpgradesOff(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.MinWorth = "statements", 0, 0
	r.Cfg.Knowledge.Label.Enabled, r.Cfg.Knowledge.NameMeetings = false, false
	r.Cfg.Knowledge.HoldUpgrades = false
	var calls atomic.Int64
	o := KnowledgeOptions{LLM: countLLM{labelLLM{emptyLLM{"m1"}}, &calls}}
	extracted := knowledgeRun(t, r, o).Items
	newChunker(t)
	run(t, r)
	if ks := knowledgeRun(t, r, o); ks.Items != extracted || r.waiting("passages") != 0 {
		t.Fatalf("hold_upgrades = false: %d of %d extracted again, %d waiting", ks.Items, extracted, r.waiting("passages"))
	}
	_ = st
}

// Items cut before chunk-source keys existed: the chunk hash recomputed
// under the version in their stamp tells an upgrade from a change.
func TestNewChunkerWaitsWithoutSourceKeys(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.MinWorth = "statements", 0, 0
	r.Cfg.Knowledge.Label.Enabled, r.Cfg.Knowledge.NameMeetings = false, false
	var calls atomic.Int64
	o := KnowledgeOptions{LLM: countLLM{labelLLM{emptyLLM{"m1"}}, &calls}}
	extracted := knowledgeRun(t, r, o).Items
	rw, err := sql.Open("sqlite3", "file:"+r.Cfg.DBPath()+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`DELETE FROM stage_runs WHERE stage = 'chunk-source'`); err != nil {
		t.Fatal(err)
	}
	newChunker(t)
	run(t, r)
	calls.Store(0)
	if ks := knowledgeRun(t, r, o); calls.Load() != 0 || r.waiting("passages") != extracted {
		t.Fatalf("legacy stamps: %d calls, %d extracted, %d waiting of %d", calls.Load(), ks.Items, r.waiting("passages"), extracted)
	}
	_ = st
}

// A new status version doesn't rewrite statuses whose items are unchanged
// until asked; a thread whose items changed is rewritten as before.
func TestNewStatusVersionWaitsForOwner(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.MinWorth = "statements", 0, 0
	knowledgeRun(t, r, KnowledgeOptions{LLM: labelLLM{emptyLLM{"m1"}}})
	sts, _ := st.AllStatements()
	tid, _ := st.NewThread("Pricing page", "the pricing page launch", 0)
	var ids []int64
	for _, s := range sts {
		ids = append(ids, s.ID)
	}
	st.AddToThread(tid, ids, 0)
	var calls atomic.Int64
	o := KnowledgeOptions{StatusLLM: statusLLM{emptyLLM{"m1"}, &calls}}
	var ks KnowledgeStats
	if err := r.writeStatuses(context.Background(), o, &ks); err != nil || ks.Statuses != 2 {
		t.Fatalf("wrote %d: %v", ks.Statuses, err)
	}
	// Restamp them as an earlier status version wrote them.
	earlier := func() {
		th, _ := st.ThreadStatements(tid)
		fix, _ := st.Corrector()
		for i := range th {
			th[i].Text = fix.Text(th[i].ID, th[i].Text)
		}
		full, op := statusInputs("Pricing page", "the pricing page launch", th)
		rows, _ := st.ThreadStatusRows(tid)
		for v, row := range rows {
			in := full
			if v == "open" {
				in = op
			}
			row.Version, row.InputHash = "ts-earlier", knowledge.StatusHash("ts-earlier", "Pricing page", "the pricing page launch", in.Items)
			if err := st.SetThreadStatus(row); err != nil {
				t.Fatal(err)
			}
		}
	}
	earlier()
	calls.Store(0)
	ks = KnowledgeStats{}
	if err := r.writeStatuses(context.Background(), o, &ks); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || ks.Outdated.Statuses != 2 || r.waiting("statuses") != 2 {
		t.Fatalf("new status version: %d calls, %d waiting", calls.Load(), ks.Outdated.Statuses)
	}
	// The plan counts them without writing.
	if n, _, err := r.statusJobs(KnowledgeOptions{}, mustThreads(t, st), true); err != nil || len(n) != 0 {
		t.Fatalf("plan jobs %d: %v", len(n), err)
	}
	// Changed items: rewritten.
	var keep []int64
	for _, s := range sts {
		if !strings.Contains(s.Text, "leaving") {
			keep = append(keep, s.ID)
		}
	}
	st.SetThreadCurrent(tid, keep)
	ks = KnowledgeStats{}
	r.writeStatuses(context.Background(), o, &ks)
	if ks.Statuses == 0 {
		t.Fatal("changed thread not rewritten")
	}
	// Asked for: the rest are rewritten.
	st.SetThreadCurrent(tid, ids)
	r.writeStatuses(context.Background(), o, &ks)
	earlier()
	calls.Store(0)
	ks = KnowledgeStats{}
	o.Redo = []string{"statuses"}
	if r.writeStatuses(context.Background(), o, &ks); ks.Statuses != 2 || r.waiting("statuses") != 0 {
		t.Fatalf("--redo statuses wrote %d, %d waiting", ks.Statuses, r.waiting("statuses"))
	}
}

// A new naming version or model doesn't rename meetings named from the
// same text until asked.
func TestNewNamingModelWaitsForOwner(t *testing.T) {
	r, _ := build(t)
	llm := &nameLLM{}
	n, err := r.NameMeetings(context.Background(), KnowledgeOptions{DigestLLM: llm})
	if err != nil || n == 0 {
		t.Fatalf("named %d: %v", n, err)
	}
	other := &namedLLM{nameLLM{}, "stub-2"}
	if n, _ := r.NameMeetings(context.Background(), KnowledgeOptions{DigestLLM: other}); n != 0 || other.calls != 0 {
		t.Fatalf("renamed %d under a new model", n)
	}
	if r.waiting("names") == 0 {
		t.Fatal("waiting names not counted")
	}
	if n2, _ := r.NameMeetings(context.Background(), KnowledgeOptions{DigestLLM: other, Redo: []string{"names"}}); n2 != n {
		t.Fatalf("--redo names renamed %d of %d", n2, n)
	}
	if r.waiting("names") != 0 {
		t.Fatalf("%d names still waiting", r.waiting("names"))
	}
}

type namedLLM struct {
	nameLLM
	name string
}

func (n *namedLLM) ModelName() string { return n.name }

func mustItems(t *testing.T, st *store.Store) []item.Item {
	t.Helper()
	items, err := st.AllItems()
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func mustThreads(t *testing.T, st *store.Store) []store.ThreadRow {
	t.Helper()
	th, err := st.Threads()
	if err != nil {
		t.Fatal(err)
	}
	return th
}
