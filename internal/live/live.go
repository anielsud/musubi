package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
}

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
	lastVer   int
	lastRun   time.Time
	briefed   map[string]time.Time
	onSaved   func()

	// Replays: timed (a timer at some speed) or clocked (an audio player).
	replay      context.CancelFunc
	replayT0    time.Time
	replaySpeed float64
	clocked     *clockedReplay

	llmBusy sync.Mutex

	traceMu sync.Mutex
	traces  []Trace
	seq     int
	cycle   int
	feed    feedStats
}

func New(eng *engine.Engine, emit Emit, onSaved func()) *Live {
	return &Live{eng: eng, emit: emit, mode: "idle", scope: "owner", briefed: map[string]time.Time{}, onSaved: onSaved}
}

// Reload rebuilds the suggester from the store (after an update).
func (l *Live) Reload() error {
	s, err := NewSuggester(l.eng.Store)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.sug = s
	l.mu.Unlock()
	return nil
}

func (l *Live) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := Status{Connected: l.connected, Mode: l.mode, Scope: l.scope, Clock: l.clockLocked()}
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
	l.lastVer = -1
	l.mu.Unlock()
	l.trace(0, "cycle", "scope set to "+name, 0, nil)
	l.pushStatus()
	return nil
}

// Run follows Tomoe's feed and runs the suggestion loop until ctx ends.
func (l *Live) Run(ctx context.Context) {
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
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.tick(ctx)
		}
	}
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
			l.meeting = NewMeeting(*ev.Session)
			l.mode = mode
			l.briefed = map[string]time.Time{}
			l.lastVer = 0
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
	}
}

// tick refreshes suggestions when enough new speech has arrived.
func (l *Live) tick(ctx context.Context) {
	l.mu.Lock()
	m, sug := l.meeting, l.sug
	if m == nil || sug == nil || m.Version() == l.lastVer {
		l.mu.Unlock()
		return
	}
	every := time.Duration(max(5, l.eng.Cfg.Live.SuggestEvery)) * time.Second
	// In a replay the meeting clock can run faster than the wall clock.
	if l.replay != nil && l.replaySpeed > 1 {
		every = time.Duration(float64(every) / l.replaySpeed)
	}
	if l.lastVer > 0 && time.Since(l.lastRun) < every {
		l.mu.Unlock()
		return
	}
	l.lastVer, l.lastRun = m.Version(), time.Now()
	window := float64(max(30, l.eng.Cfg.Live.Window))
	recent := m.Recent(window)
	lines := m.RecentLines(window)
	exclude := []string{"tomoe:" + m.Session.ID}
	scopeName := l.scope
	l.mu.Unlock()
	if strings.TrimSpace(recent) == "" {
		return
	}
	l.traceMu.Lock()
	l.cycle++
	cycle := l.cycle
	fs := l.feed.summary()
	l.feed = feedStats{}
	l.traceMu.Unlock()
	l.trace(cycle, "cycle", fmt.Sprintf("cycle %d: read the last %.0fs (%d lines)", cycle, window, len(lines)), 0, map[string]any{
		"feed": fs, "window": recent, "scope": scopeName, "exclude": exclude,
	})
	go l.suggest(ctx, cycle, sug, recent, lines, exclude, scopeName)
}

