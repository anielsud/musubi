package live

// Lab: each step of Live's pipeline on its own, for cmd/lab (the prompt
// playground). Nothing here runs in the app. Steps that Live runs inside
// its loops are repeated here from the same helpers and constants, so a
// change to one of them shows up in both; the picker and the card brief
// are copies of pick() and writeBrief() without Live's state.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// Lab holds a Live loaded with its indexes (suggester, cards as of a time,
// relevance classifier, brief model) and no meeting.
type Lab struct {
	L *Live
	// last holds each query's latest lookup, so the gate can be given what
	// Live would give it (a Brief) without sending hits back and forth.
	last map[string]labLookup
	// svecs are the statement vectors, loaded on first use.
	svecs map[int64][]float32
}

type labLookup struct {
	hits   []store.Hit
	status *BriefStatus
}

// NewLab loads the indexes the way Reload does. asOf is when the meeting
// started: cards then only know what was said before it.
func NewLab(eng *engine.Engine, asOf time.Time) (*Lab, error) {
	l := New(eng, func(string, any) {}, nil)
	l.mu.Lock()
	l.topicAsOf = asOf
	l.mu.Unlock()
	if err := l.Reload(); err != nil {
		return nil, err
	}
	return &Lab{L: l, last: map[string]labLookup{}}, nil
}

// SetAsOf rebuilds the card index as of a time.
func (b *Lab) SetAsOf(t time.Time) error {
	cs, err := b.L.eng.LoadCardsBefore(t)
	if err != nil {
		return err
	}
	b.L.mu.Lock()
	b.L.topicIx, b.L.topicAsOf = cs, t
	b.L.mu.Unlock()
	return nil
}

// FastOut is the fast lane's reading of a chunk: no model.
type FastOut struct {
	Entities []Spotted     `json:"entities"`
	Phrases  []PhraseScore `json:"phrases"`
}

func (b *Lab) Fast(text string) FastOut {
	return FastOut{Entities: b.L.sug.Entities(text), Phrases: b.L.sug.PhraseScores(text, 3)}
}

// RankIn is one suggestion going into ranking.
type RankIn struct {
	Query string  `json:"query"`
	Kind  string  `json:"kind"` // llm, entity, phrase
	Score float64 `json:"score"`
}

// RankOut is what Live decides about a suggestion.
type RankOut struct {
	Query    string  `json:"query"`
	Kind     string  `json:"kind"`
	Score    float64 `json:"score"`
	Decision string  `json:"decision"`
}

// Rank mirrors modelCycle after the model: owner-name queries are dropped,
// duplicates removed, the rest sorted by score, the top autoBrief looked up
// unless like one looked up in the last five minutes (recent).
func (b *Lab) Rank(in []RankIn, owner, ownerMode string, recent []string, autoBrief int) ([]RankOut, []string) {
	var sugs []Suggestion
	for _, s := range in {
		sugs = append(sugs, Suggestion{Query: s.Query, Kind: s.Kind, Score: s.Score})
	}
	var llms, rest []Suggestion
	for _, s := range sugs {
		if s.Kind == "llm" {
			llms = append(llms, s)
		} else {
			rest = append(rest, s)
		}
	}
	var dropped []string
	if ownerMode == "scrub" {
		llms, dropped = cleanOwner(llms, owner)
	} else {
		llms, dropped = dropOwner(llms, owner)
	}
	out := dedupe(append(rest, llms...))
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	briefed := map[string]bool{}
	for _, r := range recent {
		briefed[normalize(r)] = true
	}
	var res []RankOut
	todo := 0
	for _, s := range out {
		d := RankOut{Query: s.Query, Kind: s.Kind, Score: s.Score}
		switch {
		case todo >= autoBrief:
			d.Decision = fmt.Sprintf("shown, not looked up (below the top %d)", autoBrief)
		default:
			cw, like := contentWords(s.Query), ""
			for r := range briefed {
				if r == normalize(s.Query) || overlap(cw, contentWords(r)) >= 0.5 {
					like = r
					break
				}
			}
			if like != "" {
				d.Decision = "skipped: like “" + like + "”, looked up recently"
			} else {
				d.Decision = "looking up"
				todo++
				briefed[normalize(s.Query)] = true
			}
		}
		res = append(res, d)
	}
	return res, dropped
}

