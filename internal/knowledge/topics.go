package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// TopicsVersion changes with clustering or labelling.
const TopicsVersion = "t3"

// Clustering thresholds on cosine similarity: groups merge while their
// average similarity is at least joinAt (about the 95th percentile of claim
// pairs on the synthetic corpus, where typical pairs sit at 0.59); topics
// relate at relateAt.
const (
	joinAt    = 0.70
	relateAt  = 0.62
	labelFrom = 8 // claims used to write a label
	keepIDAt  = 0.4
	keepLabel = 0.7
)

const labelPrompt = `These statements come from one person's meetings, email and chat, and were grouped together because they are about the same topic. Name the topic in 2 to 5 words, as a person would refer to it (e.g. "Acme renewal pricing", "search latency regression"), and summarise in one sentence what the statements say about it.
Reply as JSON: {"label":"...","summary":"..."}`

// Topicer clusters claims into topics and labels them.
type Topicer struct {
	Store *store.Store
	LLM   provider.LLM // nil: labels from the most common words
	// Entities returns the organisations and projects a text names (entity
	// ids). Claims about different ones are kept in different topics, which
	// then link as "parallel"; nil turns this off.
	Entities func(text string) []string
}

type topicDraft struct {
	claims   []int64
	sum      []float32
	centroid []float32
}

// entityPenalty is subtracted from the similarity of two claims naming
// different organisations or projects (and none in common): a commitment to
// one customer and a commitment to another read alike but are separate
// topics.
const entityPenalty = 0.15

// Build reclusters every claim and writes the topics, keeping ids (and
// labels, when membership barely changed) from the previous run, and
// records how topics changed.
func (t *Topicer) Build(ctx context.Context) (int, error) {
	claims, err := t.Store.Claims()
	if err != nil {
		return 0, err
	}
	sort.Slice(claims, func(i, j int) bool {
		return claims[i].SaidAt.Before(claims[j].SaidAt) || (claims[i].SaidAt.Equal(claims[j].SaidAt) && claims[i].ID < claims[j].ID)
	})

	// Entities per claim, from all its statements.
	claimEnts := map[int64]map[string]bool{}
	if t.Entities != nil {
		var ids []int64
		for _, c := range claims {
			ids = append(ids, c.Members...)
		}
		sts, err := t.Store.StatementsByID(ids)
		if err != nil {
			return 0, err
		}
		for _, c := range claims {
			es := map[string]bool{}
			for _, m := range c.Members {
				for _, e := range t.Entities(sts[m].Text) {
					es[e] = true
				}
			}
			claimEnts[c.ID] = es
		}
	}

	var order []int64
	var vs [][]float32
	for _, c := range claims {
		if c.Centroid != nil && len(c.Centroid) > 0 {
			order = append(order, c.ID)
			vs = append(vs, normalized(c.Centroid))
		}
	}
	adjust := func(i, j int) float32 {
		a, b := claimEnts[order[i]], claimEnts[order[j]]
		if len(a) == 0 || len(b) == 0 {
			return 0
		}
		for e := range a {
			if b[e] {
				return 0
			}
		}
		return -entityPenalty
	}
	var drafts []*topicDraft
	for _, members := range hac(vs, joinAt, adjust) {
		if len(members) < 2 {
			continue // a topic needs at least two claims
		}
		d := &topicDraft{}
		for _, m := range members {
			d.claims = append(d.claims, order[m])
			d.sum = add(d.sum, vs[m])
		}
		d.centroid = normalized(d.sum)
		drafts = append(drafts, d)
	}

	prev, err := t.Store.Topics()
	if err != nil {
		return 0, err
	}
	levels, err := t.Store.StatementLevels()
	if err != nil {
		return 0, err
	}
	byClaim := map[int64]store.ClaimRow{}
	for _, c := range claims {
		byClaim[c.ID] = c
	}
	used := map[int64]bool{}
	var maxID int64
	for _, p := range prev {
		if p.ID > maxID {
			maxID = p.ID
		}
	}
	var out []store.TopicRow
	for _, d := range drafts {
		row := store.TopicRow{Centroid: d.centroid, Claims: d.claims}
		// Keep the id of the previous topic it mostly is.
		bestJ, bestP := 0.0, -1
		for i, p := range prev {
			if used[p.ID] {
				continue
			}
			if j := jaccard(d.claims, p.Claims); j > bestJ {
				bestJ, bestP = j, i
			}
		}
		if bestP >= 0 && bestJ >= keepIDAt {
			row.ID = prev[bestP].ID
			used[row.ID] = true
			if bestJ >= keepLabel {
				row.Label, row.Summary, row.LabelLevel = prev[bestP].Label, prev[bestP].Summary, prev[bestP].LabelLevel
			}
		} else {
			maxID++
			row.ID = maxID
		}
		if row.Label == "" {
			if err := t.label(ctx, &row, byClaim, levels); err != nil {
				return 0, err
			}
		}
		out = append(out, row)
	}

	// Entities per topic.
	topicEnts := map[int64]map[string]int{}
	for _, r := range out {
		es := map[string]int{}
		for _, c := range r.Claims {
			for e := range claimEnts[c] {
				es[e]++
			}
		}
		topicEnts[r.ID] = es
	}

	// Relations: shared organisations or projects, meaning across different
	// ones, and items discussing both.
	items, err := t.Store.ClaimItems()
	if err != nil {
		return 0, err
	}
	topicItems := make([]map[string]bool, len(out))
	for i, r := range out {
		topicItems[i] = map[string]bool{}
		for _, c := range r.Claims {
			for _, it := range items[c] {
				topicItems[i][it] = true
			}
		}
	}
	var rels []store.TopicRelation
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			sim := cos(out[i].Centroid, out[j].Centroid)
			shared := 0
			for it := range topicItems[i] {
				if topicItems[j][it] {
					shared++
				}
			}
			var common []string
			for e := range topicEnts[out[i].ID] {
				if topicEnts[out[j].ID][e] > 0 {
					common = append(common, e)
				}
			}
			sort.Strings(common)
			if sim >= relateAt || shared >= 2 || len(common) > 0 {
				rels = append(rels, store.TopicRelation{A: out[i].ID, B: out[j].ID, Sim: sim, Shared: shared, Entities: common})
			}
		}
	}
	events := lifecycle(prev, out, time.Now())
	return len(out), t.Store.ReplaceTopics(out, rels, topicEnts, events)
}

