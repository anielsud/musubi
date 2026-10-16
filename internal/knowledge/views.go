package knowledge

import (
	"math"
	"sort"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Views reads the knowledge layer under a scope. Every view is built from
// store.Evidence, which only returns statements visible in scope; belief,
// counts and labels are computed from those alone.
type Views struct {
	Store *store.Store
	Scope sensitivity.Scope
	Now   time.Time
}

type RelView struct {
	Rel       string `json:"rel"`       // contradicts, refines, supersedes, answers
	Direction string `json:"direction"` // out: this claim → other; in: other → this
	ClaimID   int64  `json:"claimId"`
	Text      string `json:"text"`
	Status    string `json:"status"`
}

type ClaimView struct {
	ID         int64             `json:"id"`
	Kind       string            `json:"kind"`
	Text       string            `json:"text"`
	Holder     string            `json:"holder"`
	TopicID    int64             `json:"topicId"`
	First      time.Time         `json:"first"`
	Belief     Belief            `json:"belief"`
	Statements []store.Statement `json:"statements"`
	Relations  []RelView         `json:"relations"`
}

// Claims returns the claims visible in scope, in the order given.
func (v Views) Claims(ids []int64) ([]ClaimView, error) {
	ev, err := v.Store.Evidence(v.Scope, ids)
	if err != nil {
		return nil, err
	}
	var out []ClaimView
	for _, id := range ids {
		e := ev[id]
		if e == nil {
			continue
		}
		out = append(out, v.view(e, ev))
	}
	return out, nil
}

func (v Views) view(e *store.ClaimEvidence, all map[int64]*store.ClaimEvidence) ClaimView {
	cv := ClaimView{ID: e.ClaimID, Kind: e.Kind, TopicID: e.TopicID, Statements: e.Statements, Belief: Compute(e, v.Now)}
	if len(e.Statements) > 0 {
		// The claim reads as its latest visible statement: what is
		// currently said, in the most recent words.
		last := e.Statements[len(e.Statements)-1]
		cv.Text, cv.Holder, cv.First = last.Text, last.Holder, e.Statements[0].SaidAt
	}
	for _, r := range e.Relations {
		other, dir := r.To, "out"
		if r.To == e.ClaimID {
			other, dir = r.From, "in"
		}
		sts := e.Related[other]
		if len(sts) == 0 {
			continue
		}
		rv := RelView{Rel: r.Rel, Direction: dir, ClaimID: other, Text: sts[len(sts)-1].Text}
		if o := all[other]; o != nil {
			rv.Status = Compute(o, v.Now).Status
		}
		cv.Relations = append(cv.Relations, rv)
	}
	return cv
}

// Claim returns one claim, or nil if not visible.
func (v Views) Claim(id int64) (*ClaimView, error) {
	ev, err := v.Store.Evidence(v.Scope, []int64{id})
	if err != nil {
		return nil, err
	}
	e := ev[id]
	if e == nil {
		return nil, nil
	}
	// Related claims' own evidence, for their status.
	var rel []int64
	for _, r := range e.Relations {
		rel = append(rel, r.From, r.To)
	}
	more, err := v.Store.Evidence(v.Scope, rel)
	if err != nil {
		return nil, err
	}
	more[id] = e
	cv := v.view(e, more)
	return &cv, nil
}

// SearchClaims finds visible claims whose statements contain the words.
func (v Views) SearchClaims(text string, limit int) ([]ClaimView, error) {
	ids, err := v.Store.SearchStatements(text, limit*3)
	if err != nil {
		return nil, err
	}
	cs, err := v.Claims(ids)
	if len(cs) > limit {
		cs = cs[:limit]
	}
	return cs, err
}

type TopicSummary struct {
	ID       int64          `json:"id"`
	Label    string         `json:"label"`
	Summary  string         `json:"summary"`
	Keywords []string       `json:"keywords"`
	Hidden   bool           `json:"labelHidden"` // label written from material this scope can't see
	Claims   int            `json:"claims"`
	Statuses map[string]int `json:"statuses"`
	Last     time.Time      `json:"last"`
	X        float64        `json:"x"`
	Y        float64        `json:"y"`
}

type RelatedTopic struct {
	ID     int64   `json:"id"`
	Label  string  `json:"label"`
	Sim    float64 `json:"sim"`
	Shared int     `json:"shared"`
	// Kind: "shared" (some statements belong to both), "same" (shares a
	// customer or project: Via names it), "together" (discussed back to
	// back; Shared counts the moments), "parallel" (alike in
	// meaning, about different customers or projects), "similar" (alike in
	// meaning).
	Kind        string   `json:"kind"`
	Via         []string `json:"via,omitempty"`
	Unconnected bool     `json:"unconnected"` // never discussed together
}

type PersonCount struct {
	Name   string `json:"name"`
	ID     string `json:"id,omitempty"`
	Claims int    `json:"claims"`
}

type TopicView struct {
	TopicSummary
	ClaimList []ClaimView         `json:"claimList"`
	People    []PersonCount       `json:"people"`
	Entities  []store.EntityCount `json:"entities"`
	Related   []RelatedTopic      `json:"related"`
	Events    []store.TopicEvent  `json:"events"`
}

// labelVisible: a label is shown only if the scope could see everything it
// was written from.
func labelVisible(sc sensitivity.Scope, l sensitivity.Level) bool {
	if l > sc.MaxLevel {
		return false
	}
	return len(sc.Audience) == 0 || l <= sc.Open
}

// Topics lists the topics with at least one visible claim, placed on a 2D
// map by meaning (the first two principal components of their centroids).
func (v Views) Topics() ([]TopicSummary, error) {
	d, err := v.loadTopics()
	if err != nil {
		return nil, err
	}
	return d.summaries, nil
}

// keywordTexts is how many of a topic's latest claims its keywords come from.
const keywordTexts = 200

// topicData is one load of what the topic views need: the visible topics'
// summaries and their claims' evidence.
type topicData struct {
	summaries []TopicSummary
	rows      []store.TopicRow
	ev        map[int64]*store.ClaimEvidence
}

// loadTopics loads every topic's claims under the scope once (cached, see
// viewCache): the topic list, the map and a topic page all start from it.
func (v Views) loadTopics() (*topicData, error) {
	if d, ok := viewCache.get(v, "topics"); ok {
		return d.(*topicData), nil
	}
	rows, err := v.Store.Topics()
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, t := range rows {
		ids = append(ids, t.Claims...)
	}
	ev, err := v.Store.Evidence(v.Scope, ids)
	if err != nil {
		return nil, err
	}
	var out []TopicSummary
	var cents [][]float32
	for _, t := range rows {
		ts := TopicSummary{ID: t.ID, Statuses: map[string]int{}}
		var texts []string
		for _, c := range t.Claims {
			e := ev[c]
			if e == nil {
				continue
			}
			ts.Claims++
			b := Compute(e, v.Now)
			ts.Statuses[b.Status]++
			if b.Last.After(ts.Last) {
				ts.Last = b.Last
			}
			texts = append(texts, e.Statements[len(e.Statements)-1].Text)
		}
		if ts.Claims == 0 {
			continue
		}
		// Keywords stand in for a label the scope can't see and feed the
		// filter: the topic's last claims are enough (ids rise with time),
		// and all of a large topic's texts took seconds at 400k claims.
		if len(texts) > keywordTexts {
			texts = texts[len(texts)-keywordTexts:]
		}
		ts.Keywords = Keywords(texts, 4)
		if labelVisible(v.Scope, t.LabelLevel) {
			ts.Label, ts.Summary = t.Label, t.Summary
		} else {
			ts.Hidden = true
			ts.Label = joinWords(ts.Keywords)
		}
		out = append(out, ts)
		cents = append(cents, t.Centroid)
	}
	xy := pca2(cents)
	for i := range out {
		out[i].X, out[i].Y = xy[i][0], xy[i][1]
	}
	d := &topicData{summaries: out, rows: rows, ev: ev}
	viewCache.put(v, "topics", d)
	return d, nil
}

// Topic returns one topic's page, or nil if nothing of it is visible.
func (v Views) Topic(id int64) (*TopicView, error) {
	m, err := v.Map()
	if err != nil {
		return nil, err
	}
	var tv *TopicView
	labels := map[int64]string{}
	for _, t := range m.Topics {
		labels[t.ID] = t.Label
		if t.ID == id {
			tv = &TopicView{TopicSummary: t.TopicSummary}
		}
	}
	if tv == nil {
		return nil, nil
	}
	ids, err := v.Store.TopicClaims(id)
	if err != nil {
		return nil, err
	}
	cs, err := v.Claims(ids)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(cs, func(i, j int) bool {
		ri, rj := statusRank[cs[i].Belief.Status], statusRank[cs[j].Belief.Status]
		if ri != rj {
			return ri < rj
		}
		return cs[i].Belief.Score > cs[j].Belief.Score
	})
	tv.ClaimList = cs
	people := map[string]*PersonCount{}
	for _, c := range cs {
		seen := map[string]bool{}
		for _, st := range c.Statements {
			k := holderKey(st)
			if seen[k] || st.Holder == "" {
				continue
			}
			seen[k] = true
			p := people[k]
			if p == nil {
				p = &PersonCount{Name: st.Holder, ID: st.HolderID}
				people[k] = p
			}
			p.Claims++
		}
	}
	for _, p := range people {
		tv.People = append(tv.People, *p)
	}
	sort.Slice(tv.People, func(i, j int) bool { return tv.People[i].Claims > tv.People[j].Claims })
	// Entities, shown only if the scope may know they exist.
	ents, err := v.Store.TopicEntities(id)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, e := range ents {
		if labelVisible(v.Scope, e.Level) {
			tv.Entities = append(tv.Entities, e)
			names[e.ID] = e.Name
		}
	}
	// The same links the topic map draws, strongest first.
	for _, e := range m.Edges {
		other := e.B
		if e.B == id {
			other = e.A
		} else if e.A != id {
			continue
		}
		rt := RelatedTopic{ID: other, Label: labels[other], Sim: e.Sim, Shared: e.Together, Via: e.Via,
			Unconnected: e.Together == 0 && e.Shared == 0}
		switch {
		case len(e.Via) > 0:
			rt.Kind = "same"
		case e.Kind == "shared":
			rt.Kind, rt.Shared = "shared", e.Shared
		case e.Together >= 2:
			rt.Kind = "together"
		case len(tv.Entities) > 0 && e.Sim >= relateAt:
			rt.Kind = "parallel"
		default:
			rt.Kind = "similar"
		}
		tv.Related = append(tv.Related, rt)
	}
	sort.SliceStable(tv.Related, func(i, j int) bool {
		order := map[string]int{"shared": 0, "same": 1, "together": 2, "parallel": 3, "similar": 4}
		return order[tv.Related[i].Kind] < order[tv.Related[j].Kind]
	})
	if tv.Events, err = v.Store.TopicEvents(id); err != nil {
		return nil, err
	}
	return tv, nil
}

// statusRank orders a topic page: what's live first, then what changed.
var statusRank = map[string]int{
	"contested": 0, "established": 1, "likely": 2, "open": 3, "speculative": 4,
	"uncertain": 5, "answered": 6, "superseded": 7, "disproven": 8, "stale": 9,
}

func joinWords(ws []string) string {
	s := ""
	for i, w := range ws {
		if i > 0 {
			s += " · "
		}
		s += w
	}
	return s
}

// pca2 projects vectors onto their first two principal components by power
// iteration, without forming the covariance matrix.
func pca2(vs [][]float32) [][2]float64 {
	n := len(vs)
	out := make([][2]float64, n)
	if n == 0 || len(vs[0]) == 0 {
		return out
	}
	d := len(vs[0])
	mean := make([]float64, d)
	for _, v := range vs {
		for i := range v {
			mean[i] += float64(v[i]) / float64(n)
		}
	}
	x := make([][]float64, n)
	for k, v := range vs {
		x[k] = make([]float64, d)
		for i := range v {
			x[k][i] = float64(v[i]) - mean[i]
		}
	}
	var comps [][]float64
	for c := 0; c < 2; c++ {
		w := make([]float64, d)
		for i := range w {
			w[i] = math.Sin(float64(i+1) * float64(c+1)) // deterministic start
		}
		for it := 0; it < 60; it++ {
			nw := make([]float64, d)
			for _, row := range x {
				p := dot(row, w)
				for i := range row {
					nw[i] += p * row[i]
				}
			}
			for _, prev := range comps {
				p := dot(nw, prev)
				for i := range nw {
					nw[i] -= p * prev[i]
				}
			}
			nrm := math.Sqrt(dot(nw, nw))
			if nrm == 0 {
				break
			}
			for i := range nw {
				nw[i] /= nrm
			}
			w = nw
		}
		comps = append(comps, w)
	}
	for k, row := range x {
		out[k] = [2]float64{dot(row, comps[0]), dot(row, comps[1])}
	}
	return out
}

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
