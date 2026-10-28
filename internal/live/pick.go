package live

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Picking topic cards (live.pick, research log 2026-10-28). Lookups,
// trigger terms and the talk itself put topics forward as candidates; every
// pickEvery a classifier reads the last pickSpan of the conversation and
// picks at most pickOpen of the strongest candidates that it is about, or
// none. Matching each lookup to its nearest topic put 12% of cards on the
// subject; picking among candidates, 80–90%.

const (
	pickSpan  = 180.0 // seconds of conversation the candidates come from and the picker reads
	pickEvery = 20.0  // meeting seconds between picks
	pickCands = 12    // candidates the picker chooses from
	pickOpen  = 2     // cards picked at once, at most
	pickNear  = 3     // topics a lookup puts forward
	pickTalk  = 5     // topics the talk itself puts forward
	pickTerm  = 100.0 // a trigger term said: always a candidate
	// pickAgain: a topic picked this recently, picked again, keeps its
	// brief; after longer the conversation has moved, and it is written
	// again.
	pickAgain = 600.0
	// menuOpen: cards kept from earlier that the menu shows by default,
	// most worth raising first; menuEvery, how often it is ranked again.
	menuOpen  = 5
	menuEvery = 120.0
)

// candHit is one vote for a topic: who put it forward, when, how strongly.
type candHit struct {
	at    float64
	topic int64
	w     float64
	why   string
}

// cardBrief is what the brief model wrote for a picked card.
type cardBrief struct {
	Headline string `json:"headline"`
	Raise    string `json:"raise"`
	at       float64
	// scope the card was read under: a brief written in private is not
	// shown on a shared screen.
	scope string
}

type pickState struct {
	hits     []candHit
	at       float64 // meeting clock of the last pick
	busy     bool
	picked   map[int64]float64 // the latest pick: topic → probability
	briefs   map[int64]*cardBrief
	briefing map[int64]bool
	menu     map[int64]int // kept cards: rank, 1 = most worth raising
	menuKey  string
	menuAt   float64
	menuBusy bool
	// options caches each candidate's option text for the meeting, by
	// scope ("" when the scope can't see it): candidates repeat from one
	// pick to the next, and building their cards dominated a pick.
	options map[string]string
	// cards: a replay's cards, built once: what was known as of the
	// meeting doesn't change during it.
	cards map[string]*knowledge.Card
	// statuses: the scope's written statuses, read once per meeting.
	statuses      map[int64]store.ThreadStatusRow
	statusesScope string
}

func newPickState() pickState {
	return pickState{at: math.Inf(-1), menuAt: math.Inf(-1), picked: map[int64]float64{},
		briefs: map[int64]*cardBrief{}, briefing: map[int64]bool{}, menu: map[int64]int{}, options: map[string]string{}, cards: map[string]*knowledge.Card{}}
}

// picking: live.pick is on and has its classifier.
func (l *Live) picking() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pickingLocked()
}

func (l *Live) pickingLocked() bool {
	return l.eng.Cfg.Live.Pick.Enabled && l.eng.Cfg.Live.Focus && l.rel != nil
}

func (l *Live) pickMin() float64 {
	if m := l.eng.Cfg.Live.Pick.MinP; m > 0 {
		return m
	}
	return 0.4
}

// knownAtLocked is the time the knowledge is read as of: a replayed
// meeting sees only what was known when it happened.
func (l *Live) knownAtLocked() time.Time {
	if l.mode == "replay" && l.meeting != nil && !l.meeting.Session.CreatedAt.IsZero() {
		return l.meeting.Session.CreatedAt
	}
	return time.Now()
}

