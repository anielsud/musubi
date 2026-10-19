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
	return stats, t.rederive(it, changed)
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

// rederive sets the stored "current" of the given threads to what the
// relations and the lapse rule say, as of the latest item threaded.
func (t *Threader) rederive(it ThreadItem, threads map[int64]bool) error {
	d, err := LoadDerivation(t.Store)
	if err != nil {
		return err
	}
	members, err := t.Store.ThreadMembers()
	if err != nil {
		return err
	}
	// As of the latest of: the latest item threaded, this item, and any
	// statement in a thread (as CheckDerived has it).
	if it.Start.After(d.AsOf) {
		d.AsOf = it.Start
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
		if err := t.Store.SetThreadCurrent(tid, cur, t.prov(it, "derive")); err != nil {
			return err
		}
	}
	return nil
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
