package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// Insert threads one item into the graph as it stands, whenever the item
// happened: a backfilled meeting older than what is threaded, an email
// that arrives late, a re-extracted meeting (docs/incremental-design.md).
// Its statements are placed in threads as Thread places them; then, in
// each thread they joined, they are judged against their nearest
// neighbours there, earlier and later, current or not; and "current" is
// derived again for the threads that changed. Nothing is replayed.
func (t *Threader) Insert(ctx context.Context, it ThreadItem) (ThreadStats, error) {
	var stats ThreadStats
	p, err := t.place(ctx, it, &stats)
	if err != nil || p == nil {
		return stats, err
	}
	stats.Touched = len(p.order)
	members, err := t.Store.ThreadMembers()
	if err != nil {
		return stats, err
	}
	inThread := map[int64][]int64{}
	for _, m := range members {
		inThread[m.Topic] = append(inThread[m.Topic], m.Statement)
	}
	var need []int64
	for _, tid := range p.order {
		need = append(need, inThread[tid]...)
	}
	all, err := t.Store.StatementsByID(need)
	if err != nil {
		return stats, err
	}
	type job struct {
		tid        int64
		fresh, old []int64
	}
	var jobs []job
	for _, tid := range p.order {
		th := p.threads[tid]
		j := job{tid: tid, fresh: th.members}
		j.old = t.neighbours(th.members, inThread[tid], insertNeighbours)
		jobs = append(jobs, j)
	}
	type result struct {
		j   job
		out relateOut
		err error
	}
	results := make([]result, len(jobs))
	sem := make(chan struct{}, max(t.Parallel, 1))
	done := make(chan int)
	for i, j := range jobs {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if len(j.old) == 0 {
				results[i] = result{j: j}
				done <- i
				return
			}
			out, err := t.relate(ctx, it, p.threads[j.tid].row, j.fresh, j.old, all, p.byID)
			results[i] = result{j, out, err}
			done <- i
		}()
	}
	for range jobs {
		<-done
	}
	changed := map[int64]bool{}
	for _, r := range results {
		changed[r.j.tid] = true
		if r.err != nil {
			if !errorsIsCutOff(r.err) {
				return stats, r.err
			}
			t.logf("threads %s: relate T%d: %v", it.ID, r.j.tid, r.err)
			continue
		}
		stats.Calls++
		fresh := map[int64]bool{}
		for _, id := range r.j.fresh {
			fresh[id] = true
		}
		shown := map[int64]bool{}
		for _, id := range r.j.old {
			shown[id] = true
		}
		for _, e := range r.out.Relations {
			later, okL := statementAt(e.Later, all, p.byID)
			earlier, okE := statementAt(e.Earlier, all, p.byID)
			if !okL || !okE || e.Later == e.Earlier || !(fresh[e.Later] || fresh[e.Earlier]) || !(fresh[e.Later] || shown[e.Later]) || !(fresh[e.Earlier] || shown[e.Earlier]) {
				continue
			}
			if later.SaidAt.Before(earlier.SaidAt) {
				continue // only a later item replaces an earlier one
			}
			rel := e.Relation
			if rel == "same" {
				rel = "repeats"
			} else {
				rel = threadRuleV4(rel, earlier.Kind, earlier.Status, fresh[e.Later] && fresh[e.Earlier], later.Text)
			}
			if rel == "" {
				continue
			}
			if err := t.Store.RelateStatements(e.Later, e.Earlier, rel, t.prov(it, "")); err != nil {
				return stats, err
			}
			stats.Relations++
			// An earlier item retired here is outdated in its other threads.
			for _, m := range members {
				if m.Statement == e.Earlier {
					changed[m.Topic] = true
				}
			}
		}
	}
	return stats, t.Rederive(it, changed)
}

// insertNeighbours is how many of a thread's statements each new item is
// judged against: the most similar, current or not.
const insertNeighbours = 6

// neighbours picks, for each new item, its nearest statements in the
// thread (by embedding), the union oldest first, at most 40.
func (t *Threader) neighbours(fresh, inThread []int64, k int) []int64 {
	isFresh := map[int64]bool{}
	for _, id := range fresh {
		isFresh[id] = true
	}
	pick := map[int64]bool{}
	for _, f := range fresh {
		type sc struct {
			id  int64
			sim float64
		}
		var ss []sc
		for _, id := range inThread {
			if !isFresh[id] {
				ss = append(ss, sc{id, cosine32(t.Vecs[f], t.Vecs[id])})
			}
		}
		sort.Slice(ss, func(i, j int) bool { return ss[i].sim > ss[j].sim })
		for _, s := range ss[:min(len(ss), k)] {
			pick[s.id] = true
		}
	}
	var out []int64
	for _, id := range inThread {
		if pick[id] && len(out) < 40 {
			out = append(out, id)
		}
	}
	return out
}

