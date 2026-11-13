package store

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// ReadCache keeps the results of reads that Live repeats during a meeting
// (live.cache): every pick and card refresh rebuilt each card from its
// evidence and the whole topic table, and every named entity re-read its
// graph, although none of it changes while the meeting goes on.
//
// Two rules keep it from showing what it shouldn't:
//   - Each scoped result is kept under the whole scope it was read in
//     (name, level, categories, audience), so nothing read in private is
//     served on a shared screen.
//   - Everything is dropped when the database changes: each call first
//     reads KnowledgeSignature (constant time; it moves with any write,
//     this process's or another's), and a result is kept under the
//     signature read before it was loaded, so one loaded across a write is
//     dropped at the next call.
//
// Without a cache (live.cache off) callers read the store directly; the
// methods of a nil *ReadCache only return an error. Callers must not
// change what it returns.
type ReadCache struct {
	s  *Store
	mu sync.Mutex
	// sig is the signature everything below was read under.
	sig      string
	topics   map[int64]*TopicInfo
	graphs   map[string]*Graph
	statuses map[string]map[int64]ThreadStatusRow
	evidence map[string]map[int64]*ClaimEvidence
	// claimSts: a claim's statements visible in a scope (scope|claim);
	// vecs: a statement's vector (model|statement).
	claimSts map[string][]Statement
	vecs     map[string][]float32
}

// readCacheVecs bounds the statement vectors kept (3 KB each at 768
// dimensions, so about 50 MB); past it they are dropped and read again.
const readCacheVecs = 16000

// NewReadCache starts an empty cache over the store.
func (s *Store) NewReadCache() *ReadCache { return &ReadCache{s: s} }

func scopeKey(sc sensitivity.Scope) string { return fmt.Sprintf("%+v", sc) }

// fresh returns the current signature, dropping everything when it has
// moved. ok is false when it can't be read; the caller then reads straight
// from the store and keeps nothing.
func (c *ReadCache) fresh() (string, bool) {
	sig, err := c.s.KnowledgeSignature()
	if err != nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if sig != c.sig {
		c.sig = sig
		c.topics, c.graphs, c.statuses, c.evidence, c.claimSts, c.vecs = nil, nil, nil, nil, nil, nil
	}
	return sig, true
}

// TopicInfos is Store.TopicInfos. Labels are scope-checked by the caller,
// as there.
func (c *ReadCache) TopicInfos() (map[int64]*TopicInfo, error) {
	if c == nil {
		return nil, fmt.Errorf("no cache")
	}
	sig, ok := c.fresh()
	if ok {
		c.mu.Lock()
		t := c.topics
		c.mu.Unlock()
		if t != nil {
			return t, nil
		}
	}
	t, err := c.s.TopicInfos()
	if err != nil || !ok {
		return t, err
	}
	c.mu.Lock()
	if c.sig == sig {
		c.topics = t
	}
	c.mu.Unlock()
	return t, nil
}

// EntityGraph is Store.EntityGraph.
func (c *ReadCache) EntityGraph(sc sensitivity.Scope, entityID string, exclude []string, limit int) (*Graph, error) {
	if c == nil {
		return nil, fmt.Errorf("no cache")
	}
	key := fmt.Sprintf("%s|%s|%s|%d", scopeKey(sc), entityID, strings.Join(exclude, "\x00"), limit)
	sig, ok := c.fresh()
	if ok {
		c.mu.Lock()
		g, hit := c.graphs[key]
		c.mu.Unlock()
		if hit {
			return g, nil
		}
	}
	g, err := c.s.EntityGraph(sc, entityID, exclude, limit)
	if err != nil || !ok {
		return g, err
	}
	c.mu.Lock()
	if c.sig == sig {
		if c.graphs == nil {
			c.graphs = map[string]*Graph{}
		}
		c.graphs[key] = g
	}
	c.mu.Unlock()
	return g, nil
}

// ThreadStatuses is Store.ThreadStatuses.
func (c *ReadCache) ThreadStatuses(sc sensitivity.Scope) (map[int64]ThreadStatusRow, error) {
	if c == nil {
		return nil, fmt.Errorf("no cache")
	}
	key := scopeKey(sc)
	sig, ok := c.fresh()
	if ok {
		c.mu.Lock()
		m, hit := c.statuses[key]
		c.mu.Unlock()
		if hit {
			return m, nil
		}
	}
	m, err := c.s.ThreadStatuses(sc)
	if err != nil || !ok {
		return m, err
	}
	c.mu.Lock()
	if c.sig == sig {
		if c.statuses == nil {
			c.statuses = map[string]map[int64]ThreadStatusRow{}
		}
		c.statuses[key] = m
	}
	c.mu.Unlock()
	return m, nil
}

