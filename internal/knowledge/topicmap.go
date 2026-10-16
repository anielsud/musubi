package knowledge

import (
	"math"
	"sort"
	"time"

	"github.com/anielsud/musubi/internal/store"
)

// TopicMap is everything the topic views draw: the topics, the links
// between them, the clusters those links form, and when each topic was
// discussed. Built when read, from what the scope can see.
type TopicMap struct {
	Topics   []MapTopic     `json:"topics"`
	Edges    []TopicEdge    `json:"edges"`
	Clusters []TopicCluster `json:"clusters"`
	// Items are the meetings (and other sources) topics were discussed in,
	// oldest first.
	Items []store.ItemRef `json:"items"`
	// Meaning: topics have centroids, so the meaning layout (X, Y) means
	// something. Imported items without vectors have none.
	Meaning bool `json:"meaning"`
}

type MapTopic struct {
	TopicSummary
	Cluster    int             `json:"cluster"` // -1: linked to nothing
	Statements int             `json:"statements"`
	Activity   []TopicActivity `json:"activity"`
}

type TopicActivity struct {
	Item string `json:"item"`
	N    int    `json:"n"`
}

// TopicEdge links two topics. Kind is the strongest reason: "shared" (the
// same statements belong to both), "together" (discussed back to back),
// "same" (the same customer or project), "similar" (alike in meaning).
type TopicEdge struct {
	A        int64    `json:"a"`
	B        int64    `json:"b"`
	W        float64  `json:"w"`
	Kind     string   `json:"kind"`
	Shared   int      `json:"shared"`
	Together int      `json:"together"`
	Sim      float64  `json:"sim"`
	Via      []string `json:"via,omitempty"`
}

type TopicCluster struct {
	ID         int       `json:"id"`
	Label      string    `json:"label"`
	Topics     []int64   `json:"topics"` // largest first
	Statements int       `json:"statements"`
	Last       time.Time `json:"last"`
}

const (
	edgesPerTopic = 5               // a topic keeps its strongest links
	togetherGap   = 5 * time.Minute // statements further apart weren't one discussion
)

// topicStatements returns each visible topic's visible statements. In
// thread mode a statement can belong to several threads.
func (v Views) topicStatements(ts []TopicSummary) (map[int64][]store.Statement, error) {
	rows, err := v.Store.Topics()
	if err != nil {
		return nil, err
	}
	visible := map[int64]bool{}
	for _, t := range ts {
		visible[t.ID] = true
	}
	members, err := v.Store.ThreadMembers()
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, r := range rows {
		ids = append(ids, r.Claims...)
	}
	for _, m := range members {
		ids = append(ids, m.Claim)
	}
	ev, err := v.Store.Evidence(v.Scope, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64][]store.Statement{}
	if len(members) > 0 {
		byID := map[int64]store.Statement{}
		for _, e := range ev {
			for _, s := range e.Statements {
				byID[s.ID] = s
			}
		}
		for _, m := range members {
			if s, ok := byID[m.Statement]; ok && visible[m.Topic] {
				out[m.Topic] = append(out[m.Topic], s)
			}
		}
		return out, nil
	}
	for _, r := range rows {
		if !visible[r.ID] {
			continue
		}
		for _, c := range r.Claims {
			if e := ev[c]; e != nil {
				out[r.ID] = append(out[r.ID], e.Statements...)
			}
		}
	}
	return out, nil
}