// lifecycle compares the previous topics with the new ones: births, deaths,
// merges, splits, growth.
func lifecycle(prev []store.TopicRow, next []store.TopicRow, now time.Time) []store.TopicEvent {
	var ev []store.TopicEvent
	prevByID := map[int64]store.TopicRow{}
	claimPrev := map[int64]int64{}
	for _, p := range prev {
		prevByID[p.ID] = p
		for _, c := range p.Claims {
			claimPrev[c] = p.ID
		}
	}
	nextByID := map[int64]store.TopicRow{}
	claimNext := map[int64]int64{}
	for _, n := range next {
		nextByID[n.ID] = n
		for _, c := range n.Claims {
			claimNext[c] = n.ID
		}
	}
	for _, n := range next {
		from := map[int64]int{}
		for _, c := range n.Claims {
			if p, ok := claimPrev[c]; ok && p != n.ID {
				from[p]++
			}
		}
		p, existed := prevByID[n.ID]
		for pid, k := range from {
			if k*3 >= len(n.Claims) || k >= 3 {
				kind := "merged from"
				if _, still := nextByID[pid]; still {
					kind = "split from"
				}
				ev = append(ev, store.TopicEvent{At: now, TopicID: n.ID, Event: kind, Other: pid, Detail: fmt.Sprintf("%d claims", k)})
			}
		}
		switch {
		case !existed:
			ev = append(ev, store.TopicEvent{At: now, TopicID: n.ID, Event: "born", Detail: fmt.Sprintf("%d claims", len(n.Claims))})
		case len(n.Claims)-len(p.Claims) >= 2:
			ev = append(ev, store.TopicEvent{At: now, TopicID: n.ID, Event: "grew", Detail: fmt.Sprintf("%d → %d claims", len(p.Claims), len(n.Claims))})
		case len(p.Claims)-len(n.Claims) >= 2:
			ev = append(ev, store.TopicEvent{At: now, TopicID: n.ID, Event: "shrank", Detail: fmt.Sprintf("%d → %d claims", len(p.Claims), len(n.Claims))})
		}
	}
	for _, p := range prev {
		if _, ok := nextByID[p.ID]; ok {
			continue
		}
		into := map[int64]int{}
		for _, c := range p.Claims {
			if n, ok := claimNext[c]; ok {
				into[n]++
			}
		}
		best, bn := int64(0), 0
		for n, k := range into {
			if k > bn {
				best, bn = n, k
			}
		}
		if bn*2 >= len(p.Claims) {
			ev = append(ev, store.TopicEvent{At: now, TopicID: p.ID, Event: "merged into", Other: best, Detail: fmt.Sprintf("%d claims", len(p.Claims))})
			// Recorded on the surviving topic too, where it can be seen.
			ev = append(ev, store.TopicEvent{At: now, TopicID: best, Event: "absorbed", Other: p.ID, Detail: fmt.Sprintf("%d claims", bn)})
		} else {
			ev = append(ev, store.TopicEvent{At: now, TopicID: p.ID, Event: "dissolved", Detail: fmt.Sprintf("%d claims", len(p.Claims))})
		}
	}
	return ev
}

