package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Threads (knowledge.grouping = "threads"): instead of clustering claims
// and judging statements pair by pair, a model reads each meeting's items
// against the threads so far, then each thread the meeting touched as a
// whole. On the linking benchmark this kept subjects together (B-cubed
// precision 0.93 against 0.73, recall 0.48 against 0.21) and almost never
// hid a live item (1% against 22% of sampled pairs); docs/linking-benchmark.md,
// research-log.md 2026-10-14.
//
// The lean form (t3, research-log.md 2026-10-15/16) keeps a meeting's cost
// flat however long the history: an assign call sees candidate threads, not
// every thread; a reconcile call sees a thread's latest current items, not
// all of them; replies carry codes and effects, not repeated names and
// lists. On the benchmark it cost 30–40% less with the same update tracking,
// and its prompts stayed the same size over a 20-meeting replay.

// ThreadsVersion changes with the threading prompts or rules; a change
// rebuilds every thread.
const ThreadsVersion = "t3"

// ThreadsVersionFixes is the version with Threader.AuditFixes.
const ThreadsVersionFixes = "t4"

const assignSystem = `You organise a leadership team's meeting notes into threads, so that later someone can see the current state of any subject.

A thread is ONE specific subject that can have a state over time: an initiative, programme, deal, account, metric, decision, problem, launch, event, hire or departure. It is never a broad theme ("AI strategy", "marketing", "growth"): if a theme holds several specific subjects, those are separate threads. Two items that share a word, a customer name, an event or a number are not in the same thread unless they are about the same subject.

You get candidate threads (codes T…) and new items. Put every new item in a group: an existing thread's code, a new thread you define (codes n1, n2, … with a short, specific name and a one-line scope), or none (meeting logistics, greetings, jokes, small talk). An item may be in two groups only when it genuinely bears on two subjects.`

// assignFix is added to assignSystem with AuditFixes: subjects fragmented by
// the angle of each sentence and merged through people and meetings.
const assignFix = `

Prefer an existing thread when the item continues its subject: a new figure, an outcome, a follow-up, a decision, or a different angle on the same initiative, deal, programme or event, even when the wording differs. Start a new thread only for a subject none of the candidates covers.
A person is not a subject: put items about someone's role, hire, departure or performance in the thread about that event (one thread per hire, departure or case), not in a thread about the person.
A meeting is not a subject either: an interview, a 1:1 or a review covers several subjects; put each item in the thread of its own subject.`

const mergeSystem = `You keep a leadership team's notes organised in threads, one per specific subject. Some threads were just started in today's meeting; each is shown with older threads that look similar. Say which new threads are the same specific subject as an older one (the same initiative, deal, programme, metric, issue, event or hire), so they can be merged. Different subjects that share a word, a person, a customer or a meeting are not the same. When unsure, do not merge.`

var mergeSchema = json.RawMessage(`{"type":"object","required":["merges"],"properties":{"merges":{"type":"array","items":{"type":"object","required":["new","into"],"properties":{"new":{"type":"string"},"into":{"type":"string"}}}}}}`)

const consolidateSystem = `You keep one thread of a leadership team's notes up to date, so that someone asking about the subject sees only where things stand now. You get the thread's current items, oldest first, each with its meeting date, and today's date.

List the items that are no longer the current word on this subject, each with the later item in the list that makes it so:
- superseded: a later item gives a newer version of the same figure, target, date, plan, decision, status or position
- completed: a later item says the commitment or plan was done, or the event happened
- answered: a later item answers the question
Keep everything still true or still open: rules, standing facts, decisions still in force, open questions nobody answered, commitments not yet due. When unsure, keep the item.`

var consolidateSchema = json.RawMessage(`{"type":"object","required":["outdated"],"properties":{"outdated":{"type":"array","items":{"type":"object","required":["id","by","why"],"properties":{"id":{"type":"integer"},"by":{"type":"integer"},"why":{"type":"string","enum":["superseded","completed","answered"]}}}}}}`)

// Consolidation and lapse (AuditFixes): a touched thread with more than
// consolidateMin current items has up to consolidateMax of them reviewed;
// open commitments and questions untouched for lapseAfter stop being
// current. Measured on the ELT year (research log 2026-10-17).
const (
	consolidateMin = 8
	consolidateMax = 80
	lapseAfter     = 60 * 24 * time.Hour
)

// mergeSim: a new thread is offered for merging into older threads at least
// this similar (centroids).
const mergeSim = 0.75

var assignSchema = json.RawMessage(`{"type":"object","required":["new_threads","groups"],"properties":{
"new_threads":{"type":"array","items":{"type":"object","required":["code","name","scope"],"properties":{"code":{"type":"string"},"name":{"type":"string"},"scope":{"type":"string"}}}},
"groups":{"type":"array","items":{"type":"object","required":["thread","ids"],"properties":{"thread":{"type":"string"},"ids":{"type":"array","items":{"type":"integer"}}}}}}}`)

const reconcileSystem = `You keep one thread of a leadership team's notes up to date. You get the thread's current items (oldest first) and the new items from today's meeting (in the order they were said).

List only what a new item does to an earlier item in this thread (a current one, or a new one said before it today):
- supersedes: a later version of the same plan, figure, decision, status or position that replaces it (within one meeting only when someone corrects or changes it, not when they walk through it)
- corrects: same meeting, a correction of a figure or claim
- completes: says an earlier commitment or plan was done or happened
- answers: gives what an earlier question asked
- pending: commits someone to find out or provide what an earlier question asks (the question stays open)
- contradicts: both cannot be true, about the same specific thing, and neither is clearly the later word
- same: says the same thing again
Most new items do none of these: they add to the thread. Only list real effects; an empty list is common.`

