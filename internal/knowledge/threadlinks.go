package knowledge

import (
	"sort"

	"github.com/anielsud/musubi/internal/store"
	"github.com/anielsud/musubi/internal/vindex"
)

// linkNeighbours is how many nearest threads (by centroid) each thread is
// compared with; pairs naming the same organisation or project are
// compared too. With fewer threads than this, every pair is compared.
const linkNeighbours = 50

// sharedEntityCap: an organisation or project named by more threads than
// this (the company itself, a platform everyone mentions) relates only
// threads that are also near each other: otherwise its pairs alone would
// grow with the square of the threads.
const sharedEntityCap = 100

// LinkThreads gives threads what topic mode gives topics, without a model
// call: a centroid (where the map places it), the organisations and
// projects it involves, and its relations to other threads. Threading
// itself writes none of these, so without this step every thread sits at
// the same point on the map and has no related threads.
//
// It reads claim centroids one at a time and compares each thread with its
// nearest neighbours rather than every other thread, so its time and
// memory grow with the corpus rather than its square (scaling.md, §3).
func LinkThreads(st *store.Store, entities func(string) []string) (int, error) {
	members, err := st.ThreadMembers()
	if err != nil {
		return 0, err
	}
	if len(members) == 0 {
		return 0, nil
	}
	type acc struct {
		claims map[int64]bool
		items  map[string]bool
		ents   map[string]int
		sum    []float32
	}
	threads := map[int64]*acc{}
	threadsOf := map[int64][]int64{} // claim → threads
	stmtThreads := map[int64][]int64{}
	for _, m := range members {
		a := threads[m.Topic]
		if a == nil {
			a = &acc{claims: map[int64]bool{}, items: map[string]bool{}, ents: map[string]int{}}
			threads[m.Topic] = a
		}
		if !a.claims[m.Claim] {
			a.claims[m.Claim] = true
			threadsOf[m.Claim] = append(threadsOf[m.Claim], m.Topic)
		}
		stmtThreads[m.Statement] = append(stmtThreads[m.Statement], m.Topic)
	}
	// Items and entities, statement by statement.
	claimEnts := map[int64]map[string]bool{}
	stmtClaim := map[int64]int64{}
	for _, m := range members {
		stmtClaim[m.Statement] = m.Claim
	}
	err = st.EachThreadStatement(func(id int64, itemID, text string) {
		for _, t := range stmtThreads[id] {
			threads[t].items[itemID] = true
		}
		if entities != nil {
			c := stmtClaim[id]
			es := claimEnts[c]
			if es == nil {
				es = map[string]bool{}
				claimEnts[c] = es
			}
			for _, e := range entities(text) {
				es[e] = true
			}
		}
	})
	if err != nil {
		return 0, err
	}
	err = st.EachClaimCentroid(func(id int64, v []float32) {
		if len(threadsOf[id]) == 0 || len(v) == 0 {
			return
		}
		n := normalized(v)
		for _, t := range threadsOf[id] {
			threads[t].sum = add(threads[t].sum, n)
		}
	})
	if err != nil {
		return 0, err
	}
	var ids []int64
	for id, a := range threads {
		ids = append(ids, id)
		for c := range a.claims {
			for e := range claimEnts[c] {
				a.ents[e]++
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	links := store.ThreadLinks{Centroids: map[int64][]float32{}, Sizes: map[int64]int{}, Entities: map[int64]map[string]int{}}
	ix := (*vindex.Index)(nil)
	for _, id := range ids {
		a := threads[id]
		if a.sum != nil {
			c := normalized(a.sum)
			links.Centroids[id] = c
			if ix == nil {
				ix = vindex.New(len(c))
			}
			ix.Add(id, vindex.Code(c))
		}
		links.Sizes[id] = len(a.claims)
		links.Entities[id] = a.ents
	}
	// Candidate pairs: nearest neighbours by centroid, and threads naming
	// the same (not ubiquitous) organisation or project.
	type pair struct{ a, b int64 }
	cand := map[pair]bool{}
	addPair := func(x, y int64) {
		if x == y {
			return
		}
		if x > y {
			x, y = y, x
		}
		cand[pair{x, y}] = true
	}
	for _, id := range ids {
		if c := links.Centroids[id]; c != nil {
			for _, o := range ix.Nearest(c, linkNeighbours+1) {
				addPair(id, o)
			}
		}
	}
	byEnt := map[string][]int64{}
	for _, id := range ids {
		for e := range threads[id].ents {
			byEnt[e] = append(byEnt[e], id)
		}
	}
	for _, ts := range byEnt {
		if len(ts) > sharedEntityCap {
			continue
		}
		for i, x := range ts {
			for _, y := range ts[i+1:] {
				addPair(x, y)
			}
		}
	}
	// Alike in meaning, or the same organisation or project. Unlike topic
	// mode, sharing meetings isn't enough: a recurring meeting's threads
	// all share it, and the pairs would grow with the square of the
	// threads. Discussion links are worked out when read (Views.Map).
	var pairs []pair
	for p := range cand {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].a < pairs[j].a || pairs[i].a == pairs[j].a && pairs[i].b < pairs[j].b
	})
	for _, p := range pairs {
		a, b := threads[p.a], threads[p.b]
		sim := 0.0
		if ca, cb := links.Centroids[p.a], links.Centroids[p.b]; ca != nil && cb != nil {
			sim = cos(ca, cb)
		}
		var common []string
		for e := range a.ents {
			if b.ents[e] > 0 && len(byEnt[e]) <= sharedEntityCap {
				common = append(common, e)
			}
		}
		if sim < relateAt && len(common) == 0 {
			continue
		}
		sort.Strings(common)
		shared := 0
		for it := range a.items {
			if b.items[it] {
				shared++
			}
		}
		links.Relations = append(links.Relations, store.TopicRelation{A: p.a, B: p.b, Sim: sim, Shared: shared, Entities: common})
	}
	return len(ids), st.SetThreadLinks(links)
}
