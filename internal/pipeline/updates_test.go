package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// emptyLLM extracts nothing: enough to stamp items as extracted.
type emptyLLM struct{ name string }

func (e emptyLLM) Local() bool       { return true }
func (e emptyLLM) ModelName() string { return e.name }
func (e emptyLLM) Complete(context.Context, []provider.Message, provider.ChatOptions, func(string)) (string, error) {
	return `{"statements":[]}`, nil
}

func knowledgeRun(t *testing.T, r *Runner, o KnowledgeOptions) KnowledgeStats {
	t.Helper()
	o.Embedder = provider.Hash{Dim: 64}
	ks, err := r.Knowledge(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

// A new extraction version (here, another model) keeps what was extracted
// until asked; a changed input is re-extracted anyway; "auto" redoes it all.
func TestExtractionUpdatesAreExplicit(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.Updates = "statements", 0, "explicit"
	all := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m1"}}).Items
	if all < 3 {
		t.Fatalf("extracted %d items", all)
	}
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m1"}}); ks.Items != 0 || ks.Outdated.Extract != 0 {
		t.Fatalf("second run: %d extracted, %d outdated", ks.Items, ks.Outdated.Extract)
	}

	// Plan: says what it would do, does nothing.
	ks := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m2"}, Plan: true, Redo: []string{"extract"}})
	if ks.Planned.Extract != all || ks.Items != 0 {
		t.Fatalf("plan: %d planned, %d extracted", ks.Planned.Extract, ks.Items)
	}

	// New version: nothing redone, everything reported.
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m2"}}); ks.Items != 0 || ks.Outdated.Extract != all {
		t.Fatalf("new version: %d extracted, %d outdated of %d", ks.Items, ks.Outdated.Extract, all)
	}

	// One item's input changed: that one is extracted.
	items, _ := st.AllItems()
	var changed string
	for _, it := range items {
		if s, _ := st.StageStamp("extract-input", it.ID); s != "" {
			changed = it.ID
			break
		}
	}
	if err := st.ReplaceStatementsFrom(changed, "x8", "stale", "different input", nil); err != nil {
		t.Fatal(err)
	}
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m2"}}); ks.Items != 1 || ks.Outdated.Extract != all-1 {
		t.Fatalf("changed input: %d extracted, %d outdated", ks.Items, ks.Outdated.Extract)
	}

	// Asked for: the newest two, then the rest.
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m2"}, Redo: []string{"extract"}, RedoNewest: 2}); ks.Items != 2 || ks.Outdated.Extract != all-3 {
		t.Fatalf("redo newest 2: %d extracted, %d outdated", ks.Items, ks.Outdated.Extract)
	}
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m2"}, Redo: []string{"extract"}}); ks.Items != all-3 || ks.Outdated.Extract != 0 {
		t.Fatalf("redo rest: %d extracted, %d outdated", ks.Items, ks.Outdated.Extract)
	}

	// "auto": a new version redoes everything, as before.
	r.Cfg.Knowledge.Updates = "auto"
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m3"}}); ks.Items != all || ks.Outdated.Extract != 0 {
		t.Fatalf("auto: %d extracted of %d", ks.Items, all)
	}
}

// Claims and threads under earlier rules are kept until asked; asking for
// links rethreads too.
func TestLinkAndThreadUpdatesAreExplicit(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Updates = "explicit"
	o := KnowledgeOptions{LLM: emptyLLM{"m1"}, Embedder: provider.Hash{Dim: 64}}
	cur := r.linkStamp(o)
	if s, rebuild, older := r.linkDecision(o); s != cur || rebuild || older {
		t.Fatalf("fresh: %v %v %v", s == cur, rebuild, older)
	}
	st.SetGlobalStamp("claims", "earlier")
	if s, rebuild, older := r.linkDecision(o); s != "earlier" || rebuild || !older {
		t.Fatalf("explicit, earlier rules: kept=%v rebuild=%v older=%v", s == "earlier", rebuild, older)
	}
	if _, rebuild, older := r.linkDecision(KnowledgeOptions{LLM: o.LLM, Redo: []string{"links"}}); !rebuild || older {
		t.Fatal("--redo links doesn't rebuild")
	}

	items, _ := st.AllItems()
	llm := emptyLLM{"t1"}
	p, err := r.planThreads(o, llm, cur, false)
	if err != nil {
		t.Fatal(err)
	}
	st.SetGlobalStamp("threads", p.stamp)
	st.MarkThreaded(items[0].ID, "earlier")
	st.MarkThreaded(items[1].ID, p.stamp)
	if p, _ = r.planThreads(o, llm, cur, false); p.rebuild || p.older != 1 || len(p.todo) != len(items)-2 {
		t.Fatalf("explicit: rebuild=%v older=%d todo=%d of %d", p.rebuild, p.older, len(p.todo), len(items))
	}
	for _, redo := range []string{"threads", "links"} {
		if p, _ = r.planThreads(KnowledgeOptions{LLM: o.LLM, Redo: []string{redo}}, llm, cur, false); !p.rebuild || len(p.todo) != len(items) {
			t.Fatalf("--redo %s: rebuild=%v todo=%d", redo, p.rebuild, len(p.todo))
		}
	}
	r.Cfg.Knowledge.Updates = "auto"
	st.SetGlobalStamp("threads", "earlier")
	if p, _ = r.planThreads(o, llm, cur, false); !p.rebuild || len(p.todo) != len(items) {
		t.Fatalf("auto, rules changed: rebuild=%v todo=%d", p.rebuild, len(p.todo))
	}
}