// reconcileFix is added to reconcileSystem with AuditFixes: the audits found
// later figures, outcomes and decisions months on not retiring what they
// settle, and added detail or disagreement taken for replacement.
const reconcileFix = `

What replaces what:
- A later figure for the same quantity and period (a forecast, a target, a count, a date) supersedes the earlier one, even months later.
- An outcome (done, launched, decided, signed, lost, cancelled, hired, left) completes or supersedes the plans, proposals, commitments and open questions it settles, even months later.
- Adding detail or context to an earlier item does not supersede it; a disagreement is "contradicts", not "supersedes".
- Only a newer item can replace an older one; never mark the newest statement of something as replaced by an older one.
Items under "Related items in other threads" may be affected too, by the same rules.`

// Cross-thread context (AuditFixes): other threads' current items at least
// this similar to a new item, at most crossMax of them.
const (
	crossSim    = 0.80
	crossMax    = 10
	recentShown = 5 // the latest current items always shown
)

var reconcileSchema = json.RawMessage(`{"type":"object","required":["effects"],"properties":{
"effects":{"type":"array","items":{"type":"object","required":["later","earlier","relation"],"properties":{"later":{"type":"integer"},"earlier":{"type":"integer"},
"relation":{"type":"string","enum":["supersedes","corrects","completes","answers","pending","contradicts","same"]}}}}}}`)

// Bounds that keep a meeting's cost independent of history (defaults).
const (
	threadCandidates = 40                  // nearest threads by embedding, per assign call
	threadRecent     = 15                  // plus the most recently active
	recentWindow     = 21 * 24 * time.Hour // "recently active"
	threadStateCap   = 25                  // current items shown per reconcile call
)

// Threader threads items one at a time, oldest first.
type Threader struct {
	Store *store.Store
	LLM   provider.LLM
	// Version stamps what it writes (the threading rules' stamp).
	Version string
	// Vecs are statement vectors (for candidate threads); nil shows the
	// most recently active threads only.
	Vecs map[int64][]float32
	// Chunk is how many items one assign call reads (default 70);
	// Parallel how many threads are reconciled at once (default 4);
	// Candidates and StateCap override the bounds above.
	Chunk, Parallel, Candidates, StateCap int
	// AuditFixes (knowledge.threads.audit_fixes; research log 2026-10-16,
	// backfill): reconcile sees the thread's current items nearest the new
	// ones (plus the latest few) instead of only the latest, and related
	// current items of other threads, which it may retire; its prompt says
	// what replaces what; an item is never retired by an older one;
	// "completes" also takes positions and proposals. false is t3.
	AuditFixes bool
	// WarmFirst reconciles one thread alone before the rest, so their
	// shared instructions are cached by the server.
	WarmFirst bool
	Logf      func(format string, args ...any)
}

// ThreadItem is an item to thread: its id, and when it happened (minutes
// in the prompts count from it).
type ThreadItem struct {
	ID    string
	Start time.Time
}

type ThreadStats struct {
	Items, Statements, NewThreads, Touched, Calls, Relations int
	// Unplaced: statements no thread took after the retries.
	Unplaced int
}

// prov is the provenance of a change made while threading it.
func (t *Threader) prov(it ThreadItem, why string) store.Provenance {
	return store.Provenance{Version: t.Version, During: it.ID, Why: why}
}

func (t *Threader) logf(f string, a ...any) {
	if t.Logf != nil {
		t.Logf(f, a...)
	}
}

func itemLine(s store.ItemStatement, start time.Time) string {
	who := strings.Join(s.Who, ", ")
	if who == "" {
		who = s.Holder
	}
	if who == "" {
		who = "?"
	}
	st := ""
	if s.Status != "" {
		st = "/" + s.Status
	}
	when := s.SaidAt.Format("2006-01-02")
	if !start.IsZero() {
		when = fmt.Sprintf("%s m%d", start.Format("2006-01-02"), int(s.SaidAt.Sub(start).Minutes()))
	}
	return fmt.Sprintf("[%d] (%s, %s%s, %s) %s", s.ID, when, s.Kind, st, who, s.Text)
}

func fromStatement(s store.Statement) store.ItemStatement {
	return store.ItemStatement{ID: s.ID, Kind: s.Kind, Status: s.Status, Who: s.Who, Holder: s.Holder, Text: s.Text, SaidAt: s.SaidAt}
}

// threadState is what the threader knows of a thread while threading one
// item.
type threadState struct {
	row      store.ThreadRow
	sum      []float64 // sum of member vectors
	last     time.Time // when a member was last said
	born     bool      // created while threading this item
	members  []int64   // new statements, in order
	previous []int64   // current statements before this item
	size     int       // statements before this item
	first    time.Time // when its first statement was said
}

func (st *threadState) centroid() []float64 {
	var n float64
	for _, x := range st.sum {
		n += x * x
	}
	if n == 0 {
		return nil
	}
	n = math.Sqrt(n)
	out := make([]float64, len(st.sum))
	for i, x := range st.sum {
		out[i] = x / n
	}
	return out
}

func (st *threadState) add(v []float32) {
	if v == nil {
		return
	}
	if st.sum == nil {
		st.sum = make([]float64, len(v))
	}
	for i, x := range v {
		if i < len(st.sum) {
			st.sum[i] += float64(x)
		}
	}
}

