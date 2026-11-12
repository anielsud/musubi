package live

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/provider"
)

// Relevance (live.relevance): what takes the panel's places is chosen by
// how well it fits the conversation, as a classifier judges it, rather
// than by being newest. Topic cards and lookups compete for the same
// places. Something on the panel keeps its place while it still fits; a
// newcomer takes one only by fitting better (relevanceMargin), so the
// panel doesn't turn over with every lookup.

const (
	places          = 4    // places on the panel, cards and lookups together
	relevanceMargin = 0.15 // how much better a newcomer must fit to displace
	relevanceRecent = 180.0
	// rescoreAfter: what is shown, or in the running, is judged again
	// against the conversation this often (meeting time).
	rescoreAfter = 20.0
)

// place is something competing for a place on the panel.
type place struct {
	key      string // card:<anchor> or brief:<query>
	text     string // what the classifier reads
	score    float64
	scoredAt float64 // meeting clock
	offered  float64 // when it was last raised
	open     bool
	wasOpen  bool
	brief    *Brief // a lookup, to send again when its place changes
}

const relevanceQuestion = "`conversation` is the last 90 seconds of a live meeting (speech recognition; \"You\" is the executive). `item` is something a side panel shows the executive right now, from their past meetings. Would glancing at it give them something relevant to what is being discussed right now — a fact, figure, decision, commitment or history they could use in this conversation? No if it is about something else, about small talk, only repeats what was just said, or too vague to use."

// relevance makes the classifier from live.relevance; nil when off.
func relevance(c provider.Classifier, provName string, chat *provider.Chat) (provider.Classifier, error) {
	switch provName {
	case "":
		return nil, nil
	case "jev", "clef":
		return c, nil
	case "decider":
		if chat == nil {
			return nil, fmt.Errorf("live.relevance: provider decider needs [llm]")
		}
		return provider.DeciderClassifier{D: chat, Desc: chat.Model}, nil
	}
	return nil, fmt.Errorf("live.relevance: unknown provider %q", provName)
}

func (l *Live) relevanceMin() float64 {
	if m := l.eng.Cfg.Live.Relevance.MinP; m > 0 {
		return m
	}
	return 0.5
}

// judge asks how well text fits the conversation; -1 if it can't say.
func (l *Live) judge(cycle int, key, text, window string) float64 {
	if strings.TrimSpace(window) == "" {
		return 0
	}
	t0 := time.Now()
	a, err := l.rel.Classify(l.ctxOrBackground(), map[string]any{"conversation": window, "item": text},
		map[string]provider.Question{"helps": {Type: "noul", Instructions: relevanceQuestion}})
	if err != nil {
		l.trace(cycle, "relevance", "relevance failed: "+key, time.Since(t0).Milliseconds(), err.Error())
		return -1
	}
	p := a["helps"].Noul
	l.trace(cycle, "relevance", fmt.Sprintf("%s fits %.2f", key, p), time.Since(t0).Milliseconds(), nil)
	return p
}

// offer puts something up for a place (or updates it) and judges it.
func (l *Live) offer(cycle int, key, text string, b *Brief) {
	l.mu.Lock()
	if l.places == nil {
		l.places = map[string]*place{}
	}
	now := l.clockLocked()
	p := l.places[key]
	if p == nil {
		p = &place{key: key, scoredAt: -1e9}
		l.places[key] = p
	}
	changed := p.text != text
	p.text, p.offered = text, now
	if b != nil {
		bb := *b
		p.brief = &bb
	}
	window := ""
	if l.meeting != nil {
		window = l.meeting.Recent(focusWindow)
	}
	due := changed || now-p.scoredAt >= rescoreAfter
	l.mu.Unlock()
	if due {
		s := l.judge(cycle, key, text, window)
		l.mu.Lock()
		if s >= 0 {
			p.score, p.scoredAt = s, now
		}
		l.mu.Unlock()
	}
}

// offerScored puts something up for a place with its fit already judged.
func (l *Live) offerScored(key, text string, b *Brief, score float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.places == nil {
		l.places = map[string]*place{}
	}
	now := l.clockLocked()
	p := l.places[key]
	if p == nil {
		p = &place{key: key}
		l.places[key] = p
	}
	p.text, p.offered, p.score, p.scoredAt = text, now, score, now
	if b != nil {
		bb := *b
		p.brief = &bb
	}
}

