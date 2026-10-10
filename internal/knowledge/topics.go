package knowledge

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// TopicsVersion changes with clustering or labelling.
const TopicsVersion = "t2"

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
}

type topicDraft struct {
	claims   []int64
	sum      []float32
	centroid []float32
}

// Build reclusters every claim and writes the topics, keeping ids (and
// labels, when membership barely changed) from the previous run.
func (t *Topicer) Build(ctx context.Context) (int, error) {
	claims, err := t.Store.Claims()
	if err != nil {
		return 0, err
	}
	sort.Slice(claims, func(i, j int) bool {
		return claims[i].SaidAt.Before(claims[j].SaidAt) || (claims[i].SaidAt.Equal(claims[j].SaidAt) && claims[i].ID < claims[j].ID)
	})
	cen := map[int64][]float32{}
	for _, c := range claims {
		if c.Centroid != nil && len(c.Centroid) > 0 {
			cen[c.ID] = normalized(c.Centroid)
		}
	}
	var order []int64
	var vs [][]float32
	for _, c := range claims {
		if v := cen[c.ID]; v != nil {
			order = append(order, c.ID)
			vs = append(vs, v)
		}
	}
	// A topic needs at least two claims; single claims stay loose.
	var drafts []*topicDraft
	for _, members := range hac(vs, joinAt) {
		if len(members) < 2 {
			continue
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

	// Relations: similar in meaning, and how many items discuss both.
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
			if sim >= relateAt || shared >= 2 {
				rels = append(rels, store.TopicRelation{A: out[i].ID, B: out[j].ID, Sim: sim, Shared: shared})
			}
		}
	}
	return len(out), t.Store.ReplaceTopics(out, rels)
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