// Thread assigns one item's statements to threads and reconciles each
// thread it touched.
func (t *Threader) Thread(ctx context.Context, it ThreadItem) (ThreadStats, error) {
	var stats ThreadStats
	sts, err := t.Store.LinkedItemStatements(it.ID)
	if err != nil || len(sts) == 0 {
		return stats, err
	}
	stats.Items, stats.Statements = 1, len(sts)
	chunk := t.Chunk
	if chunk <= 0 {
		chunk = 70
	}
	byID := map[int64]store.ItemStatement{}
	for _, s := range sts {
		byID[s.ID] = s
	}
	rows, err := t.Store.Threads()
	if err != nil {
		return stats, err
	}
	all, err := t.Store.ThreadMembers()
	if err != nil {
		return stats, err
	}
	threads := map[int64]*threadState{}
	byName := map[string]int64{}
	var current []int64
	for _, r := range rows {
		threads[r.ID] = &threadState{row: r, previous: r.Current}
		byName[strings.ToLower(strings.TrimSpace(r.Name))] = r.ID
		current = append(current, r.Current...)
	}
	for _, m := range all {
		if th := threads[m.Topic]; th != nil {
			th.add(t.Vecs[m.Statement])
			if m.SaidAt.After(th.last) {
				th.last = m.SaidAt
			}
			if th.first.IsZero() || m.SaidAt.Before(th.first) {
				th.first = m.SaidAt
			}
			th.size++
		}
	}
	known, err := t.Store.StatementsByID(current)
	if err != nil {
		return stats, err
	}
	var order []int64 // touched threads, in the order first touched
	for k := 0; k < len(sts); k += chunk {
		part := sts[k:min(k+chunk, len(sts))]
		reg := t.registry(part, it.Start, threads, known, byID)
		var lines []string
		for _, s := range part {
			lines = append(lines, itemLine(s, it.Start))
		}
		res, err := t.assign(ctx, it, fmt.Sprintf("Candidate threads:\n%s\nNew items from the %s meeting:\n%s\n\nPut every new item in a group (by id).",
			reg, it.Start.Format("2006-01-02"), strings.Join(lines, "\n")))
		if err != nil {
			return stats, err
		}
		stats.Calls++
		// Outputs sometimes collapse (no or few items placed): ask again for
		// the items left out, with the threads named so far.
		for retry := 0; retry < 3; retry++ {
			// Placed only in a group whose thread resolves: items grouped
			// under a code or name that names no thread were dropped
			// before, without asking again (8–10% of a year's items, a
			// few meetings losing half; research log 2026-10-19).
			codes := map[string]int64{} // new codes and names, resolved later
			for _, n := range res.NewThreads {
				codes[strings.TrimSpace(n.Code)] = -1
				codes[strings.ToLower(strings.TrimSpace(n.Name))] = -1
			}
			got := map[int64]bool{}
			for _, g := range res.Groups {
				// "none" leaves an item out on purpose (small talk); anything
				// else that names no thread is asked again.
				ref := strings.TrimSpace(g.Thread)
				if _, ok := resolveThread(ref, codes, threads, byName); !ok && !strings.EqualFold(ref, "none") {
					continue
				}
				for _, id := range g.IDs {
					got[id] = true
				}
			}
			var missing []string
			for _, s := range part {
				if !got[s.ID] {
					missing = append(missing, itemLine(s, it.Start))
				}
			}
			if len(missing) <= len(part)/20 {
				break
			}
			var named []string
			for _, n := range res.NewThreads {
				named = append(named, n.Code+": "+n.Name+" — "+n.Scope)
			}
			more, err := t.assign(ctx, it, fmt.Sprintf("Candidate threads:\n%s%s\n\nItems from the %s meeting still to place:\n%s\n\nPut every one of these items in a group (by id); new threads continue the n… numbering.",
				reg, strings.Join(named, "\n"), it.Start.Format("2006-01-02"), strings.Join(missing, "\n")))
			if err != nil {
				return stats, err
			}
			stats.Calls++
			res.NewThreads = append(res.NewThreads, more.NewThreads...)
			res.Groups = append(res.Groups, more.Groups...)
		}
		local := map[string]int64{} // n1 → thread, and new names → thread
		for _, n := range res.NewThreads {
			code, name := strings.TrimSpace(n.Code), strings.TrimSpace(n.Name)
			if code == "" || name == "" {
				continue
			}
			if tid, ok := byName[strings.ToLower(name)]; ok {
				local[code] = tid
				local[strings.ToLower(name)] = tid
				continue
			}
			tid, err := t.Store.NewThread(name, n.Scope, 0)
			if err != nil {
				return stats, err
			}
			byName[strings.ToLower(name)] = tid
			threads[tid] = &threadState{row: store.ThreadRow{ID: tid, Name: name, Scope: n.Scope}, born: true}
			local[code] = tid
			local[strings.ToLower(name)] = tid
			stats.NewThreads++
		}
		inPart := map[int64]bool{}
		for _, s := range part {
			inPart[s.ID] = true
		}
		placed := map[int64]int{}
		for _, g := range res.Groups {
			tid, ok := resolveThread(strings.TrimSpace(g.Thread), local, threads, byName)
			if !ok {
				if len(g.IDs) > 0 && !strings.EqualFold(strings.TrimSpace(g.Thread), "none") {
					t.logf("threads %s: %d items grouped under %q, which names no thread", it.ID, len(g.IDs), g.Thread)
				}
				continue // none, or a code nobody defined
			}
			th := threads[tid]
			for _, id := range g.IDs {
				if !inPart[id] || placed[id] == 2 || contains64(th.members, id) {
					continue
				}
				placed[id]++
				if len(th.members) == 0 {
					order = append(order, tid)
				}
				th.members = append(th.members, id)
				th.add(t.Vecs[id])
				th.last = byID[id].SaidAt
			}
		}
	}
	placedAll := map[int64]bool{}
	for _, th := range threads {
		for _, id := range th.members {
			placedAll[id] = true
		}
	}
	for _, s := range sts {
		if !placedAll[s.ID] {
			stats.Unplaced++
		}
	}
	// A thread named in an assign reply (often a retry's) that got no
	// statements would stay as an empty thread for ever.
	for tid, th := range threads {
		if th.born && len(th.members) == 0 {
			if err := t.Store.DeleteThread(tid); err != nil {
				return stats, err
			}
			delete(threads, tid)
			stats.NewThreads--
		}
	}
	if t.AuditFixes {
		var err error
		if order, err = t.mergeBorn(ctx, it, threads, order, byID, known, &stats); err != nil {
			return stats, err
		}
	}
	for _, tid := range order {
		th := threads[tid]
		var lvl sensitivity.Level
		for _, id := range th.members {
			lvl = max(lvl, byID[id].Level)
		}
		if err := t.Store.AddToThread(tid, th.members, lvl, t.prov(it, "")); err != nil {
			return stats, err
		}
	}
	stats.Touched = len(order)
	if err := t.reconcileAll(ctx, it, order, threads, known, byID, &stats); err != nil {
		return stats, err
	}
	if !t.AuditFixes {
		return stats, nil
	}
	if err := t.consolidate(ctx, it, order, threads, known, byID, &stats); err != nil {
		return stats, err
	}
	return stats, t.lapse(it, threads, known)
}