// candidate records a vote for a topic.
func (l *Live) candidate(topic int64, w float64, why string) {
	if topic == 0 {
		return
	}
	l.mu.Lock()
	now := l.clockLocked()
	l.pk.hits = append(l.pk.hits, candHit{at: now, topic: topic, w: w, why: why})
	// Votes older than the span no longer count.
	i := 0
	for i < len(l.pk.hits) && now-l.pk.hits[i].at > pickSpan {
		i++
	}
	l.pk.hits = l.pk.hits[i:]
	l.mu.Unlock()
}

// lookupCandidates puts a lookup's nearest topics forward.
func (l *Live) lookupCandidates(ctx context.Context, cycle int, cs *engine.Cards, s Suggestion) {
	ts := cs.Near(ctx, s.Query, pickNear)
	for i, t := range ts {
		l.candidate(t, 1/float64(i+1), "“"+s.Query+"”")
	}
	l.trace(cycle, "topic", fmt.Sprintf("“%s” puts forward %d topic(s)", s.Query, len(ts)), 0, ts)
	l.maybePick(cycle)
}

// maybePick picks again when it is due.
func (l *Live) maybePick(cycle int) {
	l.mu.Lock()
	now := l.clockLocked()
	due := l.pickingLocked() && l.meeting != nil && !l.pk.busy && now-l.pk.at >= pickEvery
	if due {
		l.pk.busy, l.pk.at = true, now
	}
	l.mu.Unlock()
	if due {
		l.spawn(func() { l.pick(cycle) })
	}
}

// pick asks the classifier which candidates the conversation is about, and
// shows them.
func (l *Live) pick(cycle int) {
	defer func() {
		l.mu.Lock()
		l.pk.busy = false
		l.mu.Unlock()
	}()
	t0 := time.Now()
	ctx := l.ctxOrBackground()
	l.mu.Lock()
	cs, scopeName, rel, m := l.topicIx, l.scope, l.rel, l.meeting
	now, asOf := l.clockLocked(), l.knownAtLocked()
	hits := append([]candHit(nil), l.pk.hits...)
	var window, title string
	if m != nil {
		window, title = m.Recent(pickSpan), m.Session.Title
	}
	l.mu.Unlock()
	if cs == nil || rel == nil || strings.TrimSpace(window) == "" {
		return
	}
	score, why := map[int64]float64{}, map[int64]string{}
	add := func(t int64, w float64, r string) {
		score[t] += w
		if why[t] == "" {
			why[t] = r
		}
	}
	for _, h := range hits {
		if now-h.at <= pickSpan {
			add(h.topic, h.w, h.why)
		}
	}
	tNear := time.Now()
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
	nearMs := time.Since(tNear).Milliseconds()
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return
	}
	tOpts := time.Now()
	var sts map[int64]store.ThreadStatusRow
	opts := map[string]string{"none": "None of these topics is what the conversation is about"}
	titles := map[int64]string{}
	var ids []int64
	for _, t := range order {
		if len(ids) == pickCands {
			break
		}
		key := fmt.Sprintf("%s/%d", scopeName, t)
		l.mu.Lock()
		s, ok := l.pk.options[key]
		l.mu.Unlock()
		if !ok {
			if c, err := l.cardOf(cs, sc, scopeName, knowledge.Anchor{Topic: t}, asOf); err == nil && c != nil {
				s = fmt.Sprintf("%s (last discussed %s)", c.Title, c.Last.Format("2006-01-02"))
				if sts == nil {
					sts = l.pickStatuses(scopeName)
				}
				if st, ok := sts[c.TopicID]; ok && !c.Collapsed && !st.AsOf.After(asOf) {
					var v knowledge.Status
					if json.Unmarshal([]byte(st.Body), &v) == nil {
						if n := knowledge.FirstSentence(knowledge.StripIDs(v.Now)); n != "" {
							s += " — " + n
						}
					}
				}
			}
			l.mu.Lock()
			l.pk.options[key] = s
			l.mu.Unlock()
		}
		if s == "" {
			continue
		}
		opts[fmt.Sprintf("t%d", t)] = s
		titles[t], _, _ = strings.Cut(s, " (last discussed")
		ids = append(ids, t)
	}
	optsMs := time.Since(tOpts).Milliseconds()
	if len(ids) == 0 {
		return
	}
	tJev := time.Now()
	a, err := rel.Classify(ctx, map[string]any{"meeting": title, "conversation": window},
		map[string]provider.Question{"topic": {Type: "choice", Instructions: pickQuestion, Options: opts}})
	if err != nil {
		l.trace(cycle, "topic", "pick failed", time.Since(t0).Milliseconds(), err.Error())
		return
	}
	probs := a["topic"].Probabilities
	type pr struct {
		t int64
		p float64
	}
	var ranked []pr
	for _, t := range ids {
		ranked = append(ranked, pr{t, probs[fmt.Sprintf("t%d", t)]})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].p > ranked[j].p })
	picked := map[int64]float64{}
	var names []string
	for _, r := range ranked {
		if len(picked) == pickOpen || r.p < l.pickMin() {
			break
		}
		picked[r.t] = r.p
		names = append(names, fmt.Sprintf("%s %.2f", titles[r.t], r.p))
	}
	var cands []string
	for _, r := range ranked {
		cands = append(cands, fmt.Sprintf("%s %.2f (%s)", titles[r.t], r.p, why[r.t]))
	}
	l.trace(cycle, "topic", fmt.Sprintf("picked %d of %d: %s", len(picked), len(ids), strings.Join(names, "; ")), time.Since(t0).Milliseconds(),
		map[string]any{"none": probs["none"], "candidates": cands, "nearMs": nearMs, "optionsMs": optsMs, "classifyMs": time.Since(tJev).Milliseconds()})
	l.mu.Lock()
	l.pk.picked = picked
	l.mu.Unlock()
	// The best last, so it ends up first.
	for i := len(ranked) - 1; i >= 0; i-- {
		if p, ok := picked[ranked[i].t]; ok {
			l.showTopic(cycle, knowledge.Anchor{Topic: ranked[i].t}, fmt.Sprintf("picked %.2f · %s", p, why[ranked[i].t]), -1, "")
		}
	}
	l.emitTopics(cycle)
}

