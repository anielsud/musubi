package live

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/knowledge"
)

// TopicCard is a topic's card on the panel, and why it is there.
type TopicCard struct {
	*knowledge.Card
	// Now is the first sentence of the thread's written status, when the
	// scope may see it (and, in a replay, it was written by then).
	Now string    `json:"now,omitempty"`
	Why string    `json:"why"`
	At  time.Time `json:"at"`
	// SpokenAgo is how long before the card was ready its trigger was said
	// (meeting seconds); -1 when it came from a lookup, not a word.
	SpokenAgo float64 `json:"spokenAgo"`
	// Focus (live.focus): Quote is the line of the conversation that last
	// raised the card; Lines, its few lines closest to the last 90 s;
	// Active, that it is one of the two most recently raised, within
	// focusActive; Stale, that the subject was last discussed over
	// staleAfter ago; MentionedAgo, meeting seconds since it was raised.
	Focus        bool        `json:"focus"`
	Quote        string      `json:"quote,omitempty"`
	Lines        []FocusLine `json:"lines,omitempty"`
	Active       bool        `json:"active"`
	Stale        bool        `json:"stale,omitempty"`
	MentionedAgo float64     `json:"mentionedAgo"`
	// Fit is how well it fits the conversation (live.relevance).
	Fit float64 `json:"fit,omitempty"`
	// Kept: pushed off the panel by newer cards, kept collapsed
	// (live.keep_topics); not ranked against the conversation.
	Kept bool `json:"kept,omitempty"`
	// live.pick.brief: Headline says where the topic stands, Raise what to
	// raise about it in this meeting; Rank, for a kept card, its place
	// among those most worth raising (0: not among them).
	Headline string `json:"headline,omitempty"`
	Raise    string `json:"raise,omitempty"`
	// RaiseMore: other things worth raising, shown when the card is opened.
	RaiseMore []string `json:"raiseMore,omitempty"`
	Rank      int      `json:"rank,omitempty"`
}

// FocusLine is a card line chosen for the conversation, with its section.
type FocusLine struct {
	knowledge.CardLine
	Section string  `json:"section"`
	Score   float64 `json:"score"`
}

type shownTopic struct {
	id        knowledge.Anchor
	why       string
	at        time.Time
	spokenAgo float64
	quote     string
	mentioned float64 // meeting clock when last raised
}

const (
	focusActive  = 180.0 // seconds a card stays open after it was last raised
	focusOpen    = 2     // cards open at once
	focusLines   = 3     // lines a card shows
	focusWindow  = 90.0  // seconds of conversation the lines are ranked against
	focusRefresh = 15 * time.Second
	staleAfter   = 60 * 24 * time.Hour
)

// maxTopics is how many topic cards the panel keeps, newest first.
const maxTopics = 4

// termTopics shows the cards of topics whose trigger terms a line says.
// Drafts count: a programme's name is distinctive enough to act on before
// the line is final.
func (l *Live) termTopics(cycle int, text string, lines []Segment) {
	l.mu.Lock()
	cs := l.topicIx
	l.mu.Unlock()
	if cs == nil {
		return
	}
	pick := l.picking()
	for _, h := range cs.Index.TermsIn(text) {
		if pick {
			l.candidate(cmp.Or(h.Anchor.Topic, h.Topic), pickTerm, "says "+h.Term)
			continue
		}
		said := evidenceAt(h.Term, lines)
		ago := -1.0
		if said >= 0 {
			ago = l.clock() - said
		}
		l.showTopic(cycle, h.Anchor, "says "+h.Term, ago, evidenceLine(h.Term, lines))
	}
	// The conversation moves on: cards are ranked again, and close, as it
	// does.
	l.mu.Lock()
	due := l.eng.Cfg.Live.Focus && (len(l.topics) > 0 || len(l.places) > 0) && l.since(l.topicsAt) >= focusRefresh
	rel := l.rel != nil
	l.mu.Unlock()
	if pick {
		l.maybePick(cycle)
	}
	if due {
		if rel {
			l.rejudge(cycle, pick)
		}
		l.emitTopics(cycle)
	}
}

// rejudge judges what is shown again as the talk moves on; a lookup that no
// longer fits gives up its place. With the picker the places hold only
// lookups and emitTopics doesn't choose, so the choice is made here.
func (l *Live) rejudge(cycle int, pick bool) {
	l.rescore(cycle)
	if !pick {
		return
	}
	resend, _ := l.choose()
	for _, o := range resend {
		o.Rated = true
		l.emit("live:brief", o)
	}
}