// statementAt returns a statement's kind, time and text, from this item or
// from earlier ones.
func statementAt(id int64, known map[int64]store.Statement, byID map[int64]store.ItemStatement) (store.ItemStatement, bool) {
	if s, ok := byID[id]; ok {
		return s, true
	}
	if s, ok := known[id]; ok {
		return fromStatement(s), true
	}
	return store.ItemStatement{}, false
}

// consolidate reviews each touched thread's whole current list once: which
// items a later item in it has replaced, completed or answered. Reconcile
// only judges what the new items do; the audits found 86% of items still
// current months after they were settled.
func (t *Threader) consolidate(ctx context.Context, it ThreadItem, order []int64, threads map[int64]*threadState, known map[int64]store.Statement, byID map[int64]store.ItemStatement, stats *ThreadStats) error {
	type result struct {
		tid int64
		cur []int64
		out struct {
			Outdated []struct {
				ID, By int64
				Why    string
			} `json:"outdated"`
		}
		err error
	}
	var todo []int64
	for _, tid := range order {
		if th := threads[tid]; th != nil && len(th.previous) > consolidateMin {
			todo = append(todo, tid)
		}
	}
	results := make([]result, len(todo))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(t.Parallel, 1))
	for i, tid := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			th := threads[tid]
			cur := th.previous[max(0, len(th.previous)-consolidateMax):]
			var lines []string
			for _, id := range cur {
				if s, ok := statementAt(id, known, byID); ok {
					lines = append(lines, itemLine(s, time.Time{}))
				}
			}
			msg := fmt.Sprintf("Thread: %s — %s\nToday: %s\n\nCurrent items (oldest first):\n%s\n\nList the items that are no longer the current word.",
				th.row.Name, th.row.Scope, it.Start.Format("2006-01-02"), strings.Join(lines, "\n"))
			opt := threadOptions(consolidateSchema, reconcileMaxTokens)
			opt.Session = "threads " + it.ID
			raw, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: consolidateSystem}, {Role: "user", Content: msg}}, opt, nil)
			results[i] = result{tid: tid, cur: cur, err: err}
			if err == nil {
				json.Unmarshal([]byte(jsonPart(raw)), &results[i].out)
			}
		}()
	}
	wg.Wait()
	rels := map[string]string{"superseded": "supersedes", "completed": "completes", "answered": "answers"}
	for _, r := range results {
		stats.Calls++
		if r.err != nil {
			t.logf("threads %s: consolidate T%d: %v", it.ID, r.tid, r.err)
			continue // the thread just isn't consolidated this time
		}
		in := map[int64]bool{}
		for _, id := range r.cur {
			in[id] = true
		}
		gone := map[int64]bool{}
		for _, o := range r.out.Outdated {
			rel := rels[o.Why]
			if rel == "" || !in[o.ID] || !in[o.By] || o.ID == o.By {
				continue
			}
			early, ok1 := statementAt(o.ID, known, byID)
			late, ok2 := statementAt(o.By, known, byID)
			if !ok1 || !ok2 || early.SaidAt.After(late.SaidAt) || (early.SaidAt.Equal(late.SaidAt) && o.By < o.ID) {
				continue // only a later item replaces an earlier one
			}
			// The same rules as reconcile's: no in-meeting walk-through taken
			// for replacement, and only plans are completed.
			_, sameA := byID[o.ID]
			_, sameB := byID[o.By]
			if rel = threadRuleV4(rel, early.Kind, early.Status, sameA && sameB, late.Text); rel != "supersedes" && rel != "completes" && rel != "answers" {
				continue
			}
			if err := t.Store.RelateStatements(o.By, o.ID, rel, t.prov(it, "")); err != nil {
				return err
			}
			stats.Relations++
			gone[o.ID] = true
		}
		if len(gone) == 0 {
			continue
		}
		th := threads[r.tid]
		var now []int64
		for _, id := range th.previous {
			if !gone[id] {
				now = append(now, id)
			}
		}
		if err := t.Store.SetThreadCurrent(r.tid, now, t.prov(it, "consolidate")); err != nil {
			return err
		}
		th.previous = now
	}
	return nil
}

