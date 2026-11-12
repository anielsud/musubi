package knowledge

import "sort"

// NearbyTopic is a thread linked on the topic map to threads someone is
// in, though they aren't in it: From is the one of theirs it is closest to,
// Kind and Via why (as for a thread's related ones), Links how many of
// theirs it is linked to.
type NearbyTopic struct {
	ID    int64    `json:"id"`
	Label string   `json:"label"`
	Kind  string   `json:"kind"`
	Via   []string `json:"via,omitempty"`
	From  string   `json:"from"`
	Links int      `json:"links"`
}

// Nearby ranks the threads linked to own (and not in it) by how many of
// own they're linked to, then by how often they were discussed together;
// n at most. Similarity alone says little (scores bunch up), so a link
// counts when the two share statements, were discussed together and are
// alike, or name the same customer or project: one named in more than a
// twentieth of the threads' links (a vendor everyone uses, a word taken for
// a name) doesn't count. A thread whose label the scope can't see is left
// out.
func Nearby(m *TopicMap, own []int64, n int) []NearbyTopic {
	if m == nil {
		return nil
	}
	mine := map[int64]bool{}
	for _, id := range own {
		mine[id] = true
	}
	topics := map[int64]MapTopic{}
	for _, t := range m.Topics {
		topics[t.ID] = t
	}
	named := map[string]int{}
	for _, e := range m.Edges {
		for _, v := range e.Via {
			named[v]++
		}
	}
	common := max(len(m.Topics)/20, 3)
	type acc struct {
		NearbyTopic
		together int
		best     float64
	}
	found := map[int64]*acc{}
	for _, e := range m.Edges {
		from, other := e.A, e.B
		if mine[e.B] {
			from, other = e.B, e.A
		}
		if !mine[from] || mine[other] {
			continue
		}
		t, ok := topics[other]
		if !ok || t.Label == "" || t.Hidden {
			continue
		}
		var via []string
		for _, v := range e.Via {
			if named[v] <= common {
				via = append(via, v)
			}
		}
		kind := ""
		switch {
		case e.Shared > 0:
			kind = "shared"
		case len(via) > 0:
			kind = "same"
		case e.Together > 0 && e.Sim >= relateAt:
			kind = "together"
		default:
			continue
		}
		a := found[other]
		if a == nil {
			a = &acc{NearbyTopic: NearbyTopic{ID: other, Label: t.Label}}
			found[other] = a
		}
		a.Links++
		a.together += e.Together
		if w := float64(e.Shared*10+e.Together) + e.Sim; w > a.best {
			a.best, a.Kind, a.Via, a.From = w, kind, via, topics[from].Label
		}
	}
	all := make([]*acc, 0, len(found))
	for _, a := range found {
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Links != all[j].Links {
			return all[i].Links > all[j].Links
		}
		if all[i].together != all[j].together {
			return all[i].together > all[j].together
		}
		return all[i].ID > all[j].ID
	})
	var out []NearbyTopic
	for _, a := range all[:min(n, len(all))] {
		out = append(out, a.NearbyTopic)
	}
	return out
}