// label writes a topic's label from its most open claims first, and records
// the highest level of the statements used: a narrower scope won't see a
// label written from material it can't see.
func (t *Topicer) label(ctx context.Context, row *store.TopicRow, byClaim map[int64]store.ClaimRow, levels map[int64]sensitivity.Level) error {
	type cl struct {
		text  string
		level sensitivity.Level
		n     int
	}
	var cs []cl
	for _, id := range row.Claims {
		c := byClaim[id]
		lvl := sensitivity.Restricted
		for _, m := range c.Members {
			if l, ok := levels[m]; ok && l < lvl {
				lvl = l
			}
		}
		cs = append(cs, cl{c.Text, lvl, len(c.Members)})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].level != cs[j].level {
			return cs[i].level < cs[j].level
		}
		return cs[i].n > cs[j].n
	})
	if len(cs) > labelFrom {
		cs = cs[:labelFrom]
	}
	var texts []string
	for _, c := range cs {
		texts = append(texts, "- "+c.text)
		if c.level > row.LabelLevel {
			row.LabelLevel = c.level
		}
	}
	if t.LLM == nil {
		row.Label = strings.Join(Keywords(texts, 3), " · ")
		return nil
	}
	out, err := t.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: labelPrompt}, {Role: "user", Content: strings.Join(texts, "\n")}},
		provider.ChatOptions{JSON: true, Temperature: 0, MaxTokens: 120}, nil)
	if err != nil {
		return err
	}
	var p struct{ Label, Summary string }
	if json.Unmarshal([]byte(out), &p) != nil || strings.TrimSpace(p.Label) == "" {
		row.Label = strings.Join(Keywords(texts, 3), " · ")
		return nil
	}
	row.Label, row.Summary = strings.TrimSpace(p.Label), strings.TrimSpace(p.Summary)
	return nil
}

// Keywords returns the most frequent content words, for labels a scope may
// see when the written label isn't visible to it.
func Keywords(texts []string, n int) []string {
	count := map[string]int{}
	for _, t := range texts {
		seen := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
			if len(w) < 4 || stopWords[w] || seen[w] {
				continue
			}
			seen[w] = true
			count[w]++
		}
	}
	var ws []string
	for w := range count {
		ws = append(ws, w)
	}
	sort.Slice(ws, func(i, j int) bool {
		if count[ws[i]] != count[ws[j]] {
			return count[ws[i]] > count[ws[j]]
		}
		return ws[i] < ws[j]
	})
	if len(ws) > n {
		ws = ws[:n]
	}
	return ws
}

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`that this with from have will would should could they them their there what when which about been were said says also into than then just only more most very some such over after before because while where these those being does need needs want wants think thinks going make made like said will shall your yours ours user other others each every much many week weeks today tomorrow`) {
		stopWords[w] = true
	}
}

func jaccard(a, b []int64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	s := map[int64]bool{}
	for _, x := range a {
		s[x] = true
	}
	inter := 0
	for _, x := range b {
		if s[x] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