// lapse drops open commitments and questions said more than lapseAfter
// before this item from every thread's current list: dated plans otherwise
// stay current for ever (the backfill audits).
func (t *Threader) lapse(it ThreadItem, threads map[int64]*threadState, known map[int64]store.Statement) error {
	for tid, th := range threads {
		var now []int64
		for _, id := range th.previous {
			s, ok := known[id]
			if ok && (s.Kind == "commitment" || s.Kind == "question") && it.Start.Sub(s.SaidAt) > lapseAfter {
				continue
			}
			now = append(now, id)
		}
		if len(now) != len(th.previous) {
			if err := t.Store.SetThreadCurrent(tid, now, t.prov(it, "lapse")); err != nil {
				return err
			}
			th.previous = now
		}
	}
	return nil
}

// registry lists the threads an assign call chooses from: the nearest to
// the batch's items by embedding, the most recently active, and those born
// while threading this item.
func (t *Threader) registry(part []store.ItemStatement, start time.Time, threads map[int64]*threadState, known map[int64]store.Statement, byID map[int64]store.ItemStatement) string {
	nc := t.Candidates
	if nc <= 0 {
		nc = threadCandidates
	}
	type scored struct {
		id    int64
		score float64
	}
	var near []scored
	for id, th := range threads {
		c := th.centroid()
		if c == nil {
			continue
		}
		best := -1.0
		for _, s := range part {
			if v := t.Vecs[s.ID]; v != nil {
				best = math.Max(best, dot64(c, v))
			}
		}
		near = append(near, scored{id, best})
	}
	sort.Slice(near, func(i, j int) bool {
		return near[i].score > near[j].score || near[i].score == near[j].score && near[i].id < near[j].id
	})
	show := map[int64]bool{}
	var list []int64
	add := func(id int64) {
		if !show[id] {
			show[id] = true
			list = append(list, id)
		}
	}
	for i := 0; i < len(near) && i < nc; i++ {
		add(near[i].id)
	}
	var recent []int64
	for id, th := range threads {
		if !th.last.IsZero() && start.Sub(th.last) <= recentWindow {
			recent = append(recent, id)
		}
	}
	sort.Slice(recent, func(i, j int) bool { return threads[recent[i]].last.After(threads[recent[j]].last) })
	for i := 0; i < len(recent) && i < threadRecent; i++ {
		add(recent[i])
	}
	for id, th := range threads {
		if th.born {
			add(id)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	var b strings.Builder
	for _, id := range list {
		th := threads[id]
		latest := "-"
		if n := len(th.previous); n > 0 {
			latest = clip(known[th.previous[n-1]].Text, 140)
		} else if n := len(th.members); n > 0 {
			latest = clip(byID[th.members[n-1]].Text, 140)
		}
		if t.AuditFixes {
			// Size, dates and the two latest items help tell a thread to
			// continue from a lookalike.
			var lat []string
			for _, x := range th.previous[max(0, len(th.previous)-2):] {
				lat = append(lat, clip(known[x].Text, 110))
			}
			if len(lat) == 0 && len(th.members) > 0 {
				lat = append(lat, clip(byID[th.members[len(th.members)-1]].Text, 110))
			}
			if len(lat) == 0 {
				lat = []string{"-"}
			}
			span := "new today"
			if !th.first.IsZero() {
				span = fmt.Sprintf("%d items, %s to %s", th.size, th.first.Format("2006-01-02"), th.last.Format("2006-01-02"))
			}
			fmt.Fprintf(&b, "T%d: %s — %s (%s; latest: %s)\n", id, th.row.Name, th.row.Scope, span, strings.Join(lat, " | "))
			continue
		}
		fmt.Fprintf(&b, "T%d: %s — %s (latest: %s)\n", id, th.row.Name, th.row.Scope, latest)
	}
	if b.Len() == 0 {
		return "(none yet)\n"
	}
	return b.String()
}

func dot64(a []float64, b []float32) float64 {
	var d, nb float64
	for i := range a {
		if i < len(b) {
			d += a[i] * float64(b[i])
			nb += float64(b[i]) * float64(b[i])
		}
	}
	if nb == 0 {
		return 0
	}
	return d / math.Sqrt(nb)
}

func contains64(xs []int64, x int64) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// reconcileAll reconciles each touched thread and records its effects; a
// thread's current statements become its previous ones plus the new, less
// those retired.
func (t *Threader) reconcileAll(ctx context.Context, it ThreadItem, order []int64, threads map[int64]*threadState, known map[int64]store.Statement, byID map[int64]store.ItemStatement, stats *ThreadStats) error {
	capN := t.StateCap
	if capN <= 0 {
		capN = threadStateCap
	}
	par := t.Parallel
	if par <= 0 {
		par = 4
	}
	type result struct {
		tid          int64
		out          reconcileOut
		err          error
		shown, cross []int64
		crossOf      map[int64]int64 // cross item → its thread
	}
	jobs := make(chan int64)
	results := make(chan result)
	var wg sync.WaitGroup
	// The first reconcile alone when warming: the others then find the
	// shared instructions cached.
	warm := make(chan struct{})
	var warmOnce sync.Once
	for w := 0; w < par; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tid := range jobs {
				th := threads[tid]
				shown := t.shownFor(th, capN)
				cross, crossOf := t.crossFor(tid, threads)
				out, err := t.reconcile(ctx, it, th.row, shown, th.members, known, byID, cross, crossOf, threads)
				if tid == order[0] {
					warmOnce.Do(func() { close(warm) })
				}
				results <- result{tid, out, err, shown, cross, crossOf}
			}
		}()
	}
	go func() {
		for i, tid := range order {
			jobs <- tid
			if i == 0 && t.WarmFirst {
				<-warm
			}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	var firstErr error
	elsewhere := map[int64][]int64{} // thread → items retired from it by another thread's update
	for r := range results {
		stats.Calls++
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		th := threads[r.tid]
		fresh := map[int64]bool{}
		for _, id := range th.members {
			fresh[id] = true
		}
		valid := map[int64]bool{}
		for _, id := range r.shown {
			valid[id] = true
		}
		for _, id := range r.cross {
			valid[id] = true
		}
		for id := range fresh {
			valid[id] = true
		}
		retired := map[int64]bool{}
		seen := map[[2]int64]bool{}
		for _, e := range r.out.Effects {
			if !fresh[e.Later] || !valid[e.Earlier] || e.Later == e.Earlier || seen[[2]int64{e.Later, e.Earlier}] {
				continue
			}
			seen[[2]int64{e.Later, e.Earlier}] = true
			if e.Relation == "same" {
				if t.AuditFixes {
					// The newer wording stays: it may add detail, and it dates
					// the subject. Dropping it was most of the audited wrong
					// retirements of t4's first run. Recorded, so "current"
					// can be derived from relations (incremental-design.md).
					retired[e.Earlier] = true
					if err := t.Store.RelateStatements(e.Later, e.Earlier, "repeats", t.prov(it, "")); err != nil {
						return err
					}
					stats.Relations++
				} else {
					retired[e.Later] = true // the earlier wording stays
				}
				continue
			}
			ek, es, eat := known[e.Earlier].Kind, known[e.Earlier].Status, known[e.Earlier].SaidAt
			if fresh[e.Earlier] {
				ek, es, eat = byID[e.Earlier].Kind, byID[e.Earlier].Status, byID[e.Earlier].SaidAt
			}
			if t.AuditFixes && eat.After(byID[e.Later].SaidAt) {
				continue // never retired by an older item
			}
			var rel string
			if t.AuditFixes {
				rel = threadRuleV4(e.Relation, ek, es, fresh[e.Earlier], byID[e.Later].Text)
			} else {
				rel = threadRule(e.Relation, ek, fresh[e.Earlier], byID[e.Later].Text)
			}
			if rel == "" {
				continue
			}
			if err := t.Store.RelateStatements(e.Later, e.Earlier, rel, t.prov(it, "")); err != nil {
				return err
			}
			stats.Relations++
			if rel == "supersedes" || rel == "completes" || rel == "answers" {
				retired[e.Earlier] = true
				if o, ok := r.crossOf[e.Earlier]; ok {
					elsewhere[o] = append(elsewhere[o], e.Earlier)
				}
			}
		}
		var now []int64
		for _, id := range append(append([]int64(nil), th.previous...), th.members...) {
			if !retired[id] {
				now = append(now, id)
			}
		}
		if err := t.Store.SetThreadCurrent(r.tid, now, t.prov(it, "reconcile")); err != nil {
			return err
		}
		th.previous, th.members = now, nil
	}
	// Items retired from other threads (AuditFixes' cross context).
	for tid, gone := range elsewhere {
		th := threads[tid]
		if th == nil {
			continue
		}
		cur := th.previous
		if len(th.members) > 0 { // touched but not yet written: its own result will rebuild it
			continue
		}
		var now []int64
		for _, id := range cur {
			if !contains64(gone, id) {
				now = append(now, id)
			}
		}
		if len(now) != len(cur) {
			if err := t.Store.SetThreadCurrent(tid, now, t.prov(it, "cross")); err != nil {
				return err
			}
			th.previous = now
		}
	}
	return firstErr
}

// shownFor is the thread's current items a reconcile call sees: the latest
// capN, or with AuditFixes the latest few plus those nearest the new items,
// oldest first.
func (t *Threader) shownFor(th *threadState, capN int) []int64 {
	cur := th.previous
	if !t.AuditFixes || len(cur) <= capN {
		if len(cur) > capN {
			return cur[len(cur)-capN:]
		}
		return cur
	}
	recent := map[int64]bool{}
	for _, id := range cur[max(0, len(cur)-recentShown):] {
		recent[id] = true
	}
	type scored struct {
		id  int64
		sim float64
	}
	var rest []scored
	for _, id := range cur {
		if recent[id] {
			continue
		}
		best := -1.0
		if v := t.Vecs[id]; v != nil {
			for _, m := range th.members {
				if w := t.Vecs[m]; w != nil {
					best = math.Max(best, cos(v, w))
				}
			}
		}
		rest = append(rest, scored{id, best})
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].sim > rest[j].sim })
	keep := recent
	for i := 0; i < len(rest) && i < capN-recentShown; i++ {
		keep[rest[i].id] = true
	}
	var out []int64
	for _, id := range cur {
		if keep[id] {
			out = append(out, id)
		}
	}
	return out
}