// rescore judges again what is shown or still in the running, as the
// conversation moves.
func (l *Live) rescore(cycle int) {
	l.mu.Lock()
	now := l.clockLocked()
	window := ""
	if l.meeting != nil {
		window = l.meeting.Recent(focusWindow)
	}
	var todo []*place
	for _, p := range l.places {
		if (p.open || now-p.offered <= relevanceRecent) && now-p.scoredAt >= rescoreAfter {
			todo = append(todo, p)
		}
	}
	l.mu.Unlock()
	for _, p := range todo {
		s := l.judge(cycle, p.key, p.text, window)
		l.mu.Lock()
		if s >= 0 {
			p.score, p.scoredAt = s, now
		}
		l.mu.Unlock()
	}
}

// choose fills the places: what is open stays while it fits; free places
// go to the best fitting above the bar; a newcomer displaces the weakest
// only by fitting clearly better. It returns the lookups whose place
// changed, to send again, and whether a card's did.
func (l *Live) choose() ([]Brief, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now, min := l.clockLocked(), l.relevanceMin()
	var open, rivals []*place
	for k, p := range l.places {
		if !p.open && now-p.offered > relevanceRecent {
			if !p.wasOpen {
				delete(l.places, k)
			}
			continue
		}
		if p.open && p.score >= min {
			open = append(open, p)
		} else if p.score >= min {
			rivals = append(rivals, p)
		}
	}
	sort.Slice(rivals, func(i, j int) bool { return rivals[i].score > rivals[j].score })
	sort.Slice(open, func(i, j int) bool { return open[i].score > open[j].score })
	for _, r := range rivals {
		if len(open) < places {
			open = append(open, r)
			continue
		}
		if w := open[len(open)-1]; r.score > w.score+relevanceMargin {
			open[len(open)-1] = r
		}
		sort.Slice(open, func(i, j int) bool { return open[i].score > open[j].score })
	}
	keep := map[*place]bool{}
	for _, p := range open {
		keep[p] = true
	}
	var resend []Brief
	cards := false
	for _, p := range l.places {
		was := p.open
		p.open = keep[p]
		if p.open {
			p.wasOpen = true
		}
		if was == p.open {
			continue
		}
		if p.brief != nil {
			b := *p.brief
			b.Open, b.WasOpen, b.Fit = p.open, p.wasOpen, p.score
			resend = append(resend, b)
		} else {
			cards = true
		}
	}
	return resend, cards
}

// placeOf reports a key's place: open, once open, and how well it fits.
func (l *Live) placeOf(key string) (open, wasOpen bool, fit float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.places[key]; p != nil {
		return p.open, p.wasOpen, p.score
	}
	return false, false, 0
}

func briefKey(q string) string { return "brief:" + normalize(q) }

// briefText is what the classifier reads of a lookup.
func briefText(b Brief) string {
	var s strings.Builder
	fmt.Fprintf(&s, "LOOKUP: %s\n", b.Query)
	if b.Quote != "" {
		fmt.Fprintf(&s, "raised by: \"%s\"\n", b.Quote)
	}
	switch {
	case b.Status != nil:
		fmt.Fprintf(&s, "Status of %s as of %s: %s\n", b.Status.Name, b.Status.AsOf.Format("2006-01-02"), b.Status.Now)
		for i, o := range b.Status.Open {
			if i == 3 {
				break
			}
			fmt.Fprintf(&s, "open: %s\n", o)
		}
	case b.Summary != "":
		s.WriteString(b.Summary)
	default:
		for i, h := range b.Hits {
			if i == 3 {
				break
			}
			fmt.Fprintf(&s, "passage: %s\n", excerpt(h.Text, 300))
		}
	}
	return s.String()
}

// cardText is what the classifier reads of a topic card.
func cardText(c TopicCard) string {
	var s strings.Builder
	old := ""
	if c.Stale {
		old = "old · "
	}
	fmt.Fprintf(&s, "CARD: %s (%slast discussed %s, %d meetings)\n", c.Title, old, c.Last.Format("2006-01-02"), c.Meetings)
	if c.Quote != "" {
		fmt.Fprintf(&s, "raised by: \"%s\"\n", c.Quote)
	}
	for _, ln := range c.Lines {
		fmt.Fprintf(&s, "[%s] %s", ln.Section, ln.Text)
		if ln.Due != "" {
			fmt.Fprintf(&s, " (%s)", ln.Due)
		}
		s.WriteString("\n")
	}
	return s.String()
}
