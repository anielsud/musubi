package live

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// Statement lookups (live.lookups, the default): every query finds its
// nearest statements, and the relevance classifier chooses one among all of
// them, or none. On 16 moments of two meetings, statements were rated useful
// five times as often as passages for the same queries; one choice across
// the queries showed a useful one at 6 moments where the passage path
// showed one at 1 (research log 2026-10-29).

const (
	stmtPerQuery = 5   // statements each query puts forward
	stmtOptions  = 12  // options the classifier chooses among
	stmtClaims   = 400 // nearest claims whose statements are scored
	// stmtWords: shared words count a little on top of similarity; the
	// boosts tried (kind, status, recency) didn't hold up across meetings.
	stmtWords = 0.1
)

const poolQuestion = "`conversation` is the last 90 seconds of a live meeting (speech recognition; \"You\" is the executive). The options are passages and notes from the executive's past meetings. Which would give them something they could use in what is being discussed right now: a fact, figure, decision, commitment or history? Choose \"none\" if no option does, because they are about something else, are small talk, only repeat what was just said, or are too vague to use. A shared word or name is not enough."

// statementLookups: the default, when there is a classifier to choose.
func (l *Live) statementLookups() bool {
	if l.eng.Cfg.Live.Lookups == "passages" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rel != nil
}

func (l *Live) lookupMaxNone() float64 {
	if m := l.eng.Cfg.Live.LookupMaxNone; m > 0 {
		return m
	}
	return 0.3
}

// stmtOption is a statement put forward, with the queries that found it.
type stmtOption struct {
	hit     store.StatementHit
	score   float64
	rank    int
	queries []string
}

// searchStatements is one query's nearest statements, best first.
func (l *Live) searchStatements(ctx context.Context, scopeName, query string, k int, exclude []string) ([]stmtOption, error) {
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return nil, err
	}
	if l.eng.Embedder == nil {
		return nil, fmt.Errorf("statement lookups need the embedder")
	}
	v := l.eng.Embed(ctx, l.spellExpand(query))
	if v == nil {
		return nil, fmt.Errorf("embedding failed")
	}
	hs, err := l.eng.Store.StatementsNear(sc, v, l.eng.Embedder.Model(), stmtClaims, exclude)
	if err != nil {
		return nil, err
	}
	qw := contentWords(query)
	out := make([]stmtOption, 0, len(hs))
	for _, h := range hs {
		out = append(out, stmtOption{hit: h, score: h.Sim + stmtWords*overlap(qw, contentWords(h.Text)), queries: []string{query}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > k {
		out = out[:k]
	}
	hs = make([]store.StatementHit, len(out))
	for i := range out {
		hs[i] = out[i].hit
	}
	if err := l.eng.Store.WithPassages(sc, hs); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].hit, out[i].rank = hs[i], i
	}
	return out, nil
}

func (o stmtOption) text() string {
	h := o.hit
	return fmt.Sprintf("%s, %s (%s): %s", h.ItemTitle, h.SaidAt.Format("2006-01-02"), h.Holder, h.Text)
}

// statementLookup searches every query's statements, has the classifier
// choose among them, and offers the choice for a place on the panel.
func (l *Live) statementLookup(ctx context.Context, cycle int, qs []Suggestion, exclude []string, scopeName string, lines []Segment) {
	l.mu.Lock()
	rel := l.rel
	var window, title string
	if l.meeting != nil {
		window, title = l.meeting.Recent(focusWindow), l.meeting.Session.Title
	}
	l.mu.Unlock()
	for _, s := range qs {
		s := s
		l.spawn(func() { l.lookupTopic(ctx, cycle, s) })
	}
	if rel == nil {
		return
	}
	t0 := time.Now()
	byID := map[int64]*stmtOption{}
	var order []int64
	for _, s := range qs {
		found, err := l.searchStatements(ctx, scopeName, s.Query, stmtPerQuery, exclude)
		if err != nil {
			l.trace(cycle, "error", "statement search failed: "+s.Query, time.Since(t0).Milliseconds(), err.Error())
			return
		}
		for _, o := range found {
			if have := byID[o.hit.ID]; have != nil {
				have.queries = append(have.queries, s.Query)
				have.rank = min(have.rank, o.rank)
				continue
			}
			o := o
			byID[o.hit.ID] = &o
			order = append(order, o.hit.ID)
		}
	}
	// Each query's best first, so the cut keeps every query's top finds.
	sort.SliceStable(order, func(i, j int) bool { return byID[order[i]].rank < byID[order[j]].rank })
	if len(order) > stmtOptions {
		order = order[:stmtOptions]
	}
	var found []map[string]any
	for _, id := range order {
		o := byID[id]
		found = append(found, map[string]any{"statement": id, "kind": o.hit.Kind, "sim": o.hit.Sim, "queries": o.queries, "text": excerpt(o.hit.Text, 200)})
	}
	l.trace(cycle, "search", fmt.Sprintf("statements: %d queries, %d options", len(qs), len(order)), time.Since(t0).Milliseconds(), found)
	if len(order) == 0 || strings.TrimSpace(window) == "" {
		return
	}
	opts := map[string]string{"none": "None of these is useful in the conversation right now"}
	for i, id := range order {
		opts[fmt.Sprintf("o%d", i+1)] = byID[id].text()
	}
	t1 := time.Now()
	a, err := rel.Classify(ctx, map[string]any{"meeting": title, "conversation": window},
		map[string]provider.Question{"best": {Type: "choice", Instructions: poolQuestion, Options: opts}})
	if err != nil {
		l.trace(cycle, "relevance", "statement choice failed", time.Since(t1).Milliseconds(), err.Error())
		return
	}
	probs := a["best"].Probabilities
	none, best, bestP := probs["none"], int64(0), -1.0
	for i, id := range order {
		if p := probs[fmt.Sprintf("o%d", i+1)]; p > bestP {
			best, bestP = id, p
		}
	}
	l.trace(cycle, "relevance", fmt.Sprintf("statement choice: none %.2f, best %.2f", none, bestP), time.Since(t1).Milliseconds(), probs)
	if none >= l.lookupMaxNone() || best == 0 {
		return
	}
	o := byID[best]
	q := o.queries[0]
	b := Brief{Query: q, Why: "model", Kind: "statement", State: "done", At: t0, SpokenAgo: -1,
		Summary: fmt.Sprintf("%s (%s, %s)", o.hit.Text, o.hit.Holder, o.hit.SaidAt.Format("Jan 2"))}
	if o.hit.Passage != nil {
		b.Hits = []store.Hit{*o.hit.Passage}
		b.Summary += " [1]"
	}
	if l.eng.Cfg.Live.Focus {
		b.Quote = evidenceLine(q, lines)
	}
	// The classifier's confidence that something fits stands in for the
	// relevance gate's score; the panel rescores it as the talk moves.
	l.finishBriefScored(cycle, b, evidenceAt(q, lines), "statement shown", "statements", 1-none)
}

// spellExpand applies the spelling rules to a query as brief does.
func (l *Live) spellExpand(q string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.spell.Expand(q)
}