// crossFor is, with AuditFixes, other threads' current items nearest the
// new items of thread tid: updates often land in a sibling thread.
func (t *Threader) crossFor(tid int64, threads map[int64]*threadState) ([]int64, map[int64]int64) {
	if !t.AuditFixes {
		return nil, nil
	}
	th := threads[tid]
	type scored struct {
		id, thread int64
		sim        float64
	}
	var cands []scored
	for oid, o := range threads {
		if oid == tid {
			continue
		}
		for _, id := range o.previous {
			v := t.Vecs[id]
			if v == nil {
				continue
			}
			best := -1.0
			for _, m := range th.members {
				if w := t.Vecs[m]; w != nil {
					best = math.Max(best, cos(v, w))
				}
			}
			if best >= crossSim {
				cands = append(cands, scored{id, oid, best})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].sim > cands[j].sim || cands[i].sim == cands[j].sim && cands[i].id < cands[j].id
	})
	var ids []int64
	of := map[int64]int64{}
	for _, c := range cands {
		if len(ids) == crossMax {
			break
		}
		if _, dup := of[c.id]; dup || contains64(th.members, c.id) {
			continue
		}
		of[c.id] = c.thread
		ids = append(ids, c.id)
	}
	return ids, of
}

type assignOut struct {
	NewThreads []struct {
		Code  string `json:"code"`
		Name  string `json:"name"`
		Scope string `json:"scope"`
	} `json:"new_threads"`
	Groups []struct {
		Thread string  `json:"thread"`
		IDs    []int64 `json:"ids"`
	} `json:"groups"`
}

