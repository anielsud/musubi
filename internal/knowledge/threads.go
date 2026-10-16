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

const assignSystem = `You organise a leadership team's meeting notes into threads, so that later someone can see the current state of any subject.

A thread is ONE specific subject that can have a state over time: an initiative, programme, deal, account, metric, decision, problem, launch, event, hire or departure. It is never a broad theme ("AI strategy", "marketing", "growth"): if a theme holds several specific subjects, those are separate threads. Two items that share a word, a customer name, an event or a number are not in the same thread unless they are about the same subject.

You get candidate threads (codes T…) and new items. Put every new item in a group: an existing thread's code, a new thread you define (codes n1, n2, … with a short, specific name and a one-line scope), or none (meeting logistics, greetings, jokes, small talk). An item may be in two groups only when it genuinely bears on two subjects.`

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
	// Vecs are statement vectors (for candidate threads); nil shows the
	// most recently active threads only.
	Vecs map[int64][]float32
	// Chunk is how many items one assign call reads (default 70);
	// Parallel how many threads are reconciled at once (default 4);
	// Candidates and StateCap override the bounds above.
	Chunk, Parallel, Candidates, StateCap int
	Logf                                  func(format string, args ...any)
}

// ThreadItem is an item to thread: its id, and when it happened (minutes
// in the prompts count from it).
type ThreadItem struct {
	ID    string
	Start time.Time
}

type ThreadStats struct {
	Items, Statements, NewThreads, Touched, Calls, Relations int
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
		res, err := t.assign(ctx, fmt.Sprintf("Candidate threads:\n%s\nNew items from the %s meeting:\n%s\n\nPut every new item in a group (by id).",
			reg, it.Start.Format("2006-01-02"), strings.Join(lines, "\n")))
		if err != nil {
			return stats, err
		}
		stats.Calls++
		// Outputs sometimes collapse (no or few items placed): ask again for
		// the items left out, with the threads named so far.
		for retry := 0; retry < 3; retry++ {
			got := map[int64]bool{}
			for _, g := range res.Groups {
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
			more, err := t.assign(ctx, fmt.Sprintf("Candidate threads:\n%s%s\n\nItems from the %s meeting still to place:\n%s\n\nPut every one of these items in a group (by id); new threads continue the n… numbering.",
				reg, strings.Join(named, "\n"), it.Start.Format("2006-01-02"), strings.Join(missing, "\n")))
			if err != nil {
				return stats, err
			}
			stats.Calls++
			res.NewThreads = append(res.NewThreads, more.NewThreads...)
			res.Groups = append(res.Groups, more.Groups...)
		}
		local := map[string]int64{} // n1 → thread
		for _, n := range res.NewThreads {
			code, name := strings.TrimSpace(n.Code), strings.TrimSpace(n.Name)
			if code == "" || name == "" {
				continue
			}
			if tid, ok := byName[strings.ToLower(name)]; ok {
				local[code] = tid
				continue
			}
			tid, err := t.Store.NewThread(name, n.Scope, 0)
			if err != nil {
				return stats, err
			}
			byName[strings.ToLower(name)] = tid
			threads[tid] = &threadState{row: store.ThreadRow{ID: tid, Name: name, Scope: n.Scope}, born: true}
			local[code] = tid
			stats.NewThreads++
		}
		inPart := map[int64]bool{}
		for _, s := range part {
			inPart[s.ID] = true
		}
		placed := map[int64]int{}
		for _, g := range res.Groups {
			ref := strings.TrimSpace(g.Thread)
			tid, ok := local[ref]
			if !ok {
				var id int64
				if _, err := fmt.Sscanf(ref, "T%d", &id); err == nil && threads[id] != nil {
					tid, ok = id, true
				}
			}
			if !ok {
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
	for _, tid := range order {
		th := threads[tid]
		var lvl sensitivity.Level
		for _, id := range th.members {
			lvl = max(lvl, byID[id].Level)
		}
		if err := t.Store.AddToThread(tid, th.members, lvl); err != nil {
			return stats, err
		}
	}
	stats.Touched = len(order)
	return stats, t.reconcileAll(ctx, it, order, threads, known, byID, &stats)
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
		tid int64
		out reconcileOut
		err error
	}
	jobs := make(chan int64)
	results := make(chan result)
	var wg sync.WaitGroup
	for w := 0; w < par; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tid := range jobs {
				th := threads[tid]
				shown := th.previous
				if len(shown) > capN {
					shown = shown[len(shown)-capN:]
				}
				out, err := t.reconcile(ctx, it, th.row, shown, th.members, known, byID)
				results <- result{tid, out, err}
			}
		}()
	}
	go func() {
		for _, tid := range order {
			jobs <- tid
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	var firstErr error
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
		shown := th.previous
		if len(shown) > capN {
			shown = shown[len(shown)-capN:]
		}
		valid := map[int64]bool{}
		for _, id := range shown {
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
				retired[e.Later] = true // the earlier wording stays
				continue
			}
			ek := known[e.Earlier].Kind
			if fresh[e.Earlier] {
				ek = byID[e.Earlier].Kind
			}
			rel := threadRule(e.Relation, ek, fresh[e.Earlier], byID[e.Later].Text)
			if rel == "" {
				continue
			}
			if err := t.Store.RelateStatements(e.Later, e.Earlier, rel); err != nil {
				return err
			}
			stats.Relations++
			if rel == "supersedes" || rel == "completes" || rel == "answers" {
				retired[e.Earlier] = true
			}
		}
		var now []int64
		for _, id := range append(append([]int64(nil), th.previous...), th.members...) {
			if !retired[id] {
				now = append(now, id)
			}
		}
		if err := t.Store.SetThreadCurrent(r.tid, now); err != nil {
			return err
		}
	}
	return firstErr
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

func (t *Threader) assign(ctx context.Context, msg string) (assignOut, error) {
	var out assignOut
	raw, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: assignSystem}, {Role: "user", Content: msg}},
		threadOptions(assignSchema, assignMaxTokens), nil)
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

func (t *Threader) reconcile(ctx context.Context, it ThreadItem, th store.ThreadRow, cur, fresh []int64, known map[int64]store.Statement, byID map[int64]store.ItemStatement) (reconcileOut, error) {
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
	msg := fmt.Sprintf("Thread: %s — %s\n\nCurrent items:\n%s\n\nNew items from the %s meeting:\n%s",
		th.Name, th.Scope, strings.Join(old, "\n"), it.Start.Format("2006-01-02"), strings.Join(neu, "\n"))
	for try := 0; try < 2; try++ {
		raw, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: reconcileSystem}, {Role: "user", Content: msg}},
			threadOptions(reconcileSchema, reconcileMaxTokens), nil)
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