// labelLLM extracts two statements per passage and, as the labeller, calls
// the one about a person's exit sensitive.
type labelLLM struct{ emptyLLM }

func (labelLLM) Complete(context.Context, []provider.Message, provider.ChatOptions, func(string)) (string, error) {
	return `{"statements":[{"kind":"fact","text":"Dana Ruiz is leaving the company in December after a performance review.","holder":"","certainty":0.9,"quote":""},
		{"kind":"fact","text":"The pricing page launches next week.","holder":"","certainty":0.9,"quote":""}]}`, nil
}

func (labelLLM) Decide(_ context.Context, msgs []provider.Message, choices []string) (map[string]float64, error) {
	p := map[string]float64{}
	for _, c := range choices {
		p[c] = 0
	}
	if strings.Contains(msgs[len(msgs)-1].Content, "leaving") {
		p["people"] = 0.9
		p["none"] = 0.1
	} else {
		p["none"] = 1
	}
	return p, nil
}

func TestStatementLabels(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.MinWorth = "statements", 0, 0
	ks := knowledgeRun(t, r, KnowledgeOptions{LLM: labelLLM{emptyLLM{"m1"}}})
	if ks.Checked == 0 || ks.Labelled == 0 || ks.Labelled*2 > ks.Checked {
		t.Fatalf("checked %d, labelled %d", ks.Checked, ks.Labelled)
	}
	sts, _ := st.AllStatements()
	for _, s := range sts {
		hr := s.Own.Categories&sensitivity.HR != 0
		if hr != strings.Contains(s.Text, "leaving") {
			t.Errorf("%q: HR=%v", s.Text, hr)
		}
		if hr && s.Own.Level < sensitivity.Confidential {
			t.Errorf("%q labelled HR at %v", s.Text, s.Own.Level)
		}
	}
	// Checked once per version.
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: labelLLM{emptyLLM{"m1"}}}); ks.Checked != 0 {
		t.Errorf("rechecked %d statements", ks.Checked)
	}
	// A classifier labels only what no labeller has checked; --redo label
	// relabels everything with it.
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"answers":{"sensitive":{"choice":"work","probabilities":{"work":1}}},"usage":{"input_tokens":10}}`)
	}))
	defer srv.Close()
	t.Setenv("JEV_TEST_KEY", "k")
	r.Cfg.Knowledge.AllowHosted = true
	r.Cfg.Knowledge.Label.Classifier = config.Classifier{Provider: "jev", URL: srv.URL, APIKey: "env:JEV_TEST_KEY"}
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: labelLLM{emptyLLM{"m1"}}}); ks.Checked != 0 || calls.Load() != 0 {
		t.Errorf("relabelled %d statements (%d calls) without being asked", ks.Checked, calls.Load())
	}
	if ks := knowledgeRun(t, r, KnowledgeOptions{LLM: labelLLM{emptyLLM{"m1"}}, Redo: []string{"label"}}); ks.Checked != len(sts) || calls.Load() != int64(len(sts)) {
		t.Errorf("--redo label checked %d of %d (%d calls)", ks.Checked, len(sts), calls.Load())
	}
	// Off restores the earlier behaviour.
	r.Cfg.Knowledge.Label.Enabled = false
	_ = st
}

// statusLLM writes a status that says whether it was shown the sensitive
// item, citing every id it was given.
type statusLLM struct {
	emptyLLM
	calls *atomic.Int64
}

func (s statusLLM) Complete(_ context.Context, msgs []provider.Message, _ provider.ChatOptions, _ func(string)) (string, error) {
	s.calls.Add(1)
	user := msgs[len(msgs)-1].Content
	now := "Launch next week."
	if strings.Contains(user, "leaving") {
		now = "Launch next week; someone is leaving."
	}
	var ids []string
	for _, l := range strings.Split(user, "\n") {
		if strings.HasPrefix(l, "[") {
			ids = append(ids, l[1:strings.Index(l, "]")])
		}
	}
	return `{"now":"` + now + `","now_ids":[` + strings.Join(ids, ",") + `,99999],"changed":[],"open":[],"people":[]}`, nil
}

func TestThreadStatuses(t *testing.T) {
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
	if err := r.writeStatuses(context.Background(), o, &ks); err != nil {
		t.Fatal(err)
	}
	// One from every item, one from what a shared screen may show.
	if ks.Statuses != 2 || calls.Load() != 2 {
		t.Fatalf("wrote %d statuses in %d calls", ks.Statuses, calls.Load())
	}
	read := func(sc sensitivity.Scope) knowledge.Status {
		got, err := st.ThreadStatuses(sc)
		if err != nil {
			t.Fatal(err)
		}
		var s knowledge.Status
		json.Unmarshal([]byte(got[tid].Body), &s)
		return s
	}
	if s := read(sensitivity.Owner()); !strings.Contains(s.Now, "leaving") || len(s.NowIDs) != len(ids) {
		t.Errorf("owner status = %+v (ids it wasn't shown must be dropped)", s)
	}
	if s := read(sensitivity.SharedScreen()); s.Now == "" || strings.Contains(s.Now, "leaving") {
		t.Errorf("shared-screen status = %+v", s)
	}
	// Unchanged items: nothing written again.
	ks = KnowledgeStats{}
	r.writeStatuses(context.Background(), o, &ks)
	if ks.Statuses != 0 {
		t.Errorf("rewrote %d unchanged statuses", ks.Statuses)
	}
	// A label raised later (a new rule, the labeller) reaches the stored
	// status without a rewrite.
	st.LabelStatement(ids[0], sensitivity.Label{Level: sensitivity.Confidential, Categories: sensitivity.Legal}, "test")
	calls.Store(0)
	r.writeStatuses(context.Background(), o, &ks)
	if got, _ := st.ThreadStatuses(sensitivity.Owner()); got[tid].Categories&sensitivity.Legal == 0 {
		t.Errorf("status label not raised: %+v", got[tid])
	}
	// Retiring an item every scope sees changes both inputs; retiring the
	// sensitive one changes only the full status.
	var shared, private []int64
	for _, x := range sts {
		if strings.Contains(x.Text, "leaving") {
			private = append(private, x.ID)
		} else {
			shared = append(shared, x.ID)
		}
	}
	st.SetThreadCurrent(tid, private)
	r.writeStatuses(context.Background(), o, &ks)
	if ks.Statuses != 2 {
		t.Errorf("after a retirement wrote %d statuses", ks.Statuses)
	}
	st.SetThreadCurrent(tid, ids)
	r.writeStatuses(context.Background(), o, &ks)
	ks = KnowledgeStats{}
	st.SetThreadCurrent(tid, shared)
	if r.writeStatuses(context.Background(), o, &ks); ks.Statuses != 1 {
		t.Errorf("after retiring the sensitive item wrote %d statuses, want 1", ks.Statuses)
	}
	// Off writes none.
	r.Cfg.Knowledge.Status.Enabled = false
	ks = KnowledgeStats{}
	st.SetThreadCurrent(tid, ids)
	if r.writeStatuses(context.Background(), o, &ks); ks.Statuses != 0 {
		t.Errorf("disabled, wrote %d", ks.Statuses)
	}
}

// Items older than threaded ones are counted as backfilled.
func TestLatestThreaded(t *testing.T) {
	_, st := build(t)
	if at, err := st.LatestThreaded(); err != nil || !at.IsZero() {
		t.Fatalf("empty store: %v %v", at, err)
	}
	items, _ := st.AllItems()
	if len(items) == 0 {
		t.Skip("fixture has no items")
	}
	st.MarkThreaded(items[len(items)-1].ID, "s")
	if at, _ := st.LatestThreaded(); !at.Equal(items[len(items)-1].OccurredAt.Truncate(time.Second)) {
		t.Errorf("latest %v, want %v", at, items[len(items)-1].OccurredAt)
	}
}

// Threading only appends in time order: an item older than what's threaded
// waits for a rethread, and an automatic update takes only a few new ones.
func TestHeldWork(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate = "statements", 0
	r.Cfg.Knowledge.Threads.Rewind = 0
	o := KnowledgeOptions{LLM: emptyLLM{"m1"}, Embedder: provider.Hash{Dim: 64}}
	items, _ := st.AllItems()
	if len(items) < 3 {
		t.Fatalf("%d items", len(items))
	}
	newest := items[0]
	for _, it := range items {
		if it.OccurredAt.After(newest.OccurredAt) {
			newest = it
		}
	}
	cur := r.linkStamp(o)
	llm := emptyLLM{"t1"}
	p, _ := r.planThreads(o, llm, cur, false)
	st.SetGlobalStamp("threads", p.stamp)
	st.MarkThreaded(newest.ID, p.stamp)
	older := 0
	for _, it := range items {
		if it.OccurredAt.Before(newest.OccurredAt) {
			older++
		}
	}
	if p, _ = r.planThreads(o, llm, cur, false); p.held != older || len(p.todo) != len(items)-1-older {
		t.Fatalf("held %d of %d older items; todo %d", p.held, older, len(p.todo))
	}
	if p, _ = r.planThreads(KnowledgeOptions{LLM: o.LLM, Redo: []string{"threads"}}, llm, cur, false); p.held != 0 || len(p.todo) != len(items) {
		t.Fatalf("a rethread holds nothing: held %d, todo %d", p.held, len(p.todo))
	}
	r.Cfg.Knowledge.Threads.HoldOlder = false
	if p, _ = r.planThreads(o, llm, cur, false); p.held != 0 || len(p.todo) != len(items)-1 {
		t.Fatalf("hold_older off threads them now, as before: held %d, todo %d", p.held, len(p.todo))
	}

	// An automatic update on a fresh dataset takes auto_max items.
	r2, _ := build(t)
	r2.Cfg.Knowledge.Extract, r2.Cfg.Knowledge.Gate, r2.Cfg.Knowledge.AutoMax = "statements", 0, 1
	ks := knowledgeRun(t, r2, KnowledgeOptions{LLM: emptyLLM{"m1"}, Auto: true})
	if ks.Items != 1 || ks.Held.Batch < 2 {
		t.Fatalf("auto: extracted %d, held %d for a run by hand", ks.Items, ks.Held.Batch)
	}
	// A run by hand takes the rest.
	if ks := knowledgeRun(t, r2, KnowledgeOptions{LLM: emptyLLM{"m1"}}); ks.Items < 2 || ks.Held.Batch != 0 {
		t.Fatalf("by hand: extracted %d, held %d", ks.Items, ks.Held.Batch)
	}
}

// A meeting threaded before and re-extracted (a speaker renamed in Tomoe)
// goes back into the threads in its place, even when newer ones exist.
func TestReextractedRejoins(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Threads.Rewind = 0
	o := KnowledgeOptions{LLM: emptyLLM{"m1"}, Embedder: provider.Hash{Dim: 64}}
	items, _ := st.AllItems()
	oldest, newest := items[0], items[0]
	for _, it := range items {
		if it.OccurredAt.Before(oldest.OccurredAt) {
			oldest = it
		}
		if it.OccurredAt.After(newest.OccurredAt) {
			newest = it
		}
	}
	cur := r.linkStamp(o)
	llm := emptyLLM{"t1"}
	p, _ := r.planThreads(o, llm, cur, false)
	st.SetGlobalStamp("threads", p.stamp)
	for _, it := range items {
		st.MarkThreaded(it.ID, p.stamp)
	}
	if err := st.ReplaceStatementsFrom(oldest.ID, "x", "h", "renamed", nil); err != nil {
		t.Fatal(err)
	}
	p, _ = r.planThreads(o, llm, cur, false)
	if p.held != 0 || len(p.todo) != 1 || p.todo[0].ID != oldest.ID {
		t.Fatalf("re-extracted oldest: held %d, todo %+v", p.held, p.todo)
	}
}