// Evidence is Store.Evidence, kept by scope and the exact set of claims (a
// card asks for the same set each time it is built).
func (c *ReadCache) Evidence(sc sensitivity.Scope, claimIDs []int64) (map[int64]*ClaimEvidence, error) {
	if c == nil {
		return nil, fmt.Errorf("no cache")
	}
	ids := slices.Clone(claimIDs)
	slices.Sort(ids)
	var b strings.Builder
	b.WriteString(scopeKey(sc))
	for _, id := range ids {
		fmt.Fprintf(&b, ",%d", id)
	}
	key := b.String()
	sig, ok := c.fresh()
	if ok {
		c.mu.Lock()
		ev, hit := c.evidence[key]
		c.mu.Unlock()
		if hit {
			return ev, nil
		}
	}
	ev, err := c.s.Evidence(sc, claimIDs)
	if err != nil || !ok {
		return ev, err
	}
	c.mu.Lock()
	if c.sig == sig {
		if c.evidence == nil {
			c.evidence = map[string]map[int64]*ClaimEvidence{}
		}
		c.evidence[key] = ev
	}
	c.mu.Unlock()
	return ev, nil
}

// StatementsNear is Store.StatementsNear with each claim's visible
// statements (by scope) and each statement's vector kept: a meeting's
// lookups land in the same neighbourhoods again and again.
func (c *ReadCache) StatementsNear(sc sensitivity.Scope, q []float32, model string, claims int, exclude []string) ([]StatementHit, error) {
	if c == nil {
		return nil, fmt.Errorf("no cache")
	}
	sig, ok := c.fresh()
	if !ok {
		return c.s.StatementsNear(sc, q, model, claims, exclude)
	}
	ix, err := c.s.ClaimIndex()
	if err != nil {
		return nil, err
	}
	near := ix.Nearest(q, claims)
	sk := scopeKey(sc) + "|"
	var sts []Statement
	var missing []int64
	c.mu.Lock()
	for _, id := range near {
		if got, hit := c.claimSts[fmt.Sprint(sk, id)]; hit {
			sts = append(sts, got...)
		} else {
			missing = append(missing, id)
		}
	}
	c.mu.Unlock()
	if len(missing) > 0 {
		more, err := c.s.visibleStatements(sc, missing)
		if err != nil {
			return nil, err
		}
		byClaim := make(map[int64][]Statement, len(missing))
		for _, st := range more {
			byClaim[st.ClaimID] = append(byClaim[st.ClaimID], st)
		}
		c.mu.Lock()
		if c.sig == sig {
			if c.claimSts == nil {
				c.claimSts = map[string][]Statement{}
			}
			for _, id := range missing {
				c.claimSts[fmt.Sprint(sk, id)] = byClaim[id] // nil: nothing visible
			}
		}
		c.mu.Unlock()
		sts = append(sts, more...)
	}
	skip := map[string]bool{}
	for _, x := range exclude {
		skip[x] = true
	}
	byID := map[int64]Statement{}
	var ids []int64
	for _, st := range sts {
		if _, dup := byID[st.ID]; !dup && !skip[st.ItemID] {
			ids = append(ids, st.ID)
			byID[st.ID] = st
		}
	}
	vecs := make(map[int64][]float32, len(ids))
	var want []int64
	c.mu.Lock()
	for _, id := range ids {
		if v, hit := c.vecs[fmt.Sprint(model, "|", id)]; hit {
			vecs[id] = v
		} else {
			want = append(want, id)
		}
	}
	c.mu.Unlock()
	if len(want) > 0 {
		got, err := c.s.statementVecsOf(want, model)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.sig == sig {
			if c.vecs == nil || len(c.vecs)+len(got) > readCacheVecs {
				c.vecs = make(map[string][]float32, len(got))
			}
			for id, v := range got {
				c.vecs[fmt.Sprint(model, "|", id)] = v
			}
		}
		c.mu.Unlock()
		for id, v := range got {
			vecs[id] = v
		}
	}
	out := make([]StatementHit, 0, len(vecs))
	for id, v := range vecs {
		out = append(out, StatementHit{Statement: byID[id], Sim: cosine(q, v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sim > out[j].Sim })
	return out, nil
}