// Map builds the topic map.
func (v Views) Map() (*TopicMap, error) {
	ts, err := v.Topics()
	if err != nil {
		return nil, err
	}
	sts, err := v.topicStatements(ts)
	if err != nil {
		return nil, err
	}
	m := &TopicMap{}
	idx := map[int64]int{}
	labels := map[int64]string{}
	itemSet := map[string]bool{}
	for i, t := range ts {
		idx[t.ID] = i
		labels[t.ID] = t.Label
		mt := MapTopic{TopicSummary: t, Statements: len(sts[t.ID])}
		per := map[string]int{}
		for _, s := range sts[t.ID] {
			per[s.ItemID]++
			itemSet[s.ItemID] = true
		}
		for it, n := range per {
			mt.Activity = append(mt.Activity, TopicActivity{Item: it, N: n})
		}
		m.Topics = append(m.Topics, mt)
		if t.X != 0 || t.Y != 0 {
			m.Meaning = true
		}
	}

	// Discussed back to back: in each item, a statement and the two after
	// it, within a few minutes, in different topics.
	key := func(a, b int64) pair {
		if a > b {
			a, b = b, a
		}
		return pair{a, b}
	}
	topicsOf := map[int64][]int64{}
	byItem := map[string][]store.Statement{}
	seen := map[int64]bool{}
	for tid, ss := range sts {
		for _, s := range ss {
			topicsOf[s.ID] = append(topicsOf[s.ID], tid)
			if !seen[s.ID] {
				seen[s.ID] = true
				byItem[s.ItemID] = append(byItem[s.ItemID], s)
			}
		}
	}
	together := map[pair]int{}
	shared := map[pair]int{}
	for _, ss := range byItem {
		sort.Slice(ss, func(i, j int) bool {
			if !ss[i].SaidAt.Equal(ss[j].SaidAt) {
				return ss[i].SaidAt.Before(ss[j].SaidAt)
			}
			return ss[i].ID < ss[j].ID
		})
		for i, s := range ss {
			tsA := topicsOf[s.ID]
			for x := 0; x < len(tsA); x++ {
				for y := x + 1; y < len(tsA); y++ {
					shared[key(tsA[x], tsA[y])]++
				}
			}
			for j := i + 1; j < len(ss) && j <= i+2; j++ {
				if ss[j].SaidAt.Sub(s.SaidAt) > togetherGap {
					break
				}
				counted := map[pair]bool{}
				for _, a := range tsA {
					for _, b := range topicsOf[ss[j].ID] {
						if a != b && !counted[key(a, b)] {
							counted[key(a, b)] = true
							together[key(a, b)]++
						}
					}
				}
			}
		}
	}

	// Stored relations add shared organisations and projects, and meaning.
	infos, err := v.Store.TopicInfos()
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	entsLoaded := map[int64]bool{}
	entityName := func(tid int64, id string) (string, bool) {
		if !entsLoaded[tid] {
			entsLoaded[tid] = true
			es, err := v.Store.TopicEntities(tid)
			if err == nil {
				for _, e := range es {
					if labelVisible(v.Scope, e.Level) {
						names[e.ID] = e.Name
					}
				}
			}
		}
		n, ok := names[id]
		return n, ok
	}
	sims := map[pair]float64{}
	vias := map[pair][]string{}
	for a, info := range infos {
		if _, ok := idx[a]; !ok {
			continue
		}
		for _, r := range info.Related {
			if _, ok := idx[r.B]; !ok || r.A > r.B {
				continue
			}
			k := key(r.A, r.B)
			sims[k] = r.Sim
			for _, e := range r.Entities {
				if n, ok := entityName(r.A, e); ok {
					vias[k] = append(vias[k], n)
				}
			}
		}
	}

	size := func(id int64) float64 { return math.Max(1, float64(len(sts[id]))) }
	cand := map[pair]*TopicEdge{}
	get := func(k pair) *TopicEdge {
		e := cand[k]
		if e == nil {
			e = &TopicEdge{A: k.a, B: k.b}
			cand[k] = e
		}
		return e
	}
	for k, n := range shared {
		get(k).Shared = n
	}
	for k, n := range together {
		get(k).Together = n
	}
	// Meaning counts only for the most alike pairs of this dataset: one
	// organisation's topics all read alike, so a fixed threshold links
	// nearly everything.
	floor := simFloor(sims)
	for k, s := range sims {
		if s >= floor {
			get(k).Sim = s
		}
	}
	for k, vs := range vias {
		get(k).Via = vs
	}
	for k, e := range cand {
		if e.Sim == 0 {
			e.Sim = sims[k]
		}
		// Counts are scaled by the topics' sizes, so a catch-all topic
		// that touches everything doesn't pull everything to itself.
		norm := 10 / math.Sqrt(size(k.a)*size(k.b))
		parts := map[string]float64{
			"shared":   2 * float64(e.Shared) * norm,
			"together": float64(e.Together) * norm,
			"same":     1.5 * float64(len(e.Via)),
		}
		if e.Sim >= floor {
			parts["similar"] = 1 + (e.Sim-floor)*20
		}
		best := 0.0
		for _, kind := range []string{"shared", "together", "same", "similar"} {
			e.W += parts[kind]
			if parts[kind] > best {
				best, e.Kind = parts[kind], kind
			}
		}
		// One back-to-back moment is chance, not a link.
		if e.Shared == 0 && len(e.Via) == 0 && e.Sim < floor && e.Together < 2 {
			delete(cand, k)
		}
	}
	perTopic := map[int64][]*TopicEdge{}
	for _, e := range cand {
		perTopic[e.A] = append(perTopic[e.A], e)
		perTopic[e.B] = append(perTopic[e.B], e)
	}
	keep := map[*TopicEdge]bool{}
	for _, es := range perTopic {
		sort.Slice(es, func(i, j int) bool {
			if es[i].W != es[j].W {
				return es[i].W > es[j].W
			}
			return es[i].A*1e6+es[i].B < es[j].A*1e6+es[j].B
		})
		for i := 0; i < len(es) && i < edgesPerTopic; i++ {
			keep[es[i]] = true
		}
	}
	for e := range keep {
		m.Edges = append(m.Edges, *e)
	}
	sort.Slice(m.Edges, func(i, j int) bool {
		if m.Edges[i].W != m.Edges[j].W {
			return m.Edges[i].W > m.Edges[j].W
		}
		return m.Edges[i].A*1e6+m.Edges[i].B < m.Edges[j].A*1e6+m.Edges[j].B
	})

	// Clusters: communities of the link graph.
	var wedges []wedge
	for _, e := range m.Edges {
		wedges = append(wedges, wedge{idx[e.A], idx[e.B], e.W})
	}
	comm := louvain(len(ts), wedges)
	linked := map[int]bool{}
	for _, e := range wedges {
		linked[e.a], linked[e.b] = true, true
	}
	groups := map[int][]int{}
	for i, c := range comm {
		if linked[i] {
			groups[c] = append(groups[c], i)
		}
	}
	var cl []TopicCluster
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool {
			if m.Topics[g[i]].Statements != m.Topics[g[j]].Statements {
				return m.Topics[g[i]].Statements > m.Topics[g[j]].Statements
			}
			return m.Topics[g[i]].ID < m.Topics[g[j]].ID
		})
		c := TopicCluster{Label: m.Topics[g[0]].Label}
		for _, i := range g {
			c.Topics = append(c.Topics, m.Topics[i].ID)
			c.Statements += m.Topics[i].Statements
			if m.Topics[i].Last.After(c.Last) {
				c.Last = m.Topics[i].Last
			}
		}
		cl = append(cl, c)
	}
	sort.Slice(cl, func(i, j int) bool {
		if cl[i].Statements != cl[j].Statements {
			return cl[i].Statements > cl[j].Statements
		}
		return cl[i].Topics[0] < cl[j].Topics[0]
	})
	for i := range m.Topics {
		m.Topics[i].Cluster = -1
	}
	for ci := range cl {
		cl[ci].ID = ci
		for _, id := range cl[ci].Topics {
			m.Topics[idx[id]].Cluster = ci
		}
	}
	m.Clusters = cl

	var items []string
	for it := range itemSet {
		items = append(items, it)
	}
	refs, err := v.Store.ItemTitles(v.Scope, items)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		m.Items = append(m.Items, r)
	}
	sort.Slice(m.Items, func(i, j int) bool {
		if !m.Items[i].OccurredAt.Equal(m.Items[j].OccurredAt) {
			return m.Items[i].OccurredAt.Before(m.Items[j].OccurredAt)
		}
		return m.Items[i].ID < m.Items[j].ID
	})
	return m, nil
}

