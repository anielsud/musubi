package pipeline

import (
	"context"
	"fmt"
	"math"

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
	// Fair placement: statements whose thread from the replay survived the
	// removal (a thread made of this item alone empties and can't be
	// chosen again), and those back in it.
	Survived, SurvivedSame int
	// New threads the insert made, and those that look like an existing
	// thread (centroid similarity ≥ 0.85): likely duplicates.
	NewThreads, NearExisting int
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
	// Threads that hold more than this item survive its removal.
	others := map[int64]bool{}
	for _, x := range rows {
		if affected[x.Topic] && !mine[x.Statement] {
			others[x.Topic] = true
		}
	}
	topicsBefore := map[int64]bool{}
	if ts, err := r.Store.Threads(); err == nil {
		for _, t := range ts {
			topicsBefore[t.ID] = true
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
	for sid, ts := range before {
		for t := range ts {
			if others[t] {
				res.Survived++
				if same[sid] {
					res.SurvivedSame++
				}
				break
			}
		}
	}
	// New threads, and whether each sits near a thread that existed.
	cents := map[int64][]float32{}
	newT := map[int64]bool{}
	for _, x := range rows {
		if !topicsBefore[x.Topic] {
			newT[x.Topic] = true
		}
	}
	res.NewThreads = len(newT)
	if vecs != nil {
		sums := map[int64][]float64{}
		for _, x := range rows {
			v := vecs[x.Statement]
			if v == nil {
				continue
			}
			sm := sums[x.Topic]
			if sm == nil {
				sm = make([]float64, len(v))
				sums[x.Topic] = sm
			}
			for i, f := range v {
				sm[i] += float64(f)
			}
		}
		for t, sm := range sums {
			var n float64
			for _, f := range sm {
				n += f * f
			}
			n = math.Sqrt(n)
			c := make([]float32, len(sm))
			for i, f := range sm {
				c[i] = float32(f / n)
			}
			cents[t] = c
		}
		for t := range newT {
			for o := range topicsBefore {
				if o == t || cents[o] == nil || cents[t] == nil {
					continue
				}
				var d float64
				for i := range cents[t] {
					d += float64(cents[t][i]) * float64(cents[o][i])
				}
				if d >= 0.85 {
					res.NearExisting++
					break
				}
			}
		}
	}
	return res, nil
}

// CorrectStatement applies the owner's correction to a statement (its
// text, or who it belongs to) and mends the graph around it
// (knowledge.Threader.Correct), then rewrites the statuses that changed.
func (r *Runner) CorrectStatement(ctx context.Context, o KnowledgeOptions, id int64, text string, who []string) (knowledge.ThreadStats, error) {
	th, embed, err := r.mender(ctx, o)
	if err != nil {
		return knowledge.ThreadStats{}, err
	}
	st, err := th.Correct(ctx, id, text, who, "owner", embed)
	if err != nil {
		return st, err
	}
	var ks KnowledgeStats
	return st, r.writeStatuses(ctx, o, &ks)
}

// UndoStatementEdit puts a statement back as it was before one of the
// owner's corrections and mends the graph around it, as a correction does.
func (r *Runner) UndoStatementEdit(ctx context.Context, o KnowledgeOptions, id int64) error {
	es, err := r.Store.StatementEdits()
	if err != nil {
		return err
	}
	var field string
	for _, e := range es {
		if e.ID == id {
			field = e.Field
		}
	}
	if field == "" {
		return fmt.Errorf("no edit %d to undo", id)
	}
	th, embed, err := r.mender(ctx, o)
	if err != nil {
		return err
	}
	stmt, err := r.Store.UndoCorrection(id)
	if err != nil {
		return err
	}
	if _, err := th.Rerelate(ctx, stmt, field == "text", "owner", embed); err != nil {
		return err
	}
	var ks KnowledgeStats
	return r.writeStatuses(ctx, o, &ks)
}

// mender is the threader that mends the graph around an owner's
// correction, with the embedder for a changed text.
func (r *Runner) mender(ctx context.Context, o KnowledgeOptions) (*knowledge.Threader, func(string) ([]float32, string, error), error) {
	kc := r.Cfg.Knowledge
	llm, err := r.threadLLM(o)
	if err != nil {
		return nil, nil, err
	}
	var vecs map[int64][]float32
	if o.Embedder != nil {
		if vecs, err = r.Store.StatementVecs(o.Embedder.Model()); err != nil {
			return nil, nil, err
		}
	}
	th := &knowledge.Threader{Store: r.Store, LLM: llm, Vecs: vecs, Version: "correct", Parallel: kc.Threads.Parallel, AuditFixes: kc.Threads.AuditFixes, Logf: r.logf}
	if o.Embedder == nil {
		return th, nil, nil
	}
	return th, func(s string) ([]float32, string, error) {
		v, err := o.Embedder.Embed(ctx, []string{s}, false)
		if err != nil || len(v) == 0 {
			return nil, "", err
		}
		return v[0], o.Embedder.Model(), nil
	}, nil
}

// RefreshSpellings brings what is derived from the statements using any of
// words in line with the spelling rules now (after one is made, changed or
// undone): their vectors are of the text as the rules read it, search
// finds them by both forms, and the statuses of their threads are written
// again. Stored text is not changed; threads are not moved.
func (r *Runner) RefreshSpellings(ctx context.Context, o KnowledgeOptions, words []string) (int, error) {
	fix, err := r.Store.Corrector()
	if err != nil {
		return 0, err
	}
	seen := map[int64]bool{}
	var ids []int64
	for _, w := range words {
		us, err := r.Store.StatementsUsing(w)
		if err != nil {
			return 0, err
		}
		for _, id := range us {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sts, err := r.Store.StatementsByID(ids)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		s, ok := sts[id]
		if !ok {
			continue
		}
		read := fix.Text(id, s.Text)
		if o.Embedder != nil {
			v, err := o.Embedder.Embed(ctx, []string{read}, false)
			if err != nil {
				return 0, err
			}
			if len(v) > 0 {
				if err := r.Store.PutStatementVec(id, o.Embedder.Model(), v[0]); err != nil {
					return 0, err
				}
			}
		}
		search := s.Text
		if read != s.Text {
			search += " " + read
		}
		if err := r.Store.SetSearchText(id, knowledge.SearchForm(search)); err != nil {
			return 0, err
		}
	}
	var ks KnowledgeStats
	return len(ids), r.writeStatuses(ctx, o, &ks)
}
