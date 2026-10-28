package live

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/anielsud/musubi/internal/knowledge"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Emit sends an event to the UI.
type Emit func(event string, data any)

// Status is what the panel shows about the feed.
type Status struct {
	Connected bool     `json:"connected"` // Tomoe's feed socket is open
	Mode      string   `json:"mode"`      // idle, live, replay, ended
	Session   *Session `json:"session,omitempty"`
	Scope     string   `json:"scope"`
	Lines     int      `json:"lines"`
	Clock     float64  `json:"clock"`
	// Focus: the panel shows what fits the conversation (live.focus);
	// Relevance, that what fits best takes the places (live.relevance).
	Focus     bool `json:"focus"`
	Relevance bool `json:"relevance"`
}

// Brief is what was found for one suggestion.
type Brief struct {
	Query   string      `json:"query"`
	Why     string      `json:"why"`
	Kind    string      `json:"kind"`
	State   string      `json:"state"` // loading, done, empty, error
	Summary string      `json:"summary"`
	Hits    []store.Hit `json:"hits"`
	At      time.Time   `json:"at"`
	// SpokenAgo is how long before the brief was ready the topic was first
	// said (meeting seconds); -1 when the words weren't said as such.
	SpokenAgo float64 `json:"spokenAgo"`
	// Quote is the line of the conversation the lookup is about (focus).
	Quote string `json:"quote,omitempty"`
	// With live.relevance: Rated, that it was judged; Open, that it has a
	// place on the panel; WasOpen, that it had one; Fit, how well it fits.
	Rated   bool    `json:"rated,omitempty"`
	Open    bool    `json:"open,omitempty"`
	WasOpen bool    `json:"wasOpen,omitempty"`
	Fit     float64 `json:"fit,omitempty"`
	// Summarizing: passages are shown and a summary is on its way.
	Summarizing bool `json:"summarizing"`
	// Status is the written status of the thread the lookup is about,
	// shown at once: no model is asked, and no summary follows.
	Status *BriefStatus `json:"status,omitempty"`
}

// BriefStatus is a thread's status as a brief shows it.
type BriefStatus struct {
	Topic   int64     `json:"topic"`
	Name    string    `json:"name"`
	AsOf    time.Time `json:"asOf"`
	Now     string    `json:"now"`
	Changed []string  `json:"changed,omitempty"`
	Open    []string  `json:"open,omitempty"`
}

// liveStatusMin is how well a lookup must match a thread for its status
// to be the brief. On All, specific lookups (a programme, a vendor pilot,
// the board deck) matched their thread at 0.81–0.86, vague ones ("next
// steps", "that customer issue") a loosely related one at 0.77–0.78
// (research log 2026-10-19).
const liveStatusMin = 0.80

// Card summarises an entity named in the conversation.
type Card struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Items     int       `json:"items"`
	LastSeen  time.Time `json:"lastSeen"`
	Neighbors []string  `json:"neighbors"`
	Recent    []string  `json:"recent"`
	Mentions  int       `json:"mentions"`
}

type Live struct {
	eng  *engine.Engine
	emit Emit

	mu        sync.Mutex
	meeting   *Meeting
	connected bool
	mode      string
	scope     string
	sug       *Suggester
	topicIx   *engine.Cards
	topicAsOf time.Time           // topicIx holds only what was said before it (replays)
	spell     knowledge.Spellings // confirmed spelling aliases, applied to lines
	topics    []shownTopic
	dropped   []shownTopic // pushed off by newer cards, most recent first (live.keep_topics)
	// lineVecs caches card lines' vectors for ranking them against the
	// conversation (focus); topicsAt is when the cards were last sent.
	lineVecs map[string][]float32
	topicsAt time.Time
	// rel judges what fits the conversation (relevance.go); places holds
	// what competes for the panel's places.
	rel     provider.Classifier
	places  map[string]*place
	briefed map[string]time.Time
	onSaved func()

	// Replays: timed (a timer at some speed) or clocked (an audio player).
	replay      context.CancelFunc
	replayT0    time.Time
	replaySpeed float64
	clocked     *clockedReplay
	// stepped: a stepped replay (stepped.go); vclk is its meeting clock.
	stepped atomic.Bool
	vclk    atomic.Uint64

	llmBusy sync.Mutex

	ctx             context.Context
	lastModelFinals int
	wake            chan struct{}
	sumQ            chan sumJob
	chips           []Suggestion
	lastModelVer    int
	lastModelRun    time.Time

	traceMu sync.Mutex
	traces  []Trace
	seq     int
	cycle   int
	feed    feedStats
}

func New(eng *engine.Engine, emit Emit, onSaved func()) *Live {
	return &Live{eng: eng, emit: emit, mode: "idle", scope: "owner", briefed: map[string]time.Time{}, onSaved: onSaved,
		ctx: context.Background(), wake: make(chan struct{}, 1), sumQ: make(chan sumJob, 16)}
}