// HitOut is a passage found by a lookup.
type HitOut struct {
	Rank    int     `json:"rank"`
	Item    string  `json:"item"`
	Seq     int     `json:"seq"`
	Title   string  `json:"title"`
	Date    string  `json:"date"`
	Score   float64 `json:"score"`
	Level   string  `json:"level"`
	Excerpt string  `json:"excerpt"`
	Text    string  `json:"text"`
}

// SearchOut is a lookup: the query as spelled, the thread status that
// replaces the summary when one fits, and the passages.
type SearchOut struct {
	Spelled string       `json:"spelled"`
	Status  *BriefStatus `json:"status,omitempty"`
	Hits    []HitOut     `json:"hits"`
	Excerpt string       `json:"excerpts"` // what the summary model reads
}

func (b *Lab) Search(ctx context.Context, query, scopeName string, k int) (SearchOut, error) {
	sc, err := b.L.eng.Scope(scopeName)
	if err != nil {
		return SearchOut{}, err
	}
	b.L.mu.Lock()
	spelled := b.L.spell.Expand(query)
	b.L.mu.Unlock()
	out := SearchOut{Spelled: spelled, Status: b.L.threadStatus(ctx, sc, spelled)}
	hits, err := b.L.eng.Search(ctx, sc, spelled, k, nil)
	if err != nil {
		return out, err
	}
	b.last[query] = labLookup{hits: hits, status: out.Status}
	for i, h := range hits {
		out.Hits = append(out.Hits, HitOut{Rank: i + 1, Item: h.ItemID, Seq: h.Seq, Title: h.Title, Date: h.OccurredAt.Format("2006-01-02"),
			Score: h.Score, Level: h.Label.Level.String(), Excerpt: excerpt(h.Text, 240), Text: h.Text})
	}
	out.Excerpt = ask.Excerpts(hits)
	return out, nil
}

// Summarise is the summary step: the model's two sentences from the hits, or
// NONE. system is briefPrompt unless overridden.
func (b *Lab) Summarise(ctx context.Context, query, excerpts, system string, llm provider.LLM) (string, error) {
	if system == "" {
		system = briefPrompt
	}
	out, err := llm.Complete(ctx, []provider.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: excerpts + "\nTopic: " + query},
	}, provider.ChatOptions{Temperature: 0.2, MaxTokens: 160}, nil)
	return strings.TrimSpace(out), err
}

// TopicOut is what a query does to the card index.
type TopicOut struct {
	Found string          `json:"found"` // the card Find would open ("" for none)
	How   string          `json:"how"`
	Near  []NearTopic     `json:"near"`
	Card  json.RawMessage `json:"card,omitempty"`
}

type NearTopic struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Last  string `json:"last"`
}

func (b *Lab) Topic(ctx context.Context, query, scopeName string, asOf time.Time, wantCard bool) (TopicOut, error) {
	b.L.mu.Lock()
	cs := b.L.topicIx
	b.L.mu.Unlock()
	var out TopicOut
	if cs == nil {
		return out, fmt.Errorf("no card index")
	}
	sc, err := b.L.eng.Scope(scopeName)
	if err != nil {
		return out, err
	}
	title := func(id int64) NearTopic {
		t := NearTopic{ID: id}
		if c, err := cs.Card(sc, knowledge.Anchor{Topic: id}, asOf); err == nil && c != nil {
			t.Title, t.Last = c.Title, c.Last.Format("2006-01-02")
		}
		return t
	}
	for _, id := range cs.Near(ctx, query, pickNear) {
		out.Near = append(out.Near, title(id))
	}
	a, how, err := cs.Find(ctx, query, false)
	if err == nil && !a.IsZero() {
		out.Found, out.How = a.Key(), how
		if wantCard {
			if c, err := cs.Card(sc, a, asOf); err == nil && c != nil {
				out.Card, _ = json.Marshal(c)
			}
		}
	}
	return out, nil
}

// GateOut is the gate's verdict and exactly what it was asked.
type GateOut struct {
	P            float64 `json:"p"`
	Min          float64 `json:"min"`
	Conversation string  `json:"conversation"`
	Item         string  `json:"item"`
	Question     string  `json:"question"`
}