const pickQuestion = "`conversation` is the last 3 minutes of a live meeting (speech recognition; \"You\" is the executive). The options are topics from the executive's past meetings. Which one is the conversation right now actually about, so that showing its notes would help them in this conversation? Choose \"none\" if the conversation is about something none of them covers, about small talk, or only touches one in passing (a shared word or name is not enough)."

// cardOf builds a topic's card under a scope; in a replay, once.
func (l *Live) cardOf(cs *engine.Cards, sc sensitivity.Scope, scopeName string, a knowledge.Anchor, now time.Time) (*knowledge.Card, error) {
	l.mu.Lock()
	replay := l.mode == "replay"
	key := scopeName + "/" + a.Key()
	c, ok := l.pk.cards[key]
	l.mu.Unlock()
	if replay && ok {
		return c, nil
	}
	c, err := cs.Card(sc, a, now)
	if err == nil && replay {
		l.mu.Lock()
		l.pk.cards[key] = c
		l.mu.Unlock()
	}
	return c, err
}

// pickStatuses is the scope's written statuses, read once per meeting.
func (l *Live) pickStatuses(scopeName string) map[int64]store.ThreadStatusRow {
	l.mu.Lock()
	sts, ok := l.pk.statuses, l.pk.statusesScope == scopeName && l.pk.statuses != nil
	l.mu.Unlock()
	if ok {
		return sts
	}
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return nil
	}
	sts, _ = l.eng.Store.ThreadStatuses(sc)
	l.mu.Lock()
	l.pk.statuses, l.pk.statusesScope = sts, scopeName
	l.mu.Unlock()
	return sts
}