// lookupTopic shows the card of the topic a lookup's nearest claims are on:
// the model's queries paraphrase ("the R&D cuts"), which no term catches.
func (l *Live) lookupTopic(ctx context.Context, cycle int, s Suggestion) {
	l.mu.Lock()
	cs := l.topicIx
	l.mu.Unlock()
	if cs == nil || s.Kind == "entity" {
		return
	}
	if l.picking() {
		l.lookupCandidates(ctx, cycle, cs, s)
		return
	}
	t0 := time.Now()
	a, how, err := cs.Find(ctx, s.Query, false)
	if err != nil || a.IsZero() {
		return
	}
	l.trace(cycle, "topic", fmt.Sprintf("“%s” opens %s (%s)", s.Query, a.Key(), how), time.Since(t0).Milliseconds(), nil)
	var quote string
	l.mu.Lock()
	if l.meeting != nil {
		quote = evidenceLine(s.Query, l.meeting.RecentLines(focusWindow))
	}
	l.mu.Unlock()
	l.showTopic(cycle, a, "“"+s.Query+"”", -1, quote)
}

func (l *Live) showTopic(cycle int, topic knowledge.Anchor, why string, spokenAgo float64, quote string) {
	l.mu.Lock()
	now := l.clockLocked()
	kept := []shownTopic{{id: topic, why: why, at: time.Now(), spokenAgo: spokenAgo, quote: quote, mentioned: now}}
	for _, t := range l.topics {
		if t.id == topic {
			if l.eng.Cfg.Live.Focus {
				// Raised again: to the front, with the latest line.
				t.mentioned = now
				if quote != "" {
					t.quote = quote
				}
			}
			// Already up: keep it where the conversation first raised it.
			kept[0] = t
			continue
		}
		kept = append(kept, t)
	}
	// Raised again after it was pushed off: back on the panel.
	dropped := l.dropped[:0:0]
	for _, t := range l.dropped {
		if t.id == topic {
			continue
		}
		dropped = append(dropped, t)
	}
	if len(kept) > maxTopics {
		if l.eng.Cfg.Live.Focus && l.eng.Cfg.Live.KeepTopics > 0 {
			dropped = append(append([]shownTopic(nil), kept[maxTopics:]...), dropped...)
			if len(dropped) > l.eng.Cfg.Live.KeepTopics {
				dropped = dropped[:l.eng.Cfg.Live.KeepTopics]
			}
		}
		kept = kept[:maxTopics]
	}
	l.dropped = dropped
	same := len(l.topics) > 0 && l.topics[0].id == topic
	l.topics = kept
	due := same && l.eng.Cfg.Live.Focus && l.since(l.topicsAt) >= focusRefresh
	l.mu.Unlock()
	if !same || due {
		l.emitTopics(cycle)
	}
}

// emitTopics rebuilds every card on the panel under the current scope and
// sends them: a scope change (screen sharing) narrows them at once.
func (l *Live) emitTopics(cycle int) {
	t0 := time.Now()
	l.mu.Lock()
	cs, shown, scopeName := l.topicIx, append([]shownTopic(nil), l.topics...), l.scope
	dropped := append([]shownTopic(nil), l.dropped...)
	l.topicsAt = l.now()
	clock := l.clockLocked()
	var window string
	if l.meeting != nil {
		window = l.meeting.Recent(focusWindow)
	}
	focus := l.eng.Cfg.Live.Focus
	// A replayed meeting sees only what was known when it happened.
	now := time.Now()
	if l.mode == "replay" && l.meeting != nil && !l.meeting.Session.CreatedAt.IsZero() {
		now = l.meeting.Session.CreatedAt
	}
	l.mu.Unlock()
	out, kept := []TopicCard{}, []TopicCard{}
	if cs != nil {
		if sc, err := l.eng.Scope(scopeName); err == nil {
			sts, _ := l.eng.Store.ThreadStatuses(sc)
			build := func(t shownTopic) *TopicCard {
				c, err := l.cardOf(cs, sc, scopeName, t.id, now)
				if err != nil || c == nil {
					return nil
				}
				// Collapsing sensitive cards is for a shared screen; in private
				// they show as they are.
				if c.Collapsed && scopeName == "owner" {
					cc := *c
					cc.Collapsed = false
					c = &cc
				}
				tc := TopicCard{Card: c, Why: t.why, At: t.at, SpokenAgo: t.spokenAgo}
				if st, ok := sts[c.TopicID]; ok && !c.Collapsed && !st.AsOf.After(now) {
					var s knowledge.Status
					if json.Unmarshal([]byte(st.Body), &s) == nil {
						tc.Now = knowledge.FirstSentence(knowledge.StripIDs(s.Now))
					}
				}
				if focus {
					tc.Focus, tc.Quote, tc.MentionedAgo = true, t.quote, clock-t.mentioned
					tc.Stale = !c.Last.IsZero() && now.Sub(c.Last) > staleAfter
				}
				return &tc
			}
			for _, t := range shown {
				if tc := build(t); tc != nil {
					out = append(out, *tc)
				}
			}
			for _, t := range dropped {
				if tc := build(t); tc != nil {
					tc.Kept = true
					kept = append(kept, *tc)
				}
			}
		}
	}
	pick := focus && l.picking()
	switch {
	case pick:
		l.pickedCards(out, window)
	case focus:
		l.focusCards(out, window)
	}
	// With relevance, cards hold places by how well they fit, competing
	// with the lookups.
	l.mu.Lock()
	rel := l.rel
	l.mu.Unlock()
	var resend []Brief
	if focus && rel != nil && !pick {
		var q []float32
		if strings.TrimSpace(window) != "" {
			q = l.eng.Embed(l.ctxOrBackground(), window)
		}
		for i := range out {
			if out[i].Lines == nil {
				out[i].Lines = l.closestLines(out[i].Card, q)
			}
			l.offer(cycle, "card:"+out[i].Anchor.Key(), cardText(out[i]), nil)
		}
		resend, _ = l.choose()
		for i := range out {
			out[i].Active, _, out[i].Fit = l.placeOf("card:" + out[i].Anchor.Key())
		}
		sort.SliceStable(out, func(a, b int) bool {
			if out[a].Active != out[b].Active {
				return out[a].Active
			}
			return out[a].Fit > out[b].Fit
		})
	}
	if pick {
		l.mu.Lock()
		for i := range kept {
			if b := l.pk.briefs[kept[i].TopicID]; b != nil && b.scope == scopeName {
				kept[i].Headline, kept[i].Raise, kept[i].RaiseMore = b.Headline, b.Raise, b.More
			}
			kept[i].Rank = l.pk.menu[kept[i].TopicID]
		}
		l.mu.Unlock()
		l.briefCards(cycle, out, kept)
	}
	out = append(out, kept...)
	var titles []string
	for _, c := range out {
		titles = append(titles, c.Title)
	}
	l.trace(cycle, "topic", fmt.Sprintf("%d topic card(s) under %s", len(out), scopeName), time.Since(t0).Milliseconds(), map[string]any{"cards": titles})
	l.emit("live:topics", out)
	for _, b := range resend {
		b.Rated = true
		l.emit("live:brief", b)
	}
}