// Gate judges a lookup as Live does: conversation is the last 90 s as
// "Speaker: text" lines (Meeting.Recent), item is briefText of the lookup
// (query, the line that raised it, then the status, else the summary, else
// the top three passages). summary is the model's text for the lookup, "" if
// none was written. question overrides relevanceQuestion.
func (b *Lab) Gate(ctx context.Context, query, chunk, summary, question string) (GateOut, error) {
	var out GateOut
	b.L.mu.Lock()
	rel := b.L.rel
	b.L.mu.Unlock()
	if rel == nil {
		return out, fmt.Errorf("live.relevance is off in this config")
	}
	if question == "" {
		question = relevanceQuestion
	}
	var segs []Segment
	var rec strings.Builder
	for _, ln := range strings.Split(chunk, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		who, text, _ := strings.Cut(ln, ": ")
		segs = append(segs, Segment{Speaker: who, Text: text})
		rec.WriteString(ln + "\n")
	}
	lk := b.last[query]
	br := Brief{Query: query, Status: lk.status, Hits: lk.hits, Summary: summary}
	if b.L.eng.Cfg.Live.Focus {
		br.Quote = evidenceLine(query, segs)
	}
	out.Conversation, out.Item, out.Question = rec.String(), briefText(br), question
	out.Min = b.L.relevanceMin()
	a, err := rel.Classify(ctx, map[string]any{"conversation": out.Conversation, "item": out.Item},
		map[string]provider.Question{"helps": {Type: "noul", Instructions: question}})
	if err != nil {
		return out, err
	}
	out.P = a["helps"].Noul
	return out, nil
}

// LLM calls the local chat model the way Live does (non-streaming, same
// request builder), with a model, system prompt and user message of the
// caller's choosing.
func (b *Lab) LLM(ctx context.Context, model, system, user string, jsonMode bool, temp float64, maxTok int) (string, error) {
	c := b.L.eng.Cfg.LLM
	c.Model = model
	chat, err := provider.NewChat(c)
	if err != nil || chat == nil {
		return "", fmt.Errorf("no local chat model: %v", err)
	}
	return chat.Complete(ctx, []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		provider.ChatOptions{JSON: jsonMode, Temperature: temp, MaxTokens: maxTok}, nil)
}

// Candidate is a topic put forward for the picker, with why.
type Candidate struct {
	ID    int64   `json:"id"`
	Title string  `json:"title"`
	Score float64 `json:"score"`
	Why   string  `json:"why"`
	P     float64 `json:"p"`
	Pick  bool    `json:"pick"`
}

// PickOut is the picker's verdict.
type PickOut struct {
	None       float64     `json:"none"`
	Min        float64     `json:"min"`
	Candidates []Candidate `json:"candidates"`
	Options    string      `json:"options"`
}

// Pick is pick(): the queries' nearest topics (1, 1/2, 1/3), the talk's
// nearest five (3, 3/2, ...), the top pickCands as options, and Jev
// choosing among them or "none".
func (b *Lab) Pick(ctx context.Context, title, window string, queries []string, asOf time.Time, scopeName, question string) (PickOut, error) {
	if question == "" {
		question = pickQuestion
	}
	b.L.mu.Lock()
	cs, rel := b.L.topicIx, b.L.rel
	b.L.mu.Unlock()
	var out PickOut
	if cs == nil || rel == nil {
		return out, fmt.Errorf("card index or classifier missing")
	}
	sc, err := b.L.eng.Scope(scopeName)
	if err != nil {
		return out, err
	}
	score, why := map[int64]float64{}, map[int64]string{}
	add := func(t int64, w float64, r string) {
		score[t] += w
		if why[t] == "" {
			why[t] = r
		}
	}
	for _, q := range queries {
		for i, t := range cs.Near(ctx, q, pickNear) {
			add(t, 1/float64(i+1), "“"+q+"”")
		}
	}
	for i, t := range cs.Near(ctx, window, pickTalk) {
		add(t, 3/float64(i+1), "the talk")
	}
	order := make([]int64, 0, len(score))
	for t := range score {
		order = append(order, t)
	}
	sort.Slice(order, func(i, j int) bool {
		if score[order[i]] != score[order[j]] {
			return score[order[i]] > score[order[j]]
		}
		return order[i] < order[j]
	})
	sts := b.L.pickStatuses(scopeName)
	opts := map[string]string{"none": "None of these topics is what the conversation is about"}
	titles := map[int64]string{}
	var ids []int64
	for _, t := range order {
		if len(ids) == pickCands {
			break
		}
		c, err := cs.Card(sc, knowledge.Anchor{Topic: t}, asOf)
		if err != nil || c == nil {
			continue
		}
		s := fmt.Sprintf("%s (last discussed %s)", c.Title, c.Last.Format("2006-01-02"))
		if st, ok := sts[c.TopicID]; ok && !c.Collapsed && !st.AsOf.After(asOf) {
			var v knowledge.Status
			if json.Unmarshal([]byte(st.Body), &v) == nil {
				if n := knowledge.FirstSentence(knowledge.StripIDs(v.Now)); n != "" {
					s += " — " + n
				}
			}
		}
		opts[fmt.Sprintf("t%d", t)] = s
		titles[t], _, _ = strings.Cut(s, " (last discussed")
		ids = append(ids, t)
	}
	if len(ids) == 0 {
		return out, nil
	}
	a, err := rel.Classify(ctx, map[string]any{"meeting": title, "conversation": window},
		map[string]provider.Question{"topic": {Type: "choice", Instructions: question, Options: opts}})
	if err != nil {
		return out, err
	}
	probs := a["topic"].Probabilities
	out.None, out.Min = probs["none"], b.L.pickMin()
	picked := 0
	for _, t := range ids {
		out.Candidates = append(out.Candidates, Candidate{ID: t, Title: titles[t], Score: score[t], Why: why[t], P: probs[fmt.Sprintf("t%d", t)]})
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool { return out.Candidates[i].P > out.Candidates[j].P })
	for i := range out.Candidates {
		if picked < pickOpen && out.Candidates[i].P >= out.Min {
			out.Candidates[i].Pick = true
			picked++
		}
	}
	var ob strings.Builder
	for _, t := range ids {
		fmt.Fprintf(&ob, "t%d: %s\n", t, opts[fmt.Sprintf("t%d", t)])
	}
	out.Options = ob.String()
	return out, nil
}