// Reload rebuilds the suggester from the store (after an update). It runs
// on a background goroutine, so a panic in building an index becomes an
// error rather than ending the app (a bad word in one dataset once made the
// app unable to start).
func (l *Live) Reload() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("live index: %v", r)
		}
	}()
	s, err := NewSuggester(l.eng.Store)
	if err != nil {
		return err
	}
	var cs *engine.Cards
	l.mu.Lock()
	asOf := l.topicAsOf
	l.mu.Unlock()
	if l.eng.Cfg.Live.TopicCards {
		if cs, err = l.eng.LoadCardsBefore(asOf); err != nil {
			return err
		}
	}
	sp, err := l.eng.Store.ConfirmedSpellings()
	if err != nil {
		return err
	}
	var rel provider.Classifier
	if c := l.eng.Cfg.Live.Relevance; c.Provider != "" && l.eng.Cfg.Live.Focus {
		var jev provider.Classifier
		if c.Provider == "jev" {
			// It reads the conversation: a hosted model needs the owner's leave.
			if !l.eng.Cfg.Knowledge.AllowHosted {
				return fmt.Errorf("live.relevance: a hosted classifier reads the meeting, so it needs knowledge.allow_hosted")
			}
			jev = provider.NewJev(c.URL, c.Model, c.APIKey, c.PriceIn)
		}
		if rel, err = relevance(jev, c.Provider, l.eng.Chat); err != nil {
			return err
		}
	}
	l.mu.Lock()
	l.rel = rel
	l.sug = s
	l.topicIx = cs
	l.spell = knowledge.Spellings(sp)
	l.mu.Unlock()
	return nil
}

// cardsAsOf gives a replayed meeting a card index of what was said before
// it, and a live one the whole of it: topics threaded from the meeting
// itself, or from later ones, would otherwise answer its lookups.
func (l *Live) cardsAsOf(mode string, start time.Time) {
	var before time.Time
	if mode == "replay" {
		before = start
	}
	l.mu.Lock()
	same := l.topicIx == nil || l.topicAsOf.Equal(before)
	l.mu.Unlock()
	if same {
		return
	}
	cs, err := l.eng.LoadCardsBefore(before)
	if err != nil {
		l.trace(0, "topic", "card index as of the meeting: "+err.Error(), 0, nil)
		return
	}
	l.mu.Lock()
	l.topicIx, l.topicAsOf = cs, before
	l.mu.Unlock()
}

func (l *Live) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := Status{Connected: l.connected, Mode: l.mode, Scope: l.scope, Clock: l.clockLocked(), Focus: l.eng.Cfg.Live.Focus, Relevance: l.rel != nil}
	if l.meeting != nil {
		s := l.meeting.Session
		st.Session = &s
		st.Lines = len(l.meeting.segs)
	}
	return st
}

func (l *Live) pushStatus() { l.emit("live:status", l.Status()) }

// clockLocked is the meeting time now, in seconds from its start.
func (l *Live) clockLocked() float64 {
	switch {
	case l.stepped.Load():
		return l.stepClock()
	case l.clocked != nil:
		return l.clocked.last
	case l.replay != nil:
		return time.Since(l.replayT0).Seconds() * l.replaySpeed
	case l.meeting != nil && !l.meeting.Session.CreatedAt.IsZero():
		return time.Since(l.meeting.Session.CreatedAt).Seconds()
	}
	return 0
}

func (l *Live) clock() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clockLocked()
}

// trace records a processing step for the diagnostics view.
func (l *Live) trace(cycle int, kind, title string, ms int64, detail any) {
	t := Trace{Cycle: cycle, At: time.Now(), Clock: l.clock(), Kind: kind, Title: title, Ms: ms, Detail: detail}
	l.traceMu.Lock()
	l.seq++
	t.Seq = l.seq
	l.traces = append(l.traces, t)
	if len(l.traces) > traceKeep {
		l.traces = l.traces[len(l.traces)-traceKeep:]
	}
	l.traceMu.Unlock()
	l.emit("diag:trace", t)
}

// Traces returns the recent processing steps.
func (l *Live) Traces() []Trace {
	l.traceMu.Lock()
	defer l.traceMu.Unlock()
	return append([]Trace(nil), l.traces...)
}

func (l *Live) resetTraces() {
	l.traceMu.Lock()
	l.traces, l.cycle, l.feed = nil, 0, feedStats{}
	l.traceMu.Unlock()
	l.emit("diag:reset", nil)
}

// SetScope switches what the panel may show, e.g. while sharing a screen.
func (l *Live) SetScope(name string) error {
	if _, err := l.eng.Scope(name); err != nil {
		return err
	}
	l.mu.Lock()
	l.scope = name
	l.briefed = map[string]time.Time{}
	l.lastModelVer = -1
	l.mu.Unlock()
	l.trace(0, "cycle", "scope set to "+name, 0, nil)
	l.pushStatus()
	l.emitTopics(0)
	return nil
}

// Run follows Tomoe's feed and runs the model lane and the summariser until
// ctx ends.
func (l *Live) Run(ctx context.Context) {
	l.mu.Lock()
	l.ctx = ctx
	l.mu.Unlock()
	if err := l.Reload(); err != nil {
		l.emit("live:error", err.Error())
	}
	go Follow(ctx, l.eng.Cfg.Live.TomoeSocket, func(ev Event) { l.handle(ev, "live") }, func(up bool) {
		l.mu.Lock()
		l.connected = up
		l.mu.Unlock()
		state := "disconnected from"
		if up {
			state = "connected to"
		}
		l.trace(0, "feed", state+" Tomoe", 0, nil)
		l.pushStatus()
	})
	go l.summarizer(ctx)
	l.modelLoop(ctx)
}

