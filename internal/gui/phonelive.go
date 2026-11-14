package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"reflect"
	"sync"
	"unicode/utf8"

	"github.com/anielsud/musubi/internal/phone"
)

// Explore on the phone (docs/phone.md, part 3): the phone's WebView runs
// this app's own frontend in phone mode, and its calls to
// window.go.gui.App arrive here over the connection the Mac opened. Only
// the read methods the Explore screens and Ask call are allowed, each
// with its arguments decoded as Wails decodes them and bounded; anything
// else is refused. Ask's streaming events reach the phone through a tap
// on emit, which leaves what the desktop receives unchanged.

// argCheck bounds one argument after it has been decoded.
type argCheck func(v any) bool

func scopeArg(v any) bool { s, _ := v.(string); return s == "owner" || s == "shared-screen" }

func textArg(max int) argCheck {
	return func(v any) bool { s, ok := v.(string); return ok && len(s) <= max && utf8.ValidString(s) }
}

func idArg(v any) bool {
	s, ok := v.(string)
	return ok && s != "" && len(s) <= 300 && utf8.ValidString(s)
}

func numArg(v any) bool { n, ok := v.(int64); return ok && n > 0 }

func kArg(v any) bool { n, ok := v.(int); return ok && n >= 1 && n <= 50 }

// phoneAllowed is the allowlist: the methods the Explore view (with its
// Topics, People and Search panes, the person and topic pages and the
// source drawer) and Ask call in phone mode (frontend/src, phone.ts), and
// the checks on their arguments. Every one only reads, in the scope it is
// given.
//
//	Search, Entities, Graph   ExploreView: search, names, an entity's graph
//	Item                      SourceDrawer: a passage or timeline item
//	People, PersonPage,
//	Headshot                  ExploreView's People pane, PersonView
//	PeopleNames               PeopleProvider: names that link to a person
//	KnowledgeCounts, Topics,
//	Topic, TopicMap           Knowledge.tsx's TopicsPane and topic pages
//	SearchClaims              Knowledge.tsx's ClaimResults beside a search
//	Ask, StopAsk              AskView's useAsk and AskCard
//
// PersonPage and Headshot may pick a person's headshot from meeting
// tiles and keep it in the headshot cache, as they do for the desktop;
// nothing else here writes. Left out though the desktop's Explore calls
// them: Voices, RecorderState and ItemsExist (the Voices and Corrections panes,
// which phone mode hides), and every edit (Judge, CorrectStatement,
// MergeThreads, SplitThread, UndoThreadEdit, aliases).
var phoneAllowed = map[string][]argCheck{
	"Search":          {textArg(1000), scopeArg, kArg},
	"Entities":        {textArg(1000), scopeArg},
	"Graph":           {idArg, scopeArg},
	"Item":            {idArg, scopeArg},
	"People":          {scopeArg},
	"PersonPage":      {idArg, scopeArg},
	"Headshot":        {idArg, scopeArg},
	"PeopleNames":     {scopeArg},
	"KnowledgeCounts": {},
	"Topics":          {scopeArg},
	"Topic":           {numArg, scopeArg},
	"TopicMap":        {scopeArg},
	"SearchClaims":    {textArg(1000), scopeArg},
	"Ask":             {textArg(40), textArg(2000), scopeArg},
	"StopAsk":         {},
}

// phoneEvents are the events the phone's screens listen to; Ask's only
// for the questions that phone asked.
var phoneEvents = map[string]bool{"ask:found": true, "ask:token": true, "ask:done": true, "update:done": true}

// maxPhoneAsks bounds the questions a connection tracks.
const maxPhoneAsks = 64

// SetFrontend gives the app its built frontend, which phone mode serves.
func (a *App) SetFrontend(f fs.FS) { a.frontend = f }

// phoneTaps are the live connections' event taps.
type phoneTaps struct {
	mu   sync.Mutex
	next int
	subs map[int]func(string, any)
}