// pickedCards marks the picked cards open, best first, with their lines
// and briefs; the rest stay as one line.
func (l *Live) pickedCards(cards []TopicCard, window string) {
	l.mu.Lock()
	picked := l.pk.picked
	briefs := map[int64]cardBrief{}
	for _, c := range cards {
		if b := l.pk.briefs[c.TopicID]; b != nil && b.scope == l.scope {
			briefs[c.TopicID] = *b
		}
	}
	l.mu.Unlock()
	var q []float32
	for i := range cards {
		p, ok := picked[cards[i].TopicID]
		cards[i].Active, cards[i].Fit = ok, p
		if b, ok := briefs[cards[i].TopicID]; ok {
			cards[i].Headline, cards[i].Raise = b.Headline, b.Raise
		}
		if ok {
			if q == nil && strings.TrimSpace(window) != "" {
				q = l.eng.Embed(l.ctxOrBackground(), window)
			}
			cards[i].Lines = l.closestLines(cards[i].Card, q)
		}
	}
	sort.SliceStable(cards, func(a, b int) bool {
		if cards[a].Active != cards[b].Active {
			return cards[a].Active
		}
		if cards[a].Fit != cards[b].Fit {
			return cards[a].Fit > cards[b].Fit
		}
		return cards[a].MentionedAgo < cards[b].MentionedAgo
	})
}

// briefCards writes briefs for the open cards that lack a fresh one, and
// ranks the kept cards for the menu when they have changed; each send the
// cards again when done.
func (l *Live) briefCards(cycle int, open, kept []TopicCard) {
	llm := l.briefLLM
	if llm == nil {
		return
	}
	l.mu.Lock()
	now := l.clockLocked()
	var todo []TopicCard
	for _, c := range open {
		b := l.pk.briefs[c.TopicID]
		if !c.Active || c.Collapsed || l.pk.briefing[c.TopicID] || b != nil && b.scope == l.scope && now-b.at < pickAgain {
			continue
		}
		l.pk.briefing[c.TopicID] = true
		todo = append(todo, c)
	}
	var menu []TopicCard
	var keys []string
	for _, c := range kept {
		if b := l.pk.briefs[c.TopicID]; b != nil && b.Raise != "" && b.scope == l.scope {
			menu = append(menu, c)
			keys = append(keys, fmt.Sprint(c.TopicID))
		}
	}
	key := strings.Join(keys, ",")
	rank := len(menu) > menuOpen && !l.pk.menuBusy && key != l.pk.menuKey && now-l.pk.menuAt >= menuEvery
	if rank {
		l.pk.menuBusy, l.pk.menuAt = true, now
	}
	var window, people string
	if l.meeting != nil {
		window, people = l.meeting.Recent(pickSpan), strings.Join(l.meeting.Speakers(), ", ")
	}
	title, scopeName := "", l.scope
	if l.meeting != nil {
		title = l.meeting.Session.Title
	}
	l.mu.Unlock()
	for _, c := range todo {
		c := c
		l.spawn(func() { l.writeBrief(cycle, llm, c, scopeName, title, people, window) })
	}
	if rank {
		l.spawn(func() { l.rankMenu(cycle, llm, menu, key, title, people, window) })
	}
}

const cardBriefPrompt = `You help an executive during a live meeting. A topic from their notes on past meetings has come up. Using only the notes below, write:
- headline: where it stands, at most 18 words, concrete (names, numbers, dates)
- raise: the one thing worth raising or asking about it in THIS meeting, with these people, at most 18 words; empty if nothing. Don't suggest what the conversation has just covered.

Meeting: %s
People speaking: %s
The last 3 minutes (speech recognition; "You" is the executive):
%s

Topic: %s
Last discussed: %s, across %d meeting(s)
Status as written: %s
The notes (section, date, who: statement):
%s

Reply as JSON only: {"headline": "...", "raise": "..."}`