// handle applies a feed (or replay) event.
func (l *Live) handle(ev Event, mode string) {
	switch ev.Type {
	case "hello":
		l.trace(0, "feed", "Tomoe "+ev.Version+" says hello", 0, nil)
	case "session":
		if ev.Session == nil {
			return
		}
		switch ev.State {
		case "started":
			tomoeTitle := ev.Session.Title
			if t, err := l.eng.Store.EventAt(ev.Session.CreatedAt); err == nil && t != "" {
				ev.Session.Title = t
			}
			l.mu.Lock()
			if mode == "live" {
				if l.replay != nil {
					l.replay()
					l.replay = nil
				}
				l.clocked = nil
			}
			l.mu.Unlock()
			l.cardsAsOf(mode, ev.Session.CreatedAt)
			l.mu.Lock()
			l.meeting = NewMeeting(*ev.Session)
			l.mode = mode
			l.briefed = map[string]time.Time{}
			l.chips = nil
			l.topics = nil
			l.dropped = nil
			l.places = nil
			l.lastModelVer = 0
			l.mu.Unlock()
			l.resetTraces()
			l.trace(0, "feed", "meeting started ("+mode+")", 0, map[string]any{"tomoeTitle": tomoeTitle, "calendarTitle": ev.Session.Title, "id": ev.Session.ID})
			l.emit("live:reset", ev.Session)
		case "stopped":
			l.mu.Lock()
			if l.mode == mode {
				l.mode = "ended"
			}
			l.mu.Unlock()
			l.trace(0, "feed", "meeting stopped", 0, nil)
		case "saved":
			l.trace(0, "feed", "meeting saved; ingesting", 0, nil)
			if l.onSaved != nil && l.eng.Cfg.Live.UpdateOnSave {
				go l.onSaved()
			}
		}
		l.pushStatus()
	case "segment":
		if ev.Segment == nil {
			return
		}
		l.mu.Lock()
		if l.meeting == nil || (ev.SessionID != "" && ev.SessionID != l.meeting.Session.ID) {
			l.meeting = NewMeeting(Session{ID: ev.SessionID})
			l.mode = mode
		}
		l.meeting.Apply(*ev.Segment)
		clk := l.clockLocked()
		l.mu.Unlock()
		l.traceMu.Lock()
		switch ev.Segment.Status {
		case "live":
			l.feed.Drafts++
		case "pending":
			l.feed.Pending++
		case "removed":
			l.feed.Removed++
		default:
			l.feed.Finals++
			if lag := clk - ev.Segment.End; lag >= 0 && lag < 600 {
				l.feed.FinalLag = append(l.feed.FinalLag, lag)
			}
		}
		l.traceMu.Unlock()
		l.emit("live:segment", ev.Segment)
		if ev.Segment.Status != "removed" {
			// A stepped replay runs the model itself, on the meeting clock.
			if !l.stepped.Load() {
				select {
				case l.wake <- struct{}{}:
				default:
				}
			}
			if l.eng.Cfg.Live.FastLane {
				seg := *ev.Segment
				l.spawn(func() { l.fastLane(seg) })
			}
		}
	}
}

// claimLocked marks a query as looked up, unless it, or one mostly about
// the same words, was in the last few minutes. The model rephrases a
// running topic every run; without this each rephrasing is a new lookup.
func (l *Live) claimLocked(q string) (bool, string) {
	key := normalize(q)
	cw := contentWords(q)
	for k, at := range l.briefed {
		if l.since(at) >= briefTTL {
			continue
		}
		if k == key || overlap(cw, contentWords(k)) >= 0.5 {
			return false, k
		}
	}
	l.briefed[key] = l.now()
	return true, ""
}

const (
	briefTTL = 5 * time.Minute
	// minLinePhrase is how distinctive a phrase in a single line must be
	// to be looked up on its own (sum of word rarities, weighted by count).
	minLinePhrase = 7.0
	// staleSummary: a summary still queued this long after its passages
	// were shown is skipped; the conversation has moved on.
	staleSummary = 25 * time.Second
	// draftRerun: with no new final text, the model reruns on drafts at
	// most this often.
	draftRerun = 1500 * time.Millisecond
)

func (l *Live) nextCycle() int {
	l.traceMu.Lock()
	defer l.traceMu.Unlock()
	l.cycle++
	return l.cycle
}