func (t *Threader) assign(ctx context.Context, it ThreadItem, msg string) (assignOut, error) {
	var out assignOut
	opt := threadOptions(assignSchema, assignMaxTokens)
	opt.Session = "threads " + it.ID
	opt.Effort = "low" // hosted: assigning needs little reasoning (measured, research log 2026-10-16)
	system := assignSystem
	if t.AuditFixes {
		system += assignFix
	}
	raw, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: msg}}, opt, nil)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(jsonPart(raw)), &out); err != nil {
		t.logf("threads: assign reply unreadable: %v", err)
	}
	return out, nil
}

type reconcileOut struct {
	Effects []struct {
		Later    int64  `json:"later"`
		Earlier  int64  `json:"earlier"`
		Relation string `json:"relation"`
	} `json:"effects"`
}

func (t *Threader) reconcile(ctx context.Context, it ThreadItem, th store.ThreadRow, cur, fresh []int64, known map[int64]store.Statement, byID map[int64]store.ItemStatement,
	cross []int64, crossOf map[int64]int64, threads map[int64]*threadState) (reconcileOut, error) {
	var out reconcileOut
	var old []string
	for _, id := range cur {
		if s, ok := known[id]; ok {
			old = append(old, itemLine(fromStatement(s), time.Time{}))
		}
	}
	if len(old) == 0 {
		old = []string{"(none: the thread starts today)"}
	}
	var neu []string
	for _, id := range fresh {
		neu = append(neu, itemLine(byID[id], it.Start))
	}
	related := ""
	if len(cross) > 0 {
		var rl []string
		for _, id := range cross {
			if s, ok := known[id]; ok {
				rl = append(rl, itemLine(fromStatement(s), time.Time{})+" [thread: "+threads[crossOf[id]].row.Name+"]")
			}
		}
		if len(rl) > 0 {
			related = "\n\nRelated items in other threads:\n" + strings.Join(rl, "\n")
		}
	}
	msg := fmt.Sprintf("Thread: %s — %s\n\nCurrent items:\n%s%s\n\nNew items from the %s meeting:\n%s",
		th.Name, th.Scope, strings.Join(old, "\n"), related, it.Start.Format("2006-01-02"), strings.Join(neu, "\n"))
	opt := threadOptions(reconcileSchema, reconcileMaxTokens)
	opt.Session = "threads " + it.ID
	for try := 0; try < 2; try++ {
		system := reconcileSystem
		if t.AuditFixes {
			system += reconcileFix
		}
		raw, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: msg}}, opt, nil)
		if err != nil {
			return out, err
		}
		// A reply cut off at the token cap (a decoding loop) doesn't parse;
		// sampling makes a second try differ.
		if err = json.Unmarshal([]byte(jsonPart(raw)), &out); err == nil {
			return out, nil
		}
		t.logf("threads: reconcile reply for %q unreadable: %v", th.Name, err)
	}
	return out, nil
}

// jsonPart trims anything around the JSON object (a reasoning trace, a
// code fence).
func jsonPart(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

// ThreadOrder sorts items for threading: oldest first, ties by id.
func ThreadOrder(items []ThreadItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Start.Equal(items[j].Start) {
			return items[i].Start.Before(items[j].Start)
		}
		return items[i].ID < items[j].ID
	})
}

// threadCallTimeout: an assign call writes a few thousand tokens; on the
// local 27B that is 100–150 s, past the provider's default.
const threadCallTimeout = 15 * time.Minute

// Replies are a few hundred (reconcile) to a few thousand (assign) tokens.
// The caps stop decoding loops early: at temperature 0 the local 27B
// looped to 16k tokens, 20 minutes a call.
const (
	assignMaxTokens    = 6000
	reconcileMaxTokens = 3000
)

// threadOptions samples as Qwen recommends for non-thinking mode, which
// avoids the loops greedy decoding falls into on long JSON.
func threadOptions(schema json.RawMessage, maxTokens int) provider.ChatOptions {
	return provider.ChatOptions{Schema: schema, Temperature: 0.7, TopP: 0.8, TopK: 20, MaxTokens: maxTokens, Timeout: threadCallTimeout}
}

// threadRule checks a reconcile verdict against what the kinds and timing
// allow; "" drops it. Measured on the linking benchmark (research-log.md,
// 2026-10-14): the local 27B called 84 pairs "completes", 70 of them on
// views and facts, and in-meeting "supersedes" without a correction were
// mostly elaborations; both hid current items.
// threadRuleV4 is threadRule with AuditFixes: "completes" also takes a
// position (the points extractor's opinion) or anything proposed.
func threadRuleV4(rel, earlierKind, earlierStatus string, sameItem bool, laterText string) string {
	if rel == "completes" && !sameItem && (earlierKind == "position" || earlierStatus == "proposed") {
		return rel
	}
	return threadRule(rel, earlierKind, sameItem, laterText)
}

