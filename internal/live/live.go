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
	Mode      string   `json:"mode"`      // idle, live, replay
	Session   *Session `json:"session,omitempty"`
	Scope     string   `json:"scope"`
	Lines     int      `json:"lines"`
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
	replay    context.CancelFunc
	clocked   *clockedReplay
	llmBusy   sync.Mutex
	onSaved   func()
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
	st := Status{Connected: l.connected, Mode: l.mode, Scope: l.scope}
	if l.meeting != nil {
		s := l.meeting.Session
		st.Session = &s
		st.Lines = len(l.meeting.segs)
	}
	return st
}

func (l *Live) pushStatus() { l.emit("live:status", l.Status()) }

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
		l.pushStatus()
	})
	t := time.NewTicker(2 * time.Second)
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
	case "session":
		if ev.Session == nil {
			return
		}
		switch ev.State {
		case "started":
			if t, err := l.eng.Store.EventAt(ev.Session.CreatedAt); err == nil && t != "" {
				ev.Session.Title = t
			}
			l.mu.Lock()
			// A live meeting takes over from a replay.
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
			l.emit("live:reset", ev.Session)
		case "stopped":
			l.mu.Lock()
			if l.mode == mode {
				l.mode = "ended"
			}
			l.mu.Unlock()
		case "saved":
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
		l.mu.Unlock()
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
	if l.lastVer > 0 && time.Since(l.lastRun) < every {
		l.mu.Unlock()
		return
	}
	l.lastVer, l.lastRun = m.Version(), time.Now()
	recent := m.Recent(float64(max(30, l.eng.Cfg.Live.Window)))
	exclude := []string{"tomoe:" + m.Session.ID}
	scopeName := l.scope
	l.mu.Unlock()
	if strings.TrimSpace(recent) == "" {
		return
	}
	go l.suggest(ctx, sug, recent, exclude, scopeName)
}

func (l *Live) suggest(ctx context.Context, sug *Suggester, recent string, exclude []string, scopeName string) {
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return
	}
	spotted := sug.Entities(recent)
	l.emit("live:entities", l.cards(sc, spotted, exclude))

	var out []Suggestion
	// The model reads the conversation; a cloud model may not.
	if l.eng.Chat != nil && l.eng.Chat.Local() && l.eng.Cfg.Live.LLMSuggestions && l.llmBusy.TryLock() {
		qs, err := sug.FromLLM(ctx, l.eng.Chat, recent, spotted)
		l.llmBusy.Unlock()
		if err != nil {
			l.emit("live:error", "suggestions: "+err.Error())
		}
		out = append(out, qs...)
	}
	for i, e := range spotted {
		if i == 2 {
			break
		}
		out = append(out, Suggestion{Query: e.Name, Why: fmt.Sprintf("mentioned %d×", e.Count), Kind: "entity", EntityID: e.ID, Score: 2 - float64(i)*0.1})
	}
	for i, p := range sug.Phrases(recent, 3) {
		out = append(out, Suggestion{Query: p, Why: "distinctive phrase", Kind: "phrase", Score: 1 - float64(i)*0.1})
	}
	out = dedupe(out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 6 {
		out = out[:6]
	}
	l.emit("live:suggestions", out)

	// Brief on the top suggestions not briefed in the last few minutes.
	n := 0
	for _, s := range out {
		if n >= l.eng.Cfg.Live.AutoBrief {
			break
		}
		key := normalize(s.Query)
		l.mu.Lock()
		last, seen := l.briefed[key]
		fresh := !seen || time.Since(last) > 5*time.Minute
		if fresh {
			l.briefed[key] = time.Now()
		}
		l.mu.Unlock()
		if !fresh {
			continue
		}
		n++
		l.Brief(ctx, s, exclude, scopeName)
	}
}

