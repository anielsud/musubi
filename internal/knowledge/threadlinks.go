package knowledge

import (
	"sort"

	"github.com/anielsud/musubi/internal/store"
)

// LinkThreads gives threads what topic mode gives topics, without a model
// call: a centroid (where the map places it), the organisations and
// projects it involves, and its relations to other threads. Threading
// itself writes none of these, so without this step every thread sits at
// the same point on the map and has no related threads.
func LinkThreads(st *store.Store, entities func(string) []string) (int, error) {
	members, err := st.ThreadMembers()
	if err != nil {
		return 0, err
	}
	if len(members) == 0 {
		return 0, nil
	}
	claims, err := st.Claims()
	if err != nil {
		return 0, err
	}
	cen := map[int64][]float32{}
	for _, c := range claims {
		if len(c.Centroid) > 0 {
			cen[c.ID] = normalized(c.Centroid)
		}
	}
	var sids []int64
	for _, m := range members {
		sids = append(sids, m.Statement)
	}
	sts, err := st.StatementsByID(sids)
	if err != nil {
		return 0, err
	}

	type acc struct {
		claims map[int64]bool
		items  map[string]bool
		ents   map[string]int
		sum    []float32
	}
	threads := map[int64]*acc{}
	claimEnts := map[int64]map[string]bool{}
	for _, m := range members {
		a := threads[m.Topic]
		if a == nil {
			a = &acc{claims: map[int64]bool{}, items: map[string]bool{}, ents: map[string]int{}}
			threads[m.Topic] = a
		}
		a.items[sts[m.Statement].ItemID] = true
		if entities != nil {
			es := claimEnts[m.Claim]
			if es == nil {
				es = map[string]bool{}
				claimEnts[m.Claim] = es
			}
			for _, e := range entities(sts[m.Statement].Text) {
				es[e] = true
			}
		}
		if !a.claims[m.Claim] {
			a.claims[m.Claim] = true
			if v := cen[m.Claim]; v != nil {
				a.sum = add(a.sum, v)
			}
		}
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
	for _, id := range ids {
		a := threads[id]
		if a.sum != nil {
			links.Centroids[id] = normalized(a.sum)
		}
		links.Sizes[id] = len(a.claims)
		links.Entities[id] = a.ents
	}
	// Alike in meaning, or the same organisation or project. Unlike topic
	// mode, sharing meetings isn't enough: a recurring meeting's threads
	// all share it, and the pairs would grow with the square of the
	// threads. Discussion links are worked out when read (Views.Map).
	for i, x := range ids {
		for _, y := range ids[i+1:] {
			a, b := threads[x], threads[y]
			sim := 0.0
			if ca, cb := links.Centroids[x], links.Centroids[y]; ca != nil && cb != nil {
				sim = cos(ca, cb)
			}
			shared := 0
			for it := range a.items {
				if b.items[it] {
					shared++
				}
			}
			var common []string
			for e := range a.ents {
				if b.ents[e] > 0 {
					common = append(common, e)
				}
			}
			sort.Strings(common)
			if sim >= relateAt || len(common) > 0 {
				links.Relations = append(links.Relations, store.TopicRelation{A: x, B: y, Sim: sim, Shared: shared, Entities: common})
			}
		}
	}
	return len(ids), st.SetThreadLinks(links)
}