// fastLane looks up what a single line names, as soon as it arrives:
// people and organisations in drafts or final text, and very distinctive
// phrases in final text. No model is involved, so the passages appear well
// within a second; a summary follows from the summariser.
func (l *Live) fastLane(seg Segment) {
	t0 := time.Now()
	l.mu.Lock()
	// A misheard name, once its alias is confirmed, reads as itself.
	seg.Text = l.spell.Canonical(seg.Text)
	sug, m, scopeName, ctx, topics := l.sug, l.meeting, l.scope, l.ctx, l.topicIx != nil
	var recentLines []Segment
	if m != nil && topics {
		recentLines = m.RecentLines(float64(max(30, l.eng.Cfg.Live.Window)))
	}
	l.mu.Unlock()
	if sug == nil || m == nil {
		return
	}
	spotted := sug.Entities(seg.Text)
	if topics {
		l.termTopics(l.nextCycle(), seg.Text, recentLines)
	}
	var phrases []PhraseScore
	if seg.Status == "" && l.eng.Cfg.Live.PhraseLookups {
		for _, p := range sug.PhraseScores(seg.Text, 2) {
			if p.Score >= minLinePhrase && strings.Contains(p.Phrase, " ") {
				phrases = append(phrases, p)
			}
		}
	}
	var todo []Suggestion
	l.mu.Lock()
	for _, e := range spotted {
		if e.ID == l.eng.OwnerEntity() || len(todo) == 2 {
			continue
		}
		if ok, _ := l.claimLocked(e.Name); ok {
			todo = append(todo, Suggestion{Query: e.Name, Why: "just mentioned", Kind: "entity", EntityID: e.ID, Score: 2})
		}
	}
	for _, p := range phrases {
		if len(todo) == 2 {
			break
		}
		if ok, _ := l.claimLocked(p.Phrase); ok {
			todo = append(todo, Suggestion{Query: p.Phrase, Why: "distinctive phrase", Kind: "phrase", Score: 1})
		}
	}
	window := float64(max(30, l.eng.Cfg.Live.Window))
	lines := l.meeting.RecentLines(window)
	recent := l.meeting.Recent(window)
	exclude := []string{"tomoe:" + l.meeting.Session.ID}
	l.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	id := l.nextCycle()
	status := "final"
	if seg.Status == "live" {
		status = "draft"
	}
	var names []string
	for _, s := range todo {
		names = append(names, s.Query)
	}
	l.trace(id, "line", fmt.Sprintf("%s line names %s", status, strings.Join(names, ", ")), time.Since(t0).Milliseconds(),
		map[string]any{"status": status, "speaker": speakerName(seg.Speaker), "text": seg.Text, "spotted": spotted, "phrases": phrases})
	l.addChips(todo...)
	if sc, err := l.eng.Scope(scopeName); err == nil {
		l.emit("live:entities", l.cards(sc, sug.Entities(recent), exclude))
	}
	for _, s := range todo {
		s := s
		l.spawn(func() { l.brief(ctx, id, s, exclude, scopeName, lines) })
	}
}

// addChips puts suggestions at the front of the chip row.
func (l *Live) addChips(s ...Suggestion) {
	l.mu.Lock()
	all := dedupe(append(append([]Suggestion(nil), s...), l.chips...))
	if len(all) > 8 {
		all = all[:8]
	}
	l.chips = all
	l.mu.Unlock()
	l.emit("live:suggestions", all)
}

// modelLoop asks the model for queries whenever new speech has arrived,
// one run after another (no fixed cadence unless suggest_every is set).
func (l *Live) modelLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		}
		// A burst of draft updates becomes one run.
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
		l.mu.Lock()
		m, sug := l.meeting, l.sug
		if m == nil || sug == nil || m.Version() == l.lastModelVer {
			l.mu.Unlock()
			continue
		}
		// Run on new final text; on draft changes alone only every few
		// seconds, or the model reruns on every partial word.
		if m.Finals() == l.lastModelFinals && time.Since(l.lastModelRun) < draftRerun {
			wait := draftRerun - time.Since(l.lastModelRun)
			l.mu.Unlock()
			go func() {
				time.Sleep(wait)
				select {
				case l.wake <- struct{}{}:
				default:
				}
			}()
			continue
		}
		l.lastModelFinals = m.Finals()
		gap := time.Duration(l.eng.Cfg.Live.SuggestEvery) * time.Second
		if l.replay != nil && l.replaySpeed > 1 {
			gap = time.Duration(float64(gap) / l.replaySpeed)
		}
		if wait := gap - time.Since(l.lastModelRun); gap > 0 && wait > 0 {
			l.mu.Unlock()
			go func() {
				time.Sleep(wait)
				select {
				case l.wake <- struct{}{}:
				default:
				}
			}()
			continue
		}
		l.lastModelVer, l.lastModelRun = m.Version(), time.Now()
		window := float64(max(30, l.eng.Cfg.Live.Window))
		recent, lines := m.Recent(window), m.RecentLines(window)
		exclude := []string{"tomoe:" + m.Session.ID}
		scopeName := l.scope
		l.mu.Unlock()
		if strings.TrimSpace(recent) != "" {
			l.modelCycle(ctx, sug, recent, lines, exclude, scopeName)
		}
	}
}

