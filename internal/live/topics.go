package live

import (
	"context"
	"fmt"
	"time"

	"github.com/anielsud/musubi/internal/knowledge"
)

// TopicCard is a topic's card on the panel, and why it is there.
type TopicCard struct {
	*knowledge.Card
	Why string    `json:"why"`
	At  time.Time `json:"at"`
	// SpokenAgo is how long before the card was ready its trigger was said
	// (meeting seconds); -1 when it came from a lookup, not a word.
	SpokenAgo float64 `json:"spokenAgo"`
}

type shownTopic struct {
	id        knowledge.Anchor
	why       string
	at        time.Time
	spokenAgo float64
}

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
	for _, h := range cs.Index.TermsIn(text) {
		said := evidenceAt(h.Term, lines)
		ago := -1.0
		if said >= 0 {
			ago = l.clock() - said
		}
		l.showTopic(cycle, h.Anchor, "says "+h.Term, ago)
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
	t0 := time.Now()
	a, how, err := cs.Find(ctx, s.Query, false)
	if err != nil || a.IsZero() {
		return
	}
	l.trace(cycle, "topic", fmt.Sprintf("“%s” opens %s (%s)", s.Query, a.Key(), how), time.Since(t0).Milliseconds(), nil)
	l.showTopic(cycle, a, "“"+s.Query+"”", -1)
}

func (l *Live) showTopic(cycle int, topic knowledge.Anchor, why string, spokenAgo float64) {
	l.mu.Lock()
	kept := []shownTopic{{id: topic, why: why, at: time.Now(), spokenAgo: spokenAgo}}
	for _, t := range l.topics {
		if t.id == topic {
			// Already up: keep it where the conversation first raised it.
			kept[0] = t
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) > maxTopics {
		kept = kept[:maxTopics]
	}
	same := len(l.topics) > 0 && l.topics[0].id == topic
	l.topics = kept
	l.mu.Unlock()
	if !same {
		l.emitTopics(cycle)
	}
}

// emitTopics rebuilds every card on the panel under the current scope and
// sends them: a scope change (screen sharing) narrows them at once.
func (l *Live) emitTopics(cycle int) {
	t0 := time.Now()
	l.mu.Lock()
	cs, shown, scopeName := l.topicIx, append([]shownTopic(nil), l.topics...), l.scope
	// A replayed meeting sees only what was known when it happened.
	now := time.Now()
	if l.mode == "replay" && l.meeting != nil && !l.meeting.Session.CreatedAt.IsZero() {
		now = l.meeting.Session.CreatedAt
	}
	l.mu.Unlock()
	out := []TopicCard{}
	if cs != nil {
		if sc, err := l.eng.Scope(scopeName); err == nil {
			for _, t := range shown {
				c, err := cs.Card(sc, t.id, now)
				if err != nil || c == nil {
					continue
				}
				out = append(out, TopicCard{Card: c, Why: t.why, At: t.at, SpokenAgo: t.spokenAgo})
			}
		}
	}
	var titles []string
	for _, c := range out {
		titles = append(titles, c.Title)
	}
	l.trace(cycle, "topic", fmt.Sprintf("%d topic card(s) under %s", len(out), scopeName), time.Since(t0).Milliseconds(), map[string]any{"cards": titles})
	l.emit("live:topics", out)
}
