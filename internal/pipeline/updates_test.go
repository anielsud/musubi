package pipeline

import (
	"context"
	"testing"

	"github.com/anielsud/musubi/internal/provider"
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
