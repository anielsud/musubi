package live

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// StmtOut is a statement found by meaning (a lab experiment: Live looks up
// passages).
type StmtOut struct {
	ID      int64   `json:"id"`
	Claim   int64   `json:"claim"`
	Kind    string  `json:"kind"`
	Status  string  `json:"status"`
	Holder  string  `json:"holder"`
	Date    string  `json:"date"`
	Text    string  `json:"text"`
	Quote   string  `json:"quote"`
	Item    string  `json:"item"`
	Title   string  `json:"title"`
	Seq     int     `json:"seq"`
	Worth   float64 `json:"worth"`
	Cos     float64 `json:"cos"`
	Overlap float64 `json:"overlap"` // share of the query's content words in the statement
	Score   float64 `json:"score"`
}

// Statements ranks statements by the query's embedding (cosine), with a
// small bonus for shared content words, as visible in the scope and said
// before `before` (zero: no limit). Statements that joined no claim are
// skipped: scope is applied through claims. boost adds to the score by kind
// (decision, commitment...), to try favouring some kinds.
func (b *Lab) Statements(ctx context.Context, query, scopeName string, k int, before time.Time, boost map[string]float64) ([]StmtOut, error) {
	eng := b.L.eng
	sc, err := eng.Scope(scopeName)
	if err != nil {
		return nil, err
	}
	if eng.Embedder == nil {
		return nil, fmt.Errorf("no embedder")
	}
	if b.svecs == nil {
		if b.svecs, err = eng.Store.StatementVecs(eng.Embedder.Model()); err != nil {
			return nil, err
		}
	}
	qs, err := eng.Embedder.Embed(ctx, []string{b.L.spellExpand(query)}, true)
	if err != nil || len(qs) == 0 || qs[0] == nil {
		return nil, fmt.Errorf("embedding the query: %v", err)
	}
	type sc2 struct {
		id  int64
		cos float64
	}
	all := make([]sc2, 0, len(b.svecs))
	for id, v := range b.svecs {
		all = append(all, sc2{id, cosine(qs[0], v)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].cos > all[j].cos })
	// Over-fetch: scope, date and claim-less statements remove some, and a
	// boost can lift a statement from further down.
	n := min(len(all), k*6+30)
	if len(boost) > 0 {
		n = min(len(all), k*20+100)
	}
	ids := make([]int64, n)
	cosOf := map[int64]float64{}
	for i := 0; i < n; i++ {
		ids[i] = all[i].id
		cosOf[all[i].id] = all[i].cos
	}
	sts, err := eng.Store.StatementsByID(ids)
	if err != nil {
		return nil, err
	}
	var claims []int64
	seen := map[int64]bool{}
	for _, s := range sts {
		if s.ClaimID != 0 && !seen[s.ClaimID] {
			seen[s.ClaimID] = true
			claims = append(claims, s.ClaimID)
		}
	}
	ev, err := eng.Store.Evidence(sc, claims)
	if err != nil {
		return nil, err
	}
	visible := map[int64]store.Statement{} // with the item title, which StatementsByID lacks
	for _, e := range ev {
		for _, s := range e.Statements {
			visible[s.ID] = s
		}
	}
	qw := contentWords(query)
	var out []StmtOut
	for _, id := range ids {
		s, ok := visible[id]
		if !ok || (!before.IsZero() && !s.SaidAt.Before(before)) {
			continue
		}
		ov := overlap(qw, contentWords(s.Text))
		out = append(out, StmtOut{ID: s.ID, Claim: s.ClaimID, Kind: s.Kind, Status: s.Status, Holder: s.Holder, Date: s.SaidAt.Format("2006-01-02"),
			Text: s.Text, Quote: s.Quote, Item: s.ItemID, Title: s.ItemTitle, Seq: s.ChunkSeq, Worth: s.Worth,
			Cos: cosOf[id], Overlap: ov, Score: cosOf[id] + 0.1*ov + boost[s.Kind]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// PoolOpt is one option in the pooled choice.
type PoolOpt struct {
	Key     string   `json:"key"`
	ID      int64    `json:"id,omitempty"` // statement id
	Kind    string   `json:"kind"`         // passage or statement
	Queries []string `json:"queries"`
	Text    string   `json:"text"`
	Item    string   `json:"item"`
	Seq     int      `json:"seq"`
	P       float64  `json:"p"`
	Pick    bool     `json:"pick"`
}

// PoolOut is the pooled choice: every option with its probability, the
// probability of "none", each query's best option, and what was sent.
type PoolOut struct {
	None    float64            `json:"none"`
	Min     float64            `json:"min"`
	Options []PoolOpt          `json:"options"`
	ByQuery map[string]float64 `json:"byQuery"`
	Sent    string             `json:"sent"`
}

// Pool searches every query, pools the passages (and optionally statements)
// and has the relevance classifier choose among them, so the queries are
// ranked by what they found rather than by the order the model wrote them.
func (b *Lab) Pool(ctx context.Context, title, chunk string, queries []string, scopeName string, perQuery, perQueryStmts, maxOpts, open int, before time.Time, question string, boost map[string]float64) (PoolOut, error) {
	var out PoolOut
	b.L.mu.Lock()
	rel := b.L.rel
	b.L.mu.Unlock()
	if rel == nil {
		return out, fmt.Errorf("live.relevance is off in this config")
	}
	if question == "" {
		question = poolQuestion
	}
	type cand struct {
		opt  PoolOpt
		rank float64
	}
	byKey := map[string]*cand{}
	var order []string
	add := func(key, kind, text, item string, seq int, id int64, q string, rank float64) {
		c := byKey[key]
		if c == nil {
			c = &cand{opt: PoolOpt{Key: key, ID: id, Kind: kind, Text: text, Item: item, Seq: seq}, rank: rank}
			byKey[key] = c
			order = append(order, key)
		}
		c.opt.Queries = append(c.opt.Queries, q)
		c.rank = math.Min(c.rank, rank)
	}
	sc, err := b.L.eng.Scope(scopeName)
	if err != nil {
		return out, err
	}
	for _, q := range queries {
		var hits []store.Hit
		if perQuery > 0 {
			if hits, err = b.L.eng.Search(ctx, sc, b.L.spellExpand(q), perQuery, nil); err != nil {
				return out, err
			}
		}
		for i, h := range hits {
			add(fmt.Sprintf("p:%s:%d", h.ItemID, h.Seq), "passage",
				fmt.Sprintf("%s (%s): %s", h.Title, h.OccurredAt.Format("2006-01-02"), excerpt(h.Text, 300)), h.ItemID, h.Seq, 0, q, float64(i))
		}
		if perQueryStmts > 0 {
			ss, err := b.Statements(ctx, q, scopeName, perQueryStmts, before, boost)
			if err != nil {
				return out, err
			}
			for i, s := range ss {
				add(fmt.Sprintf("s:%d", s.ID), "statement",
					fmt.Sprintf("%s, %s (%s, %s): %s", s.Title, s.Date, s.Holder, s.Kind, s.Text), s.Item, s.Seq, s.ID, q, float64(i))
			}
		}
	}
	// Best-ranked first, so the cut to maxOpts keeps each query's top finds.
	sort.SliceStable(order, func(i, j int) bool { return byKey[order[i]].rank < byKey[order[j]].rank })
	if len(order) > maxOpts {
		order = order[:maxOpts]
	}
	if len(order) == 0 {
		return out, nil
	}
	opts := map[string]string{"none": "None of these is useful in the conversation right now"}
	var sent strings.Builder
	for i, k := range order {
		key := fmt.Sprintf("o%d", i+1)
		c := byKey[k]
		opts[key] = c.opt.Text
		c.opt.Key = key
		fmt.Fprintf(&sent, "%s: %s\n", key, c.opt.Text)
	}
	a, err := rel.Classify(ctx, map[string]any{"meeting": title, "conversation": chunk},
		map[string]provider.Question{"best": {Type: "choice", Instructions: question, Options: opts}})
	if err != nil {
		return out, err
	}
	probs := a["best"].Probabilities
	out.None, out.Min, out.Sent = probs["none"], b.L.pickMin(), sent.String()
	out.ByQuery = map[string]float64{}
	for _, k := range order {
		c := byKey[k]
		c.opt.P = probs[c.opt.Key]
		for _, q := range c.opt.Queries {
			out.ByQuery[q] = math.Max(out.ByQuery[q], c.opt.P)
		}
		out.Options = append(out.Options, c.opt)
	}
	sort.SliceStable(out.Options, func(i, j int) bool { return out.Options[i].P > out.Options[j].P })
	picked := 0
	for i := range out.Options {
		if picked < open && out.Options[i].P >= out.Min {
			out.Options[i].Pick = true
			picked++
		}
	}
	return out, nil
}