func (l *Live) suggest(ctx context.Context, cycle int, sug *Suggester, recent string, lines []Segment, exclude []string, scopeName string) {
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return
	}
	t0 := time.Now()
	spotted := sug.Entities(recent)
	cards := l.cards(sc, spotted, exclude)
	var shown []string
	for _, c := range cards {
		shown = append(shown, c.Name)
	}
	l.trace(cycle, "entities", fmt.Sprintf("%d names recognised, %d cards visible", len(spotted), len(cards)), time.Since(t0).Milliseconds(),
		map[string]any{"spotted": spotted, "cards": shown})
	l.emit("live:entities", cards)

	var out []Suggestion
	// The model reads the conversation; a cloud model may not.
	switch {
	case l.eng.Chat == nil || !l.eng.Cfg.Live.LLMSuggestions:
		l.trace(cycle, "llm", "model suggestions off", 0, nil)
	case !l.eng.Chat.Local():
		l.trace(cycle, "llm", "model suggestions skipped: the model isn't local", 0, nil)
	case !l.llmBusy.TryLock():
		l.trace(cycle, "llm", "model busy; this cycle uses names and phrases only", 0, nil)
	default:
		t1 := time.Now()
		qs, raw, prompt, err := sug.FromLLM(ctx, l.eng.Chat, l.eng.Cfg.Owner.Name, recent, spotted)
		l.llmBusy.Unlock()
		detail := map[string]any{"model": l.eng.Chat.Model, "prompt": prompt, "raw": raw}
		if err != nil {
			detail["error"] = err.Error()
			l.emit("live:error", "suggestions: "+err.Error())
		}
		l.trace(cycle, "llm", fmt.Sprintf("model suggested %d queries", len(qs)), time.Since(t1).Milliseconds(), detail)
		out = append(out, qs...)
	}
	for i, e := range spotted {
		if i == 2 {
			break
		}
		out = append(out, Suggestion{Query: e.Name, Why: fmt.Sprintf("mentioned %d×", e.Count), Kind: "entity", EntityID: e.ID, Score: 2 - float64(i)*0.1})
	}
	t2 := time.Now()
	phrases := sug.PhraseScores(recent, 3)
	var ptrace []map[string]any
	for i, p := range phrases {
		out = append(out, Suggestion{Query: p.Phrase, Why: "distinctive phrase", Kind: "phrase", Score: 1 - float64(i)*0.1})
		ptrace = append(ptrace, map[string]any{"phrase": p.Phrase, "score": p.Score, "said": p.Count})
	}
	l.trace(cycle, "phrases", fmt.Sprintf("%d distinctive phrases", len(phrases)), time.Since(t2).Milliseconds(), ptrace)
	out = dedupe(out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 6 {
		out = out[:6]
	}
	l.emit("live:suggestions", out)

	// Brief on the top suggestions not briefed in the last few minutes.
	type decision struct {
		Query    string  `json:"query"`
		Kind     string  `json:"kind"`
		Score    float64 `json:"score"`
		Decision string  `json:"decision"`
		SaidAt   float64 `json:"saidAt"`
	}
	var decisions []decision
	var todo []Suggestion
	for _, s := range out {
		key := normalize(s.Query)
		d := decision{Query: s.Query, Kind: s.Kind, Score: s.Score, SaidAt: evidenceAt(s.Query, lines)}
		l.mu.Lock()
		last, seen := l.briefed[key]
		fresh := !seen || time.Since(last) > 5*time.Minute
		switch {
		case len(todo) >= l.eng.Cfg.Live.AutoBrief:
			d.Decision = "shown, not looked up (below the top " + fmt.Sprint(l.eng.Cfg.Live.AutoBrief) + ")"
		case !fresh:
			d.Decision = fmt.Sprintf("skipped: looked up %s ago", time.Since(last).Round(time.Second))
		default:
			l.briefed[key] = time.Now()
			d.Decision = "looking up"
			todo = append(todo, s)
		}
		l.mu.Unlock()
		decisions = append(decisions, d)
	}
	l.trace(cycle, "rank", fmt.Sprintf("%d suggestions, %d to look up", len(out), len(todo)), 0, decisions)
	for _, s := range todo {
		l.brief(ctx, cycle, s, exclude, scopeName, lines)
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
	l.mu.Unlock()
	l.brief(ctx, 0, s, exclude, scopeName, lines)
}

// brief searches for a suggestion and, if a local model is available,
// writes a two-sentence summary of what was found.
func (l *Live) brief(ctx context.Context, cycle int, s Suggestion, exclude []string, scopeName string, lines []Segment) {
	b := Brief{Query: s.Query, Why: s.Why, Kind: s.Kind, State: "loading", At: time.Now(), SpokenAgo: -1}
	said := evidenceAt(s.Query, lines)
	l.emit("live:brief", b)
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return
	}
	t0 := time.Now()
	hits, err := l.eng.Search(ctx, sc, s.Query, 4, exclude)
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
		l.finishBrief(cycle, b, said, "nothing found")
		return
	}
	if l.eng.Chat != nil && l.eng.Chat.Local() {
		t1 := time.Now()
		l.llmBusy.Lock()
		waited := time.Since(t1)
		user := ask.Excerpts(hits) + "\nTopic: " + s.Query
		sum, err := l.eng.Chat.Complete(ctx, []provider.Message{
			{Role: "system", Content: briefPrompt},
			{Role: "user", Content: user},
		}, provider.ChatOptions{Temperature: 0.2, MaxTokens: 160}, nil)
		l.llmBusy.Unlock()
		sum = strings.TrimSpace(sum)
		verdict := "summary kept"
		switch {
		case err != nil:
			verdict = "model error; showing passages"
			b.Summary = ""
		case strings.HasPrefix(strings.ToUpper(sum), "NONE"):
			verdict = "model said the results aren't about the topic: dropped"
		default:
			b.Summary = sum
		}
		l.trace(cycle, "summary", verdict, time.Since(t1).Milliseconds(), map[string]any{
			"query": s.Query, "raw": sum, "waitedForModelMs": waited.Milliseconds(), "error": errString(err)})
		if strings.HasPrefix(strings.ToUpper(sum), "NONE") {
			b.State, b.Hits = "empty", nil
			l.finishBrief(cycle, b, said, "dropped")
			return
		}
	}
	b.State = "done"
	l.finishBrief(cycle, b, said, "shown")
}

func (l *Live) finishBrief(cycle int, b Brief, said float64, outcome string) {
	now := l.clock()
	if said >= 0 {
		b.SpokenAgo = now - said
	}
	l.emit("live:brief", b)
	d := map[string]any{"query": b.Query, "outcome": outcome, "readyAt": now, "totalMs": time.Since(b.At).Milliseconds()}
	title := fmt.Sprintf("“%s”: %s", b.Query, outcome)
	if said >= 0 {
		d["saidAt"], d["spokenAgo"] = said, b.SpokenAgo
		title += fmt.Sprintf(", %.0fs after it was said", b.SpokenAgo)
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
		l.lastVer = 0
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
