package pipeline

import (
	"context"
	"fmt"

	"github.com/anielsud/musubi/internal/knowledge"
)

// InsertCheck measures Insert against a replay (docs/incremental-design.md,
// "equivalence"): on a database threaded in time order, it takes one item
// out of the graph (its memberships and the relations it took part in),
// derives "current" again for the threads it was in, inserts the item, and
// compares with what the replay had. Run it on a copy: it changes the
// database.
type InsertResult struct {
	Item                     string
	Statements, SameThread   int // statements, and those placed in a thread they were in before
	Placed, PlacedBefore     int
	ThreadItems, AgreeBefore int // memberships in the affected threads; current flags as the replay had them
	RelationsBefore, After   int
}

func (r *Runner) InsertCheck(ctx context.Context, o KnowledgeOptions, itemID string) (InsertResult, error) {
	res := InsertResult{Item: itemID}
	kc := r.Cfg.Knowledge
	llm, err := r.threadLLM(o)
	if err != nil {
		return res, err
	}
	sts, err := r.Store.LinkedItemStatements(itemID)
	if err != nil || len(sts) == 0 {
		return res, fmt.Errorf("item %s has no statements: %v", itemID, err)
	}
	mine := map[int64]bool{}
	for _, s := range sts {
		mine[s.ID] = true
	}
	res.Statements = len(sts)
	rows, err := r.Store.ThreadItemRows()
	if err != nil {
		return res, err
	}
	before := map[int64]map[int64]bool{} // statement → its threads, before
	affected := map[int64]bool{}
	for _, x := range rows {
		if mine[x.Statement] {
			if before[x.Statement] == nil {
				before[x.Statement] = map[int64]bool{}
			}
			before[x.Statement][x.Topic] = true
			affected[x.Topic] = true
		}
	}
	res.PlacedBefore = len(before)
	type key struct{ t, s int64 }
	was := map[key]bool{}
	for _, x := range rows {
		if affected[x.Topic] {
			was[key{x.Topic, x.Statement}] = x.Current
		}
	}
	n, err := r.Store.RemoveFromThreads(sts)
	if err != nil {
		return res, err
	}
	res.RelationsBefore = n
	it := knowledge.ThreadItem{ID: itemID}
	items, _ := r.Store.AllItems()
	for _, x := range items {
		if x.ID == itemID {
			it.Start = x.OccurredAt
		}
	}
	vecs, _ := r.Store.StatementVecs(o.Embedder.Model())
	th := &knowledge.Threader{Store: r.Store, LLM: llm, Vecs: vecs, Version: "insert-check", Chunk: kc.Threads.Chunk, Parallel: kc.Threads.Parallel,
		Candidates: kc.Threads.Candidates, StateCap: kc.Threads.StateCap, AuditFixes: kc.Threads.AuditFixes, ConsolidateWindow: kc.Threads.ConsolidateWindow, Logf: r.logf}
	if err := th.Rederive(it, affected); err != nil {
		return res, err
	}
	st, err := th.Insert(ctx, it)
	if err != nil {
		return res, err
	}
	res.After = st.Relations
	rows, err = r.Store.ThreadItemRows()
	if err != nil {
		return res, err
	}
	placed, same := map[int64]bool{}, map[int64]bool{}
	for _, x := range rows {
		if mine[x.Statement] {
			placed[x.Statement] = true
			if before[x.Statement][x.Topic] {
				same[x.Statement] = true
			}
		}
		if c, ok := was[key{x.Topic, x.Statement}]; ok {
			res.ThreadItems++
			if c == x.Current {
				res.AgreeBefore++
			}
		}
	}
	res.Placed, res.SameThread = len(placed), len(same)
	return res, nil
}

// CorrectStatement applies the owner's correction to a statement (its
// text, or who it belongs to) and mends the graph around it
// (knowledge.Threader.Correct), then rewrites the statuses that changed.
func (r *Runner) CorrectStatement(ctx context.Context, o KnowledgeOptions, id int64, text string, who []string) (knowledge.ThreadStats, error) {
	kc := r.Cfg.Knowledge
	llm, err := r.threadLLM(o)
	if err != nil {
		return knowledge.ThreadStats{}, err
	}
	var vecs map[int64][]float32
	if o.Embedder != nil {
		if vecs, err = r.Store.StatementVecs(o.Embedder.Model()); err != nil {
			return knowledge.ThreadStats{}, err
		}
	}
	th := &knowledge.Threader{Store: r.Store, LLM: llm, Vecs: vecs, Version: "correct", Parallel: kc.Threads.Parallel, AuditFixes: kc.Threads.AuditFixes, Logf: r.logf}
	embed := func(s string) ([]float32, string, error) {
		if o.Embedder == nil {
			return nil, "", fmt.Errorf("no embedder")
		}
		v, err := o.Embedder.Embed(ctx, []string{s}, false)
		if err != nil || len(v) == 0 {
			return nil, "", err
		}
		return v[0], o.Embedder.Model(), nil
	}
	if o.Embedder == nil {
		embed = nil
	}
	st, err := th.Correct(ctx, id, text, who, "owner", embed)
	if err != nil {
		return st, err
	}
	var ks KnowledgeStats
	return st, r.writeStatuses(ctx, o, &ks)
}