// modelCycle runs one model suggestion and briefs on its top queries. With
// the fast lane off it also does the names and phrases, as a whole cycle.
func (l *Live) modelCycle(ctx context.Context, sug *Suggester, recent string, lines []Segment, exclude []string, scopeName string) {
	id := l.nextCycle()
	l.traceMu.Lock()
	fs := l.feed.summary()
	l.feed = feedStats{}
	l.traceMu.Unlock()
	l.trace(id, "cycle", fmt.Sprintf("model run: reading the last %ds (%d lines)", max(30, l.eng.Cfg.Live.Window), len(lines)), 0, map[string]any{
		"feed": fs, "window": recent, "scope": scopeName, "exclude": exclude,
	})
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return
	}
	var out []Suggestion
	if !l.eng.Cfg.Live.FastLane {
		spotted := sug.Entities(recent)
		l.emit("live:entities", l.cards(sc, spotted, exclude))
		for i, e := range spotted {
			if i == 2 {
				break
			}
			out = append(out, Suggestion{Query: e.Name, Why: fmt.Sprintf("mentioned %d×", e.Count), Kind: "entity", EntityID: e.ID, Score: 2 - float64(i)*0.1})
		}
		for i, p := range sug.PhraseScores(recent, 3) {
			out = append(out, Suggestion{Query: p.Phrase, Why: "distinctive phrase", Kind: "phrase", Score: 1 - float64(i)*0.1})
		}
	}
	switch {
	case l.eng.Chat == nil || !l.eng.Cfg.Live.LLMSuggestions:
		l.trace(id, "llm", "model suggestions off", 0, nil)
	case !l.eng.Chat.Local():
		// The model would read the conversation; a cloud model may not.
		l.trace(id, "llm", "model suggestions skipped: the model isn't local", 0, nil)
	default:
		t1 := time.Now()
		l.llmBusy.Lock()
		waited := time.Since(t1)
		t2 := time.Now()
		qs, raw, prompt, err := sug.FromLLM(ctx, l.eng.Chat, l.eng.Cfg.Owner.Name, recent, nil)
		l.llmBusy.Unlock()
		var aboutOwner []string
		qs, aboutOwner = dropOwner(qs, l.eng.Cfg.Owner.Name)
		detail := map[string]any{"model": l.eng.Chat.Model, "prompt": prompt, "raw": raw, "waitedForModelMs": waited.Milliseconds(), "droppedAboutOwner": aboutOwner}
		if err != nil {
			detail["error"] = err.Error()
			l.emit("live:error", "suggestions: "+err.Error())
		}
		l.trace(id, "llm", fmt.Sprintf("model suggested %d queries", len(qs)), time.Since(t2).Milliseconds(), detail)
		out = append(out, qs...)
	}
	out = dedupe(out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	type decision struct {
		Query    string  `json:"query"`
		Kind     string  `json:"kind"`
		Score    float64 `json:"score"`
		Decision string  `json:"decision"`
		SaidAt   float64 `json:"saidAt"`
	}
	var decisions []decision
	var todo []Suggestion
	l.mu.Lock()
	for _, s := range out {
		d := decision{Query: s.Query, Kind: s.Kind, Score: s.Score, SaidAt: evidenceAt(s.Query, lines)}
		if len(todo) >= l.eng.Cfg.Live.AutoBrief {
			d.Decision = fmt.Sprintf("shown, not looked up (below the top %d)", l.eng.Cfg.Live.AutoBrief)
		} else if ok, like := l.claimLocked(s.Query); !ok {
			d.Decision = "skipped: like “" + like + "”, looked up recently"
		} else {
			d.Decision = "looking up"
			todo = append(todo, s)
		}
		decisions = append(decisions, d)
	}
	l.mu.Unlock()
	if len(out) > 0 {
		l.addChips(out...)
	}
	l.trace(id, "rank", fmt.Sprintf("%d suggestions, %d to look up", len(out), len(todo)), 0, decisions)
	for _, s := range todo {
		s := s
		l.spawn(func() { l.brief(ctx, id, s, exclude, scopeName, lines) })
	}
}

// Brief looks a suggestion up now (a clicked chip or a typed query).
func (l *Live) Brief(ctx context.Context, s Suggestion, exclude []string, scopeName string) {
	l.mu.Lock()
	var lines []Segment
	if l.meeting != nil {
		if exclude == nil {
			exclude = []string{"tomoe:" + l.meeting.Session.ID}
		}
		lines = l.meeting.RecentLines(float64(max(30, l.eng.Cfg.Live.Window)))
	}
	l.briefed[normalize(s.Query)] = l.now()
	l.mu.Unlock()
	l.brief(ctx, l.nextCycle(), s, exclude, scopeName, lines)
}

type sumJob struct {
	cycle int
	b     Brief
	said  float64
	ready time.Time
}