// focusCards marks the cards to keep open (the most recently raised, a
// fresh subject before an old one, within focusActive) and picks each
// one's lines closest to the conversation.
func (l *Live) focusCards(cards []TopicCard, window string) {
	order := make([]int, len(cards))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := cards[order[a]], cards[order[b]]
		if x.Stale != y.Stale {
			return !x.Stale
		}
		return x.MentionedAgo < y.MentionedAgo
	})
	open := 0
	for _, i := range order {
		if open < focusOpen && cards[i].MentionedAgo <= focusActive {
			cards[i].Active = true
			open++
		}
	}
	sort.SliceStable(cards, func(a, b int) bool {
		if cards[a].Active != cards[b].Active {
			return cards[a].Active
		}
		return cards[a].MentionedAgo < cards[b].MentionedAgo
	})
	var q []float32
	if strings.TrimSpace(window) != "" {
		q = l.eng.Embed(l.ctxOrBackground(), window)
	}
	for i := range cards {
		if cards[i].Active {
			cards[i].Lines = l.closestLines(cards[i].Card, q)
		}
	}
}

// closestLines ranks a card's lines by how close they are to the
// conversation (q); without a vector, the card's own order. Due and
// overdue items of the owner's are kept among them.
func (l *Live) closestLines(c *knowledge.Card, q []float32) []FocusLine {
	var all []FocusLine
	for _, s := range c.Sections {
		for _, ln := range s.Lines {
			all = append(all, FocusLine{CardLine: ln, Section: s.Name})
		}
	}
	if len(all) == 0 {
		return nil
	}
	if q != nil {
		vecs := l.lineVectors(all)
		for i := range all {
			if v := vecs[all[i].Text]; v != nil {
				all[i].Score = cosine(q, v)
			}
		}
		sort.SliceStable(all, func(a, b int) bool { return all[a].Score > all[b].Score })
	}
	return all[:min(len(all), focusLines)]
}

// lineVectors embeds card lines once each, for the meeting.
func (l *Live) lineVectors(lines []FocusLine) map[string][]float32 {
	l.mu.Lock()
	if l.lineVecs == nil {
		l.lineVecs = map[string][]float32{}
	}
	out := map[string][]float32{}
	var todo []string
	for _, ln := range lines {
		if v, ok := l.lineVecs[ln.Text]; ok {
			out[ln.Text] = v
		} else if _, dup := out[ln.Text]; !dup {
			out[ln.Text] = nil
			todo = append(todo, ln.Text)
		}
	}
	l.mu.Unlock()
	if len(todo) == 0 || l.eng.Embedder == nil {
		return out
	}
	vs, err := l.eng.Embedder.Embed(l.ctxOrBackground(), todo, false)
	if err != nil || len(vs) != len(todo) {
		return out
	}
	l.mu.Lock()
	for i, t := range todo {
		l.lineVecs[t], out[t] = vs[i], vs[i]
	}
	l.mu.Unlock()
	return out
}

func (l *Live) ctxOrBackground() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx != nil {
		return l.ctx
	}
	return context.Background()
}

func cosine(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		if i >= len(b) {
			break
		}
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return d / math.Sqrt(na*nb)
}