func (l *Live) writeBrief(cycle int, llm provider.LLM, c TopicCard, scopeName, title, people, window string) {
	t0 := time.Now()
	var lines []string
	for _, s := range c.Sections {
		for _, ln := range s.Lines {
			due := ""
			if ln.Due != "" {
				due = " (" + ln.Due + ")"
			}
			lines = append(lines, fmt.Sprintf("[%s] %s %s: %s%s", s.Name, ln.Said.Format("2006-01-02"), ln.Who, ln.Text, due))
		}
	}
	now := c.Now
	if now == "" {
		now = "(none)"
	}
	prompt := fmt.Sprintf(cardBriefPrompt, title, people, window, c.Title, c.Last.Format("2006-01-02"), c.Meetings, now, strings.Join(lines, "\n"))
	out, err := llm.Complete(l.ctxOrBackground(), []provider.Message{{Role: "user", Content: prompt}},
		provider.ChatOptions{JSON: true, Temperature: 0.3, MaxTokens: 4000}, nil)
	var b cardBrief
	if err == nil {
		err = json.Unmarshal([]byte(jsonObject(out)), &b)
	}
	l.mu.Lock()
	delete(l.pk.briefing, c.TopicID)
	if err == nil {
		b.at, b.scope = l.clockLocked(), scopeName
		l.pk.briefs[c.TopicID] = &b
	}
	l.mu.Unlock()
	if err != nil {
		l.trace(cycle, "topic", "brief failed: "+c.Title, time.Since(t0).Milliseconds(), err.Error())
		return
	}
	l.trace(cycle, "topic", "brief: "+c.Title, time.Since(t0).Milliseconds(), map[string]any{"headline": b.Headline, "raise": b.Raise})
	l.emitTopics(cycle)
}

const menuPrompt = `You help an executive during a live meeting. Below are topics that came up earlier in it, each with something they could raise. Which are most worth raising before the meeting ends, with these people, given where the conversation is now? Choose at most %d, best first; leave out what has been settled in the conversation or isn't worth their time.

Meeting: %s
People speaking: %s
The last 3 minutes (speech recognition; "You" is the executive):
%s

Topics:
%s

Reply as JSON only: {"top": ["t123", ...]}`

func (l *Live) rankMenu(cycle int, llm provider.LLM, menu []TopicCard, key, title, people, window string) {
	t0 := time.Now()
	defer func() {
		l.mu.Lock()
		l.pk.menuBusy = false
		l.mu.Unlock()
	}()
	l.mu.Lock()
	var list []string
	for _, c := range menu {
		list = append(list, fmt.Sprintf("t%d: %s — %s", c.TopicID, c.Title, l.pk.briefs[c.TopicID].Raise))
	}
	l.mu.Unlock()
	prompt := fmt.Sprintf(menuPrompt, menuOpen, title, people, window, strings.Join(list, "\n"))
	out, err := llm.Complete(l.ctxOrBackground(), []provider.Message{{Role: "user", Content: prompt}},
		provider.ChatOptions{JSON: true, Temperature: 0, MaxTokens: 4000}, nil)
	var r struct {
		Top []string `json:"top"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(jsonObject(out)), &r)
	}
	if err != nil {
		l.trace(cycle, "topic", "menu ranking failed", time.Since(t0).Milliseconds(), err.Error())
		return
	}
	rank := map[int64]int{}
	for _, id := range r.Top {
		var t int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(id), "t"), "%d", &t); err == nil && rank[t] == 0 && len(rank) < menuOpen {
			rank[t] = len(rank) + 1
		}
	}
	l.mu.Lock()
	l.pk.menu, l.pk.menuKey = rank, key
	l.mu.Unlock()
	l.trace(cycle, "topic", fmt.Sprintf("menu: %d of %d worth raising", len(rank), len(menu)), time.Since(t0).Milliseconds(), r.Top)
	l.emitTopics(cycle)
}

// jsonObject is the outermost {...} of a model's reply.
func jsonObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