func threadRule(rel, earlierKind string, sameItem bool, laterText string) string {
	switch rel {
	case "corrects":
		return "supersedes"
	case "completes":
		// Only a plan can be done, and not in the meeting that made it.
		if sameItem || (earlierKind != "commitment" && earlierKind != "decision" && earlierKind != "opinion") {
			return ""
		}
	case "supersedes":
		if sameItem && !correction(laterText) {
			return ""
		}
	}
	return rel
}

// mergeBorn (AuditFixes) asks, in one call, which threads born while
// threading this item are an older thread's subject, and folds their new
// statements into it before they are stored. Returns the touched threads.
func (t *Threader) mergeBorn(ctx context.Context, it ThreadItem, threads map[int64]*threadState, order []int64, byID map[int64]store.ItemStatement, known map[int64]store.Statement, stats *ThreadStats) ([]int64, error) {
	type cand struct {
		born int64
		near []int64
	}
	var cands []cand
	for _, b := range order {
		nb := threads[b]
		if !nb.born {
			continue
		}
		c := nb.centroid()
		if c == nil {
			continue
		}
		type scored struct {
			id  int64
			sim float64
		}
		var near []scored
		for id, o := range threads {
			if o.born || o.sum == nil {
				continue
			}
			oc := o.centroid()
			var d float64
			for i := range c {
				d += c[i] * oc[i]
			}
			if d >= mergeSim {
				near = append(near, scored{id, d})
			}
		}
		sort.Slice(near, func(i, j int) bool {
			return near[i].sim > near[j].sim || near[i].sim == near[j].sim && near[i].id < near[j].id
		})
		var ids []int64
		for i := 0; i < len(near) && i < 2; i++ {
			ids = append(ids, near[i].id)
		}
		if len(ids) > 0 {
			cands = append(cands, cand{b, ids})
		}
	}
	if len(cands) == 0 {
		return order, nil
	}
	show := func(id int64, items []string) string {
		th := threads[id]
		return fmt.Sprintf("T%d: %s — %s\n   - %s", id, th.row.Name, th.row.Scope, strings.Join(items, "\n   - "))
	}
	var parts []string
	for _, c := range cands {
		var mine []string
		for _, id := range threads[c.born].members[max(0, len(threads[c.born].members)-5):] {
			mine = append(mine, clip(byID[id].Text, 160))
		}
		var older []string
		for _, o := range c.near {
			var its []string
			for _, id := range threads[o].previous[max(0, len(threads[o].previous)-3):] {
				if s, ok := known[id]; ok {
					its = append(its, clip(s.Text, 160))
				}
			}
			older = append(older, show(o, its))
		}
		parts = append(parts, "New thread "+show(c.born, mine)+"\nSimilar older threads:\n"+strings.Join(older, "\n"))
	}
	opt := threadOptions(mergeSchema, reconcileMaxTokens)
	opt.Session = "threads " + it.ID
	raw, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: mergeSystem},
		{Role: "user", Content: strings.Join(parts, "\n\n") + "\n\nList the new threads that are the same subject as one of their older threads."}}, opt, nil)
	if err != nil {
		return order, err
	}
	stats.Calls++
	var out struct {
		Merges []struct{ New, Into string } `json:"merges"`
	}
	if json.Unmarshal([]byte(jsonPart(raw)), &out) != nil {
		return order, nil // no merges rather than a failed item
	}
	allowed := map[int64]map[int64]bool{}
	for _, c := range cands {
		allowed[c.born] = map[int64]bool{}
		for _, o := range c.near {
			allowed[c.born][o] = true
		}
	}
	gone := map[int64]int64{}
	for _, m := range out.Merges {
		var n, into int64
		if _, err := fmt.Sscanf(strings.TrimSpace(m.New), "T%d", &n); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(m.Into), "T%d", &into); err != nil {
			continue
		}
		if !allowed[n][into] || gone[n] != 0 {
			continue
		}
		src, dst := threads[n], threads[into]
		for _, id := range src.members {
			if !contains64(dst.members, id) {
				dst.members = append(dst.members, id)
				dst.add(t.Vecs[id])
			}
		}
		if err := t.Store.DeleteThread(n); err != nil {
			return order, err
		}
		delete(threads, n)
		gone[n] = into
		stats.NewThreads--
	}
	if len(gone) == 0 {
		return order, nil
	}
	var next []int64
	seen := map[int64]bool{}
	for _, id := range order {
		if into, ok := gone[id]; ok {
			id = into
		}
		if !seen[id] {
			seen[id] = true
			next = append(next, id)
		}
	}
	return next, nil
}

// resolveThread finds the thread an assign reply's group names: a new
// thread's code ("n2") or name, an existing thread's code ("T123", also
// written "T123: name" or "[T123]"), or an existing thread's name.
func resolveThread(ref string, local map[string]int64, threads map[int64]*threadState, byName map[string]int64) (int64, bool) {
	if tid, ok := local[ref]; ok {
		return tid, true
	}
	// "n15: name" or "T12 — name": the code before the name; "n8 + n7":
	// the first code.
	if i := strings.IndexAny(ref, ":—+,/"); i > 0 {
		if tid, ok := resolveThread(strings.TrimSpace(ref[:i]), local, threads, byName); ok {
			return tid, true
		}
	}
	if tid, ok := local[strings.ToLower(ref)]; ok {
		return tid, true
	}
	r := strings.TrimLeft(ref, "[(")
	var id int64
	if _, err := fmt.Sscanf(r, "T%d", &id); err == nil && threads[id] != nil {
		return id, true
	}
	if i := strings.Index(ref, ":"); i > 0 {
		if tid, ok := byName[strings.ToLower(strings.TrimSpace(ref[i+1:]))]; ok {
			return tid, true
		}
	}
	if tid, ok := byName[strings.ToLower(ref)]; ok {
		return tid, true
	}
	return 0, false
}