// Brief looks a suggestion up and, if a local model is available, writes a
// two-sentence summary of what was found.
func (l *Live) Brief(ctx context.Context, s Suggestion, exclude []string, scopeName string) {
	b := Brief{Query: s.Query, Why: s.Why, Kind: s.Kind, State: "loading", At: time.Now()}
	l.emit("live:brief", b)
	sc, err := l.eng.Scope(scopeName)
	if err != nil {
		return
	}
	if exclude == nil {
		l.mu.Lock()
		if l.meeting != nil {
			exclude = []string{"tomoe:" + l.meeting.Session.ID}
		}
		l.mu.Unlock()
	}
	hits, err := l.eng.Search(ctx, sc, s.Query, 4, exclude)
	if err != nil {
		b.State, b.Summary = "error", err.Error()
		l.emit("live:brief", b)
		return
	}
	b.Hits = hits
	if len(hits) == 0 {
		b.State = "empty"
		l.emit("live:brief", b)
		return
	}
	if l.eng.Chat != nil && l.eng.Chat.Local() {
		l.llmBusy.Lock()
		sum, err := l.eng.Chat.Complete(ctx, []provider.Message{
			{Role: "system", Content: briefPrompt},
			{Role: "user", Content: ask.Excerpts(hits) + "\nTopic: " + s.Query},
		}, provider.ChatOptions{Temperature: 0.2, MaxTokens: 160}, nil)
		l.llmBusy.Unlock()
		sum = strings.TrimSpace(sum)
		switch {
		case err != nil:
			b.Summary = ""
		case strings.HasPrefix(strings.ToUpper(sum), "NONE"):
			b.State = "empty"
			b.Hits = nil
			l.emit("live:brief", b)
			return
		default:
			b.Summary = sum
		}
	}
	b.State = "done"
	l.emit("live:brief", b)
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
	session Session
	segs    []Segment
	drafted map[int]bool
	final   map[int]bool
	last    float64
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

// ReplayWithAudio starts a replay driven by ReplayClock (the audio
// player's position) rather than a timer.
func (l *Live) ReplayWithAudio(sessionID string) error {
	sess, segs, err := l.loadSession(sessionID)
	if err != nil {
		return err
	}
	l.mu.Lock()
	if l.replay != nil {
		l.replay()
		l.replay = nil
	}
	l.clocked = &clockedReplay{session: sess, segs: segs, drafted: map[int]bool{}, final: map[int]bool{}}
	l.mu.Unlock()
	l.handle(Event{V: 1, Type: "session", State: "started", Session: &sess}, "replay")
	return nil
}

// ReplayClock moves a clocked replay to t seconds into the meeting: lines
// appear as drafts halfway through being spoken and become final when they
// end. Seeking backwards rebuilds the conversation up to t.
func (l *Live) ReplayClock(t float64) {
	l.mu.Lock()
	c := l.clocked
	if c == nil || l.mode != "replay" {
		l.mu.Unlock()
		return
	}
	if t < c.last-1 {
		c.drafted, c.final = map[int]bool{}, map[int]bool{}
		l.meeting = NewMeeting(l.meeting.Session)
		l.briefed = map[string]time.Time{}
		l.lastVer = 0
		l.mu.Unlock()
		l.emit("live:reset", c.session)
		l.mu.Lock()
	}
	c.last = t
	var out []Segment
	for i, g := range c.segs {
		if g.Start > t {
			break
		}
		switch {
		case !c.final[i] && g.End <= t:
			c.final[i] = true
			out = append(out, g)
		case !c.final[i] && !c.drafted[i] && t >= g.Start+(g.End-g.Start)/2 && len(strings.Fields(g.Text)) > 4:
			c.drafted[i] = true
			ws := strings.Fields(g.Text)
			d := g
			d.Status = "live"
			d.Text = strings.ToLower(strings.Join(ws[:len(ws)/2], " "))
			out = append(out, d)
		}
	}
	sid := c.session.ID
	l.mu.Unlock()
	for i := range out {
		l.handle(Event{V: 1, Type: "segment", SessionID: sid, Segment: &out[i]}, "replay")
	}
}

// Replay plays a saved Tomoe session through the panel as if it were live,
// speed times faster, with each line first appearing as a partial draft.
func (l *Live) Replay(ctx context.Context, sessionID string, speed float64) error {
	b, err := os.ReadFile(filepath.Join(l.eng.Cfg.TomoeSessions, sessionID, "session.json"))
	if err != nil {
		return err
	}
	var s struct {
		Session
		Segments []Segment `json:"segments"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if speed <= 0 {
		speed = 4
	}
	l.mu.Lock()
	if l.replay != nil {
		l.replay()
	}
	rctx, cancel := context.WithCancel(ctx)
	l.replay = cancel
	l.mu.Unlock()
	sess := s.Session
	// The replayed meeting is in the knowledge base already; excluding it by
	// id (tomoe:<id>) keeps it from answering itself, as for a live meeting.
	l.handle(Event{V: 1, Type: "session", State: "started", Session: &sess}, "replay")
	go func() {
		defer cancel()
		start := time.Now()
		wait := func(at float64) bool {
			d := time.Duration(at/speed*float64(time.Second)) - time.Since(start)
			if d <= 0 {
				return true
			}
			select {
			case <-rctx.Done():
				return false
			case <-time.After(d):
				return true
			}
		}
		segs := s.Segments
		sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
		for _, g := range segs {
			if g.Status != "" || strings.TrimSpace(g.Text) == "" {
				continue
			}
			ws := strings.Fields(g.Text)
			if len(ws) > 4 {
				if !wait(g.Start + (g.End-g.Start)/2) {
					return
				}
				draft := g
				draft.Status = "live"
				draft.Text = strings.ToLower(strings.Join(ws[:len(ws)/2], " "))
				l.handle(Event{V: 1, Type: "segment", SessionID: sess.ID, Segment: &draft}, "replay")
			}
			if !wait(g.End) {
				return
			}
			final := g
			l.handle(Event{V: 1, Type: "segment", SessionID: sess.ID, Segment: &final}, "replay")
		}
		l.handle(Event{V: 1, Type: "session", State: "stopped", Session: &sess}, "replay")
	}()
	return nil
}

// StopReplay ends a replay in progress.
func (l *Live) StopReplay() {
	l.mu.Lock()
	if l.replay != nil {
		l.replay()
		l.replay = nil
	}
	l.clocked = nil
	if l.mode == "replay" {
		l.mode = "idle"
	}
	l.mu.Unlock()
	l.pushStatus()
}
