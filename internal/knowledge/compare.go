package knowledge

import (
	"math"
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/store"
)

// Comparison lines up two datasets' topics: for each topic of A, the topics
// of B that hold the same material. Datasets built over the same items are
// matched by shared statements; datasets with different statements (another
// extraction) by wording.
type Comparison struct {
	Mode   string         `json:"mode"` // "statements" or "wording"
	Rows   []CompareRow   `json:"rows"`
	OnlyB  []CompareTopic `json:"onlyB"`
	Counts map[string]int `json:"counts"` // rows by status
	Sizes  map[string]int `json:"sizes"`  // topics in a, b
	Shared float64        `json:"shared"` // share of A's statements found in B ("statements" mode)
}

type CompareTopic struct {
	ID         int64  `json:"id"`
	Label      string `json:"label"`
	Statements int    `json:"statements"`
	Cluster    int    `json:"cluster"`
}

// CompareMatch: CoverA is the share of A's topic found in B's, CoverB the
// share of B's found in A's (in wording mode, both are the similarity).
type CompareMatch struct {
	B      CompareTopic `json:"b"`
	CoverA float64      `json:"coverA"`
	CoverB float64      `json:"coverB"`
}

// CompareRow: Status is "same" (one topic each, mostly the same material),
// "split" (A's topic spread over several of B's), "merged" (B holds it
// together with other topics of A: With names them), "partial" (a weaker
// match), or "only" (nothing in B).
type CompareRow struct {
	A       CompareTopic   `json:"a"`
	Matches []CompareMatch `json:"matches"`
	Status  string         `json:"status"`
	With    []string       `json:"with,omitempty"`
}

type sideTopics struct {
	topics []CompareTopic
	sts    map[int64][]store.Statement
}

func (v Views) sideTopics() (sideTopics, error) {
	m, err := v.Map()
	if err != nil {
		return sideTopics{}, err
	}
	d, err := v.loadTopics()
	if err != nil {
		return sideTopics{}, err
	}
	sts, err := v.topicStatements(d)
	if err != nil {
		return sideTopics{}, err
	}
	s := sideTopics{sts: sts}
	for _, t := range m.Topics {
		s.topics = append(s.topics, CompareTopic{ID: t.ID, Label: t.Label, Statements: t.Statements, Cluster: t.Cluster})
	}
	return s, nil
}

func statementKey(s store.Statement) string {
	return s.ItemID + "|" + strings.Join(strings.Fields(strings.ToLower(s.Text)), " ")
}