func (t *phoneTaps) add(f func(string, any)) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.subs == nil {
		t.subs = map[int]func(string, any){}
	}
	t.next++
	id := t.next
	t.subs[id] = f
	return func() {
		t.mu.Lock()
		delete(t.subs, id)
		t.mu.Unlock()
	}
}

// emit hands an event to every live connection; each filters and queues
// it without blocking.
func (t *phoneTaps) emit(ev string, data any) {
	if !phoneEvents[ev] {
		return
	}
	t.mu.Lock()
	subs := make([]func(string, any), 0, len(t.subs))
	for _, f := range t.subs {
		subs = append(subs, f)
	}
	t.mu.Unlock()
	for _, f := range subs {
		f(ev, data)
	}
}

// phoneLive is the app's phone.LiveHandler.
type phoneLive struct{ a *App }

func (p phoneLive) Open(emit func(string, any)) phone.LiveCalls {
	c := &phoneCalls{a: p.a, emit: emit, asks: map[string]bool{}}
	c.stop = p.a.phoneTaps.add(c.event)
	return c
}

// phoneCalls are one live connection's calls.
type phoneCalls struct {
	a    *App
	emit func(string, any)
	stop func()

	mu   sync.Mutex
	asks map[string]bool
}

func (c *phoneCalls) Close() { c.stop() }

// event forwards an event to this phone: Ask's only for its own
// questions (the desktop's answers stay on the desktop).
func (c *phoneCalls) event(ev string, data any) {
	if ev == "update:done" {
		c.emit(ev, data)
		return
	}
	id := askID(data)
	c.mu.Lock()
	mine := c.asks[id]
	if mine && ev == "ask:done" {
		delete(c.asks, id)
	}
	c.mu.Unlock()
	if mine {
		c.emit(ev, data)
	}
}

func askID(data any) string {
	switch d := data.(type) {
	case map[string]any:
		s, _ := d["id"].(string)
		return s
	case map[string]string:
		return d["id"]
	}
	return ""
}

// Call runs an allowlisted method as Wails would: each argument decoded
// into the parameter's type, the result returned for encoding, an error
// as the failure.
func (c *phoneCalls) Call(_ context.Context, method string, args []json.RawMessage) (any, error) {
	checks, ok := phoneAllowed[method]
	if !ok {
		return nil, phone.ErrNotAllowed
	}
	m := reflect.ValueOf(c.a).MethodByName(method)
	if !m.IsValid() || m.Type().NumIn() != len(checks) || len(args) != len(checks) {
		return nil, phone.ErrBadArgs
	}
	in := make([]reflect.Value, len(args))
	for i, raw := range args {
		v := reflect.New(m.Type().In(i))
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(v.Interface()); err != nil || dec.More() || !checks[i](v.Elem().Interface()) {
			return nil, phone.ErrBadArgs
		}
		in[i] = v.Elem()
	}
	if method == "Ask" {
		id := in[0].String()
		c.mu.Lock()
		if len(c.asks) >= maxPhoneAsks {
			c.asks = map[string]bool{}
		}
		c.asks[id] = true
		c.mu.Unlock()
	}
	out := m.Call(in)
	var res any
	errType := reflect.TypeOf((*error)(nil)).Elem()
	for _, o := range out {
		if o.Type().Implements(errType) {
			if !o.IsNil() {
				return nil, o.Interface().(error)
			}
			continue
		}
		res = o.Interface()
	}
	return res, nil
}

// phoneLiveFor is what openPhone hands phone sync: live mode when
// [phone] explore is on and the frontend is there to serve.
func (a *App) phoneLiveFor(explore bool) (phone.LiveHandler, fs.FS) {
	if !explore || a.frontend == nil {
		return nil, nil
	}
	if _, err := fs.Stat(a.frontend, "index.html"); err != nil {
		return nil, nil
	}
	return phoneLive{a}, a.frontend
}