// simFloor is the similarity above which a pair counts as alike: the top
// fifth of the stored pairs (themselves the pairs above relateAt), and never
// below relateAt.
func simFloor(sims map[pair]float64) float64 {
	var xs []float64
	for _, s := range sims {
		xs = append(xs, s)
	}
	if len(xs) < 5 {
		return relateAt
	}
	sort.Float64s(xs)
	return math.Max(relateAt, xs[len(xs)*4/5])
}

type pair struct{ a, b int64 }

type wedge struct {
	a, b int
	w    float64
}

// louvain groups a weighted graph's nodes into communities by modularity
// (local moves, then the communities as nodes, until nothing moves).
// Deterministic: nodes are visited in order.
func louvain(n int, edges []wedge) []int {
	comm := make([]int, n)
	for i := range comm {
		comm[i] = i
	}
	if len(edges) == 0 {
		return comm
	}
	nodes := n
	cur := edges
	member := make([]int, n) // original node → current super-node
	for i := range member {
		member[i] = i
	}
	for level := 0; level < 10; level++ {
		adj := make([]map[int]float64, nodes)
		for i := range adj {
			adj[i] = map[int]float64{}
		}
		k := make([]float64, nodes)
		m2 := 0.0
		for _, e := range cur {
			if e.a == e.b {
				adj[e.a][e.a] += e.w
				k[e.a] += 2 * e.w
				m2 += 2 * e.w
				continue
			}
			adj[e.a][e.b] += e.w
			adj[e.b][e.a] += e.w
			k[e.a] += e.w
			k[e.b] += e.w
			m2 += 2 * e.w
		}
		c := make([]int, nodes)
		tot := make([]float64, nodes)
		for i := range c {
			c[i] = i
			tot[i] = k[i]
		}
		moved := false
		for pass := 0; pass < 50; pass++ {
			changed := false
			for i := 0; i < nodes; i++ {
				old := c[i]
				tot[old] -= k[i]
				in := map[int]float64{}
				var order []int
				for j, w := range adj[i] {
					if j == i {
						continue
					}
					if _, ok := in[c[j]]; !ok {
						order = append(order, c[j])
					}
					in[c[j]] += w
				}
				sort.Ints(order)
				best, gain := old, in[old]-tot[old]*k[i]/m2
				for _, cc := range order {
					if g := in[cc] - tot[cc]*k[i]/m2; g > gain+1e-12 {
						best, gain = cc, g
					}
				}
				c[i] = best
				tot[best] += k[i]
				if best != old {
					changed, moved = true, true
				}
			}
			if !changed {
				break
			}
		}
		if !moved {
			break
		}
		renum := map[int]int{}
		for i := 0; i < nodes; i++ {
			if _, ok := renum[c[i]]; !ok {
				renum[c[i]] = len(renum)
			}
		}
		for o := range member {
			member[o] = renum[c[member[o]]]
		}
		agg := map[[2]int]float64{}
		for _, e := range cur {
			a, b := renum[c[e.a]], renum[c[e.b]]
			if a > b {
				a, b = b, a
			}
			agg[[2]int{a, b}] += e.w
		}
		cur = cur[:0:0]
		for ab, w := range agg {
			cur = append(cur, wedge{ab[0], ab[1], w})
		}
		sort.Slice(cur, func(i, j int) bool {
			if cur[i].a != cur[j].a {
				return cur[i].a < cur[j].a
			}
			return cur[i].b < cur[j].b
		})
		nodes = len(renum)
	}
	copy(comm, member)
	return comm
}