// Rederive sets the stored "current" of the given threads to what the
// relations and the lapse rule say, as of the latest item threaded.
func (t *Threader) Rederive(it ThreadItem, threads map[int64]bool) error {
	return RederiveThreads(t.Store, threads, it.Start, t.prov(it, "derive"))
}

// RederiveThreads sets the given threads' stored "current" to the derived
// one, as of the latest of: the latest item threaded, asOf, and any
// statement in a thread (as CheckDerived has it).
func RederiveThreads(st *store.Store, threads map[int64]bool, asOf time.Time, p store.Provenance) error {
	if len(threads) == 0 {
		return nil
	}
	d, err := LoadDerivation(st)
	if err != nil {
		return err
	}
	members, err := st.ThreadMembers()
	if err != nil {
		return err
	}
	if asOf.After(d.AsOf) {
		d.AsOf = asOf
	}
	for _, m := range members {
		if m.SaidAt.After(d.AsOf) {
			d.AsOf = m.SaidAt
		}
	}
	byThread := map[int64][]int64{}
	for _, m := range members {
		if threads[m.Topic] {
			byThread[m.Topic] = append(byThread[m.Topic], m.Statement)
		}
	}
	for tid, ids := range byThread {
		var cur []int64
		for _, id := range ids {
			if d.Current(id) {
				cur = append(cur, id)
			}
		}
		if err := st.SetThreadCurrent(tid, cur, p); err != nil {
			return err
		}
	}
	return nil
}

// ForgetItem deletes an item and everything derived from it (store.Forget),
// then mends the graph where it was: the threads it was in have "current"
// derived again (what only it retired is current again), and threads left
// with no statements go. Nothing is replayed.
func ForgetItem(st *store.Store, itemID string) (rawHash string, stillUsed bool, err error) {
	threads, err := st.ThreadsOfItem(itemID)
	if err != nil {
		return "", false, err
	}
	if rawHash, stillUsed, err = st.Forget(itemID); err != nil {
		return rawHash, stillUsed, err
	}
	if err := st.DeleteEmptyThreads(threads); err != nil {
		return rawHash, stillUsed, err
	}
	return rawHash, stillUsed, RederiveThreads(st, threads, time.Time{}, store.Provenance{During: itemID, Why: "forget"})
}

// Correct changes a statement (its text, or who it belongs to; empty text
// and nil who keep them), then mends the graph around it: re-embedded, its
// relations judged again against its neighbours in each of its threads,
// and "current" derived again there and wherever it retired something.
// Its threads stay as they are: membership is by statement, not by words.
func (t *Threader) Correct(ctx context.Context, id int64, text string, who []string, by string, embed func(string) ([]float32, string, error)) (ThreadStats, error) {
	var stats ThreadStats
	before, err := t.Store.RetiringRelations()
	if err != nil {
		return stats, err
	}
	if err := t.Store.CorrectStatement(id, text, who, by); err != nil {
		return stats, err
	}
	sts, err := t.Store.StatementsByID([]int64{id})
	if err != nil || len(sts) == 0 {
		return stats, err
	}
	s := sts[id]
	if embed != nil && text != "" {
		v, model, err := embed(text)
		if err != nil {
			return stats, err
		}
		if err := t.Store.PutStatementVec(id, model, v); err != nil {
			return stats, err
		}
		if t.Vecs != nil {
			t.Vecs[id] = v
		}
	}
	if err := t.Store.SetSearchText(id, SearchForm(s.Text)); err != nil {
		return stats, err
	}
	members, err := t.Store.ThreadMembers()
	if err != nil {
		return stats, err
	}
	inThread := map[int64][]int64{}
	changed := map[int64]bool{}
	for _, m := range members {
		inThread[m.Topic] = append(inThread[m.Topic], m.Statement)
	}
	var mine []int64
	for tid, ids := range inThread {
		if contains64(ids, id) {
			mine = append(mine, tid)
			changed[tid] = true
		}
	}
	// What it used to retire may be current again.
	for earlier, laters := range before {
		if contains64(laters, id) {
			for _, m := range members {
				if m.Statement == earlier {
					changed[m.Topic] = true
				}
			}
		}
	}
	rows, err := t.Store.Threads()
	if err != nil {
		return stats, err
	}
	rowOf := map[int64]store.ThreadRow{}
	for _, r := range rows {
		rowOf[r.ID] = r
	}
	it := ThreadItem{ID: s.ItemID, Start: s.SaidAt}
	byID := map[int64]store.ItemStatement{id: fromStatement(s)}
	for _, tid := range mine {
		old := t.neighbours([]int64{id}, inThread[tid], insertNeighbours)
		if len(old) == 0 {
			continue
		}
		all, err := t.Store.StatementsByID(old)
		if err != nil {
			return stats, err
		}
		out, err := t.relate(ctx, it, rowOf[tid], []int64{id}, old, all, byID)
		if err != nil {
			if errorsIsCutOff(err) {
				continue
			}
			return stats, err
		}
		stats.Calls++
		for _, e := range out.Relations {
			if e.Later != id && e.Earlier != id {
				continue
			}
			later, okL := statementAt(e.Later, all, byID)
			earlier, okE := statementAt(e.Earlier, all, byID)
			if !okL || !okE || e.Later == e.Earlier || later.SaidAt.Before(earlier.SaidAt) {
				continue
			}
			rel := e.Relation
			if rel == "same" {
				rel = "repeats"
			} else {
				rel = threadRuleV4(rel, earlier.Kind, earlier.Status, false, later.Text)
			}
			if rel == "" {
				continue
			}
			if err := t.Store.RelateStatements(e.Later, e.Earlier, rel, store.Provenance{Version: t.Version, During: "correct", Why: by}); err != nil {
				return stats, err
			}
			stats.Relations++
			for _, m := range members {
				if m.Statement == e.Earlier {
					changed[m.Topic] = true
				}
			}
		}
	}
	return stats, RederiveThreads(t.Store, changed, time.Time{}, store.Provenance{Version: t.Version, During: "correct", Why: by})
}