// brief searches for a suggestion and shows the passages straight away;
// the summary follows from the summariser.
func (l *Live) brief(ctx context.Context, cycle int, s Suggestion, exclude []string, scopeName string, lines []Segment) {
	b := Brief{Query: s.Query, Why: s.Why, Kind: s.Kind, State: "loading", At: time.Now(), SpokenAgo: -1}
	said := evidenceAt(s.Query, lines)
	if l.eng.Cfg.Live.Focus {
		b.Quote = evidenceLine(s.Query, lines)
	}
	l.emit("live:brief", b)
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return
	}
	l.spawn(func() { l.lookupTopic(ctx, cycle, s) })
	t0 := time.Now()
	l.mu.Lock()
	query := l.spell.Expand(s.Query)
	l.mu.Unlock()
	if bs := l.threadStatus(ctx, sc, query); bs != nil {
		b.Status = bs
		l.trace(cycle, "status", fmt.Sprintf("“%s”: %s's status", s.Query, bs.Name), time.Since(t0).Milliseconds(), nil)
		l.emit("live:brief", b)
	}
	hits, err := l.eng.Search(ctx, sc, query, 4, exclude)
	if err != nil {
		b.State, b.Summary = "error", err.Error()
		l.trace(cycle, "error", "search failed: "+s.Query, time.Since(t0).Milliseconds(), err.Error())
		l.emit("live:brief", b)
		return
	}
	var ht []SearchHitTrace
	for i, h := range hits {
		ht = append(ht, SearchHitTrace{Rank: i + 1, Item: h.ItemID, Seq: h.Seq, Title: h.Title, Date: h.OccurredAt.Format("Jan 2"),
			Score: h.Score, Level: h.Label.Level.String(), Cats: h.Label.Categories.String(), Excerpt: excerpt(h.Text, 240)})
	}
	mode := "hybrid"
	if l.eng.Embedder == nil {
		mode = "keyword"
	}
	l.trace(cycle, "search", fmt.Sprintf("“%s”: %d results", s.Query, len(hits)), time.Since(t0).Milliseconds(),
		map[string]any{"query": s.Query, "kind": s.Kind, "scope": sc.Name, "mode": mode, "exclude": exclude, "hits": ht})
	b.Hits = hits
	if len(hits) == 0 {
		b.State = "empty"
		l.finishBrief(cycle, b, said, "nothing found", "passages")
		return
	}
	b.State = "done"
	// A written status says more than two sentences from four passages,
	// and saves the local model's time during the meeting.
	summarise := l.eng.Chat != nil && l.eng.Chat.Local() && b.Status == nil
	b.Summarizing = summarise
	l.finishBrief(cycle, b, said, "passages shown", "passages")
	if summarise && l.stepped.Load() {
		l.summarise(ctx, sumJob{cycle: cycle, b: b, said: said, ready: l.now()})
	} else if summarise {
		select {
		case l.sumQ <- sumJob{cycle: cycle, b: b, said: said, ready: l.now()}:
		default:
			l.trace(cycle, "summary", "summary queue full; passages only", 0, nil)
			b.Summarizing = false
			l.emit("live:brief", b)
		}
	}
}

// summarizer writes brief summaries one at a time, newest first, skipping
// ones the conversation has moved past.
func (l *Live) summarizer(ctx context.Context) {
	for {
		var job sumJob
		select {
		case <-ctx.Done():
			return
		case job = <-l.sumQ:
		}
		// Newest first: take whatever else is waiting and keep the latest.
		pending := []sumJob{job}
		for more := true; more; {
			select {
			case j := <-l.sumQ:
				pending = append(pending, j)
			default:
				more = false
			}
		}
		for i := len(pending) - 1; i >= 0; i-- {
			l.summarise(ctx, pending[i])
		}
	}
}

func (l *Live) summarise(ctx context.Context, job sumJob) {
	b := job.b
	if age := l.since(job.ready); age > staleSummary {
		b.Summarizing = false
		l.emit("live:brief", b)
		l.trace(job.cycle, "summary", fmt.Sprintf("“%s”: skipped, %s old", b.Query, age.Round(time.Second)), 0, nil)
		return
	}
	t1 := time.Now()
	l.llmBusy.Lock()
	waited := time.Since(t1)
	t2 := time.Now()
	sum, err := l.eng.Chat.Complete(ctx, []provider.Message{
		{Role: "system", Content: briefPrompt},
		{Role: "user", Content: ask.Excerpts(b.Hits) + "\nTopic: " + b.Query},
	}, provider.ChatOptions{Temperature: 0.2, MaxTokens: 160}, nil)
	l.llmBusy.Unlock()
	sum = strings.TrimSpace(sum)
	b.Summarizing = false
	verdict := "summary kept"
	dropped := false
	switch {
	case err != nil:
		verdict = "model error; passages only"
	case strings.HasPrefix(strings.ToUpper(sum), "NONE"):
		verdict = "model said the results aren't about the topic: dropped"
		dropped = true
	default:
		b.Summary = sum
	}
	l.trace(job.cycle, "summary", verdict, time.Since(t2).Milliseconds(), map[string]any{
		"query": b.Query, "raw": sum, "waitedForModelMs": waited.Milliseconds(), "error": errString(err)})
	if dropped {
		b.State, b.Hits = "empty", nil
		l.finishBrief(job.cycle, b, job.said, "dropped", "summary")
		return
	}
	l.finishBrief(job.cycle, b, job.said, "summary shown", "summary")
}