// BriefOut is the card brief: its prompt and the model's headline and lines.
type BriefOut struct {
	Prompt   string   `json:"prompt"`
	Raw      string   `json:"raw"`
	Fits     bool     `json:"fits"`
	Headline string   `json:"headline"`
	Raise    []string `json:"raise"`
}

// CardBrief is writeBrief() for one topic without Live's state: asked are
// the lookups that put it forward, before and since the conversation
// before and after it came up (Meeting.RecentSplit).
func (b *Lab) CardBrief(ctx context.Context, topic int64, title, people, asked, before, since, scopeName string, asOf time.Time, promptOverride string) (BriefOut, error) {
	var out BriefOut
	b.L.mu.Lock()
	cs, llm := b.L.topicIx, b.L.briefLLM
	b.L.mu.Unlock()
	if cs == nil || llm == nil {
		return out, fmt.Errorf("card index or [live.pick.brief] missing")
	}
	sc, err := b.L.eng.Scope(scopeName)
	if err != nil {
		return out, err
	}
	c, err := cs.Card(sc, knowledge.Anchor{Topic: topic}, asOf)
	if err != nil || c == nil {
		return out, fmt.Errorf("no card for topic %d", topic)
	}
	var now string
	if sts := b.L.pickStatuses(scopeName); sts != nil {
		if st, ok := sts[c.TopicID]; ok && !st.AsOf.After(asOf) {
			var v knowledge.Status
			if json.Unmarshal([]byte(st.Body), &v) == nil {
				now = knowledge.FirstSentence(knowledge.StripIDs(v.Now))
			}
		}
	}
	tmpl := cardBriefPrompt
	if promptOverride != "" {
		tmpl = promptOverride
	}
	out.Prompt = cardBriefText(tmpl, c, now, title, asOf.Format("Monday 2 January 2006"), people, asked, before, since)
	raw, err := llm.Complete(ctx, []provider.Message{{Role: "user", Content: out.Prompt}},
		provider.ChatOptions{JSON: true, Temperature: 0.3, MaxTokens: 4000}, nil)
	out.Raw = raw
	if err != nil {
		return out, err
	}
	cb, err := parseBrief(raw)
	if err != nil {
		return out, err
	}
	out.Fits, out.Headline = !cb.Off, cb.Headline
	out.Raise = append([]string{cb.Raise}, cb.More...)
	return out, nil
}

var _ = config.LLM{}

// LabPrompts are the prompts and questions Live uses, to start the
// playground's editors from the real text.
func LabPrompts() map[string]string {
	return map[string]string{
		"suggest": suggestPrompt, "summary": briefPrompt, "cardBrief": cardBriefPrompt,
		"pickQuestion": pickQuestion, "relevanceQuestion": relevanceQuestion, "poolQuestion": poolQuestion,
	}
}