// CompareDatasets lines up a's topics with b's, both read under their own scope.
func CompareDatasets(a, b Views) (*Comparison, error) {
	sa, err := a.sideTopics()
	if err != nil {
		return nil, err
	}
	sb, err := b.sideTopics()
	if err != nil {
		return nil, err
	}
	keysA := map[int64]map[string]bool{}
	keysB := map[int64]map[string]bool{}
	allB := map[string]bool{}
	for id, ss := range sa.sts {
		keysA[id] = map[string]bool{}
		for _, s := range ss {
			keysA[id][statementKey(s)] = true
		}
	}
	for id, ss := range sb.sts {
		keysB[id] = map[string]bool{}
		for _, s := range ss {
			k := statementKey(s)
			keysB[id][k] = true
			allB[k] = true
		}
	}
	totalA, inB := 0, 0
	seenA := map[string]bool{}
	for _, ks := range keysA {
		for k := range ks {
			if !seenA[k] {
				seenA[k] = true
				totalA++
				if allB[k] {
					inB++
				}
			}
		}
	}
	c := &Comparison{Mode: "statements", Counts: map[string]int{}, Sizes: map[string]int{"a": len(sa.topics), "b": len(sb.topics)}}
	if totalA > 0 {
		c.Shared = float64(inB) / float64(totalA)
	}
	if c.Shared < 0.3 {
		c.Mode = "wording"
	}

	var wa, wb map[int64]map[string]float64
	if c.Mode == "wording" {
		wa, wb = tfidf(sa.sts, sb.sts)
	}
	score := func(x, y int64) (float64, float64) {
		if c.Mode == "statements" {
			ka, kb := keysA[x], keysB[y]
			if len(ka) == 0 || len(kb) == 0 {
				return 0, 0
			}
			n := 0
			for k := range ka {
				if kb[k] {
					n++
				}
			}
			return float64(n) / float64(len(ka)), float64(n) / float64(len(kb))
		}
		s := cosMap(wa[x], wb[y])
		return s, s
	}

	minCover := 0.15
	if c.Mode == "wording" {
		minCover = 0.25
	}
	bestOfB := map[int64]int64{} // B topic → the A topic it covers most
	bestScore := map[int64]float64{}
	matched := map[int64]bool{}
	for _, ta := range sa.topics {
		row := CompareRow{A: ta}
		for _, tb := range sb.topics {
			ca, cb := score(ta.ID, tb.ID)
			if ca < minCover && cb < minCover*2 {
				continue
			}
			row.Matches = append(row.Matches, CompareMatch{B: tb, CoverA: ca, CoverB: cb})
			if cb > bestScore[tb.ID] {
				bestScore[tb.ID], bestOfB[tb.ID] = cb, ta.ID
			}
		}
		sort.Slice(row.Matches, func(i, j int) bool { return row.Matches[i].CoverA > row.Matches[j].CoverA })
		if len(row.Matches) > 4 {
			row.Matches = row.Matches[:4]
		}
		for _, mt := range row.Matches {
			matched[mt.B.ID] = true
		}
		c.Rows = append(c.Rows, row)
	}
	labelA := map[int64]string{}
	for _, t := range sa.topics {
		labelA[t.ID] = t.Label
	}
	// Which A topics each B topic takes a real share of.
	takers := map[int64][]int64{}
	for _, r := range c.Rows {
		for _, mt := range r.Matches {
			if mt.CoverA >= 0.4 {
				takers[mt.B.ID] = append(takers[mt.B.ID], r.A.ID)
			}
		}
	}
	for i := range c.Rows {
		r := &c.Rows[i]
		big := 0
		for _, mt := range r.Matches {
			if mt.CoverA >= 0.25 {
				big++
			}
		}
		switch {
		case len(r.Matches) == 0:
			r.Status = "only"
		case big >= 2:
			r.Status = "split"
		case r.Matches[0].CoverA >= 0.4 && len(takers[r.Matches[0].B.ID]) >= 2:
			r.Status = "merged"
			for _, o := range takers[r.Matches[0].B.ID] {
				if o != r.A.ID {
					r.With = append(r.With, labelA[o])
				}
			}
		case r.Matches[0].CoverA >= 0.5 && r.Matches[0].CoverB >= 0.5:
			r.Status = "same"
		default:
			r.Status = "partial"
		}
		c.Counts[r.Status]++
	}
	for _, tb := range sb.topics {
		if !matched[tb.ID] {
			c.OnlyB = append(c.OnlyB, tb)
		}
	}
	c.Counts["onlyB"] = len(c.OnlyB)
	return c, nil
}

// tfidf weights each topic's words by how rare they are across both sides'
// topics, so shared vocabulary ("team", "customer") doesn't match
// everything.
func tfidf(a, b map[int64][]store.Statement) (map[int64]map[string]float64, map[int64]map[string]float64) {
	words := func(ss []store.Statement) map[string]float64 {
		tf := map[string]float64{}
		for _, s := range ss {
			for _, w := range strings.FieldsFunc(strings.ToLower(s.Text), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
				if len(w) >= 4 && !stopWords[w] {
					tf[w]++
				}
			}
		}
		return tf
	}
	ta, tb := map[int64]map[string]float64{}, map[int64]map[string]float64{}
	df := map[string]float64{}
	for id, ss := range a {
		ta[id] = words(ss)
		for w := range ta[id] {
			df[w]++
		}
	}
	for id, ss := range b {
		tb[id] = words(ss)
		for w := range tb[id] {
			df[w]++
		}
	}
	n := float64(len(a) + len(b))
	weigh := func(m map[int64]map[string]float64) {
		for _, tf := range m {
			for w, f := range tf {
				tf[w] = (1 + math.Log(f)) * math.Log(1+n/df[w])
			}
		}
	}
	weigh(ta)
	weigh(tb)
	return ta, tb
}

func cosMap(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var dot, na, nb float64
	for w, x := range a {
		na += x * x
		if y, ok := b[w]; ok {
			dot += x * y
		}
	}
	for _, y := range b {
		nb += y * y
	}
	return dot / math.Sqrt(na*nb)
}