func (l *Live) finishBrief(cycle int, b Brief, said float64, outcome, stage string) {
	now := l.clock()
	if said >= 0 {
		b.SpokenAgo = now - said
	}
	l.mu.Lock()
	rel := l.rel
	l.mu.Unlock()
	if rel != nil && b.State == "done" {
		key := briefKey(b.Query)
		l.offer(cycle, key, briefText(b), &b)
		b.Rated = true
		b.Open, b.WasOpen, b.Fit = l.placeOf(key)
		resend, cards := l.choose()
		b.Open, b.WasOpen, b.Fit = l.placeOf(key)
		for _, o := range resend {
			if briefKey(o.Query) != key {
				o.Rated = true
				l.emit("live:brief", o)
			}
		}
		if cards {
			l.emitTopics(cycle)
		}
	} else if rel != nil && b.State == "empty" {
		// Dropped (nothing found, or the summary says it's off the topic):
		// out of the running, and its place goes to the next best.
		l.mu.Lock()
		delete(l.places, briefKey(b.Query))
		l.mu.Unlock()
		resend, cards := l.choose()
		for _, o := range resend {
			o.Rated = true
			l.emit("live:brief", o)
		}
		if cards {
			l.emitTopics(cycle)
		}
	}
	l.emit("live:brief", b)
	d := map[string]any{"query": b.Query, "kind": b.Kind, "outcome": outcome, "stage": stage, "readyAt": now, "totalMs": time.Since(b.At).Milliseconds()}
	title := fmt.Sprintf("“%s”: %s", b.Query, outcome)
	if said >= 0 {
		d["saidAt"], d["spokenAgo"] = said, b.SpokenAgo
		title += fmt.Sprintf(", %.1fs after it was said", b.SpokenAgo)
	}
	l.trace(cycle, "brief", title, time.Since(b.At).Milliseconds(), d)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

const briefPrompt = `You brief someone during a live meeting. From the numbered excerpts of their own past meetings, email and chat, say in at most two short sentences what they should know about the topic: prior decisions, commitments, open issues, who said what. Cite excerpts like [1]. Transcripts contain recognition errors; "You" is the user. If the excerpts are not actually about the topic, reply exactly NONE.`

func (l *Live) cards(sc sensitivity.Scope, spotted []Spotted, exclude []string) []Card {
	var out []Card
	for _, e := range spotted {
		if len(out) == 6 {
			break
		}
		if e.ID == l.eng.OwnerEntity() {
			continue
		}
		g, err := l.eng.Store.EntityGraph(sc, e.ID, []string{l.eng.OwnerEntity()}, 4)
		if err != nil || g == nil {
			continue
		}
		c := Card{ID: e.ID, Name: g.Entity.Name, Type: g.Entity.Type, Items: g.Entity.Items, LastSeen: g.Entity.LastSeen, Mentions: e.Count}
		for _, n := range g.Neighbors {
			c.Neighbors = append(c.Neighbors, n.Name)
		}
		for _, it := range g.Items {
			if len(c.Recent) == 3 {
				break
			}
			skip := false
			for _, x := range exclude {
				if it.ID == x {
					skip = true
				}
			}
			if !skip {
				c.Recent = append(c.Recent, it.OccurredAt.Format("Jan 2")+" · "+it.Title)
			}
		}
		out = append(out, c)
	}
	return out
}

func normalize(q string) string { return strings.Join(words(q), " ") }

func dedupe(in []Suggestion) []Suggestion {
	seen := map[string]bool{}
	var out []Suggestion
	for _, s := range in {
		k := normalize(s.Query)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// clockedReplay follows an audio player's position instead of a timer, so
// what the panel shows matches what is heard, through pauses and seeks.
type clockedReplay struct {
	session  Session
	schedule []timed
	next     int
	last     float64
}

// SessionAudio returns the path of a saved session's audio, if it has one.
func (l *Live) SessionAudio(sessionID string) (string, bool) {
	if !validSessionID(sessionID) {
		return "", false
	}
	p := filepath.Join(l.eng.Cfg.TomoeSessions, sessionID, "audio.m4a")
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// validSessionID accepts Tomoe's session directory names only (UUIDs), so a
// session id can never reach outside the sessions folder.
func validSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f'):
			return false
		}
	}
	return true
}

func (l *Live) loadSession(sessionID string) (Session, []Segment, error) {
	if !validSessionID(sessionID) {
		return Session{}, nil, fmt.Errorf("not a session id: %q", sessionID)
	}
	b, err := os.ReadFile(filepath.Join(l.eng.Cfg.TomoeSessions, sessionID, "session.json"))
	if err != nil {
		return Session{}, nil, err
	}
	var s struct {
		Session
		Segments []Segment `json:"segments"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Session{}, nil, err
	}
	var segs []Segment
	for _, g := range s.Segments {
		if g.Status == "" && strings.TrimSpace(g.Text) != "" {
			segs = append(segs, g)
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	return s.Session, segs, nil
}

func (l *Live) stopReplayLocked() {
	if l.replay != nil {
		l.replay()
		l.replay = nil
	}
	l.clocked = nil
}

// ReplayWithAudio starts a replay driven by ReplayClock (the audio
// player's position) rather than a timer.
func (l *Live) ReplayWithAudio(sessionID string) error {
	sess, segs, err := l.loadSession(sessionID)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.stopReplayLocked()
	l.clocked = &clockedReplay{session: sess, schedule: buildSchedule(segs)}
	l.mu.Unlock()
	l.handle(Event{V: 1, Type: "session", State: "started", Session: &sess}, "replay")
	l.trace(0, "feed", "replaying with audio; drafts and finals timed by the replay model", 0, map[string]any{
		"lines": len(segs), "draftEvery": draftEvery, "turnGap": turnGap, "decodeShare": decodeShare})
	return nil
}

// ReplayClock moves a clocked replay to t seconds into the meeting.
// Seeking backwards rebuilds the conversation up to t.
func (l *Live) ReplayClock(t float64) {
	l.mu.Lock()
	c := l.clocked
	if c == nil || l.mode != "replay" {
		l.mu.Unlock()
		return
	}
	if t < c.last-1 {
		c.next = 0
		l.meeting = NewMeeting(l.meeting.Session)
		l.briefed = map[string]time.Time{}
		l.chips = nil
		l.lastModelVer = 0
		c.last = t
		l.mu.Unlock()
		l.emit("live:reset", c.session)
		l.trace(0, "feed", fmt.Sprintf("seeked back to %.0fs; rebuilt the conversation", t), 0, nil)
		l.mu.Lock()
	}
	c.last = t
	var due []Segment
	for c.next < len(c.schedule) && c.schedule[c.next].At <= t {
		due = append(due, c.schedule[c.next].Seg)
		c.next++
	}
	sid := c.session.ID
	l.mu.Unlock()
	for i := range due {
		l.handle(Event{V: 1, Type: "segment", SessionID: sid, Segment: &due[i]}, "replay")
	}
}

// Replay plays a saved Tomoe session through the panel as if it were live,
// speed times faster, with the same draft and final timing as the audio
// replay.
func (l *Live) Replay(ctx context.Context, sessionID string, speed float64) error {
	sess, segs, err := l.loadSession(sessionID)
	if err != nil {
		return err
	}
	if speed <= 0 {
		speed = 4
	}
	sched := buildSchedule(segs)
	l.mu.Lock()
	l.stopReplayLocked()
	rctx, cancel := context.WithCancel(ctx)
	l.replay, l.replayT0, l.replaySpeed = cancel, time.Now(), speed
	l.mu.Unlock()
	// The replayed meeting is in the knowledge base already; excluding it by
	// id (tomoe:<id>) keeps it from answering itself, as for a live meeting.
	l.handle(Event{V: 1, Type: "session", State: "started", Session: &sess}, "replay")
	l.trace(0, "feed", fmt.Sprintf("replaying text at %g×", speed), 0, map[string]any{"lines": len(segs)})
	go func() {
		defer cancel()
		start := time.Now()
		for _, e := range sched {
			d := time.Duration(e.At/speed*float64(time.Second)) - time.Since(start)
			if d > 0 {
				select {
				case <-rctx.Done():
					return
				case <-time.After(d):
				}
			}
			seg := e.Seg
			l.handle(Event{V: 1, Type: "segment", SessionID: sess.ID, Segment: &seg}, "replay")
		}
		l.handle(Event{V: 1, Type: "session", State: "stopped", Session: &sess}, "replay")
	}()
	return nil
}

// StopReplay ends a replay in progress.
func (l *Live) StopReplay() {
	l.mu.Lock()
	l.stopReplayLocked()
	if l.mode == "replay" || l.mode == "ended" {
		l.mode = "idle"
	}
	l.mu.Unlock()
	l.pushStatus()
}

// threadStatus is the status of the thread a lookup is about, when it
// matches one well (liveStatusMin) and the scope may see its status. A
// replay sees only what was known when the meeting happened, so a status
// written as of a later item isn't shown.
func (l *Live) threadStatus(ctx context.Context, sc sensitivity.Scope, query string) *BriefStatus {
	f, err := l.eng.ThreadFinder()
	if err != nil || f == nil {
		return nil
	}
	v := l.eng.Embed(ctx, query)
	if v == nil {
		return nil
	}
	m := f.Find(sc, v, query, 1)
	if len(m) == 0 || m[0].Score < liveStatusMin {
		return nil
	}
	sts, err := l.eng.Store.ThreadStatuses(sc)
	if err != nil {
		return nil
	}
	st, ok := sts[m[0].ID]
	if !ok {
		return nil
	}
	l.mu.Lock()
	replayAt := time.Time{}
	if l.mode == "replay" && l.meeting != nil {
		replayAt = l.meeting.Session.CreatedAt
	}
	l.mu.Unlock()
	if !replayAt.IsZero() && st.AsOf.After(replayAt) {
		return nil
	}
	var s knowledge.Status
	if json.Unmarshal([]byte(st.Body), &s) != nil {
		return nil
	}
	bs := &BriefStatus{Topic: m[0].ID, Name: m[0].Name, AsOf: st.AsOf, Now: knowledge.StripIDs(s.Now)}
	for _, c := range s.Changed[:min(len(s.Changed), 2)] {
		bs.Changed = append(bs.Changed, knowledge.StripIDs(c.Text))
	}
	for _, o := range s.Open[:min(len(s.Open), 3)] {
		bs.Open = append(bs.Open, knowledge.StripIDs(o.Text))
	}
	return bs
}