const relateSystem = `You keep one thread of a leadership team's notes, so that someone asking about the subject sees only where things stand. You get items already in the thread, each with its meeting date, kind/status, who said it, and whether it is CURRENT or RETIRED, and NEW items from one more meeting. That meeting may be older or newer than the items already there.

List each pair where a later item (by date) bears on an earlier one, where at least one of the two is NEW:
- supersedes: the later gives a newer version of the same figure, target, date, plan, decision, status or position
- completes: the later says the commitment or plan was done, or the event happened
- answers: the later answers the question
- same: the later says the same thing again
- contradicts: the later disagrees, without replacing it
Rules, standing facts and open questions stay unless something later replaces them. When unsure, leave the pair out.`

var relateSchema = json.RawMessage(`{"type":"object","required":["relations"],"properties":{"relations":{"type":"array","items":{"type":"object","required":["later","earlier","relation"],"properties":{
"later":{"type":"integer"},"earlier":{"type":"integer"},"relation":{"type":"string","enum":["supersedes","completes","answers","same","contradicts"]}}}}}}`)

type relateOut struct {
	Relations []struct {
		Later    int64  `json:"later"`
		Earlier  int64  `json:"earlier"`
		Relation string `json:"relation"`
	} `json:"relations"`
}

func (t *Threader) relate(ctx context.Context, it ThreadItem, th store.ThreadRow, fresh, old []int64, known map[int64]store.Statement, byID map[int64]store.ItemStatement) (relateOut, error) {
	var out relateOut
	cur := map[int64]bool{}
	for _, id := range th.Current {
		cur[id] = true
	}
	var a, b []string
	for _, id := range old {
		if s, ok := statementAt(id, known, byID); ok {
			flag := "RETIRED"
			if cur[id] {
				flag = "CURRENT"
			}
			a = append(a, itemLine(s, time.Time{})+" · "+flag)
		}
	}
	for _, id := range fresh {
		if s, ok := statementAt(id, known, byID); ok {
			b = append(b, itemLine(s, time.Time{}))
		}
	}
	msg := fmt.Sprintf("Thread: %s — %s\n\nItems already in the thread (oldest first):\n%s\n\nNEW items, from the %s meeting:\n%s\n\nList the pairs.",
		th.Name, th.Scope, strings.Join(a, "\n"), it.Start.Format("2006-01-02"), strings.Join(b, "\n"))
	opt := threadOptions(relateSchema, reconcileMaxTokens)
	opt.Kind, opt.Session = "relate", "threads "+it.ID
	raw, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: relateSystem}, {Role: "user", Content: msg}}, opt, nil)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(jsonPart(raw)), &out); err != nil {
		t.logf("threads %s: relate reply unreadable: %v", it.ID, err)
	}
	return out, nil
}

func errorsIsCutOff(err error) bool { return errors.Is(err, provider.ErrCutOff) }
