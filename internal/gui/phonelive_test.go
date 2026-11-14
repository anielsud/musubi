package gui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/anielsud/musubi/internal/phone"
	"github.com/anielsud/musubi/internal/store"
)

type phoneSeen struct {
	mu  sync.Mutex
	evs []string
	ids []string
}

func (p *phoneSeen) emit(ev string, d any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, ev)
	p.ids = append(p.ids, askID(d))
}

func (p *phoneSeen) list() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.evs), slices.Clone(p.ids)
}

func rawArgs(t *testing.T, args ...any) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func phoneFixture(t *testing.T) (*App, *phoneSeen, phone.LiveCalls, *events) {
	a := exportFixture(t)
	ev := &events{}
	a.ctx, a.emitFn = context.Background(), ev.emit
	seen := &phoneSeen{}
	calls := phoneLive{a}.Open(seen.emit)
	t.Cleanup(calls.Close)
	return a, seen, calls, ev
}

func TestPhoneAllowlistRefuses(t *testing.T) {
	_, _, calls, _ := phoneFixture(t)
	for _, c := range []struct {
		method string
		args   []any
	}{
		{"SetPreference", []any{"phone", "enabled", true}},
		{"Judge", []any{1, "wrong", ""}},
		{"CorrectStatement", []any{1, "x", ""}},
		{"MergeThreads", []any{1, 2}},
		{"RecorderStart", nil},
		{"DeleteMeeting", []any{"tomoe:s1"}},
		{"SetSharing", []any{true}},
		{"Diagnostics", nil},
		{"PhoneRevoke", []any{"x"}},
		{"SaveTranscript", []any{"tomoe:s1", "owner"}},
		{"Voices", nil},
		{"Status", nil},
		{"emit", []any{"x", 1}},
		{"Startup", nil},
	} {
		if _, err := calls.Call(context.Background(), c.method, rawArgs(t, c.args...)); !errors.Is(err, phone.ErrNotAllowed) {
			t.Errorf("%s: %v", c.method, err)
		}
	}
}

func TestPhoneArgumentsChecked(t *testing.T) {
	_, _, calls, _ := phoneFixture(t)
	raw := func(s ...string) []json.RawMessage {
		var out []json.RawMessage
		for _, x := range s {
			out = append(out, json.RawMessage(x))
		}
		return out
	}
	long := `"` + strings.Repeat("a", 1001) + `"`
	for name, c := range map[string]struct {
		method string
		args   []json.RawMessage
	}{
		"too few":         {"Search", raw(`"x"`, `"owner"`)},
		"too many":        {"Topics", raw(`"owner"`, `"owner"`)},
		"scope cloud":     {"Search", raw(`"x"`, `"cloud"`, `5`)},
		"reply scope":     {"Topics", raw(`"reply:a@b.example"`)},
		"k too large":     {"Search", raw(`"x"`, `"owner"`, `1000`)},
		"k a float":       {"Search", raw(`"x"`, `"owner"`, `1.5`)},
		"k a string":      {"Search", raw(`"x"`, `"owner"`, `"5"`)},
		"query too long":  {"Search", raw(long, `"owner"`, `5`)},
		"id not a number": {"Topic", raw(`"1"`, `"owner"`)},
		"id zero":         {"Topic", raw(`0`, `"owner"`)},
		"empty item id":   {"Item", raw(`""`, `"owner"`)},
		"two values":      {"Topics", raw(`"owner" "owner"`)},
		"not JSON":        {"Topics", raw(`owner`)},
	} {
		if _, err := calls.Call(context.Background(), c.method, c.args); !errors.Is(err, phone.ErrBadArgs) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPhoneReadsScoped(t *testing.T) {
	_, _, calls, _ := phoneFixture(t)
	v, err := calls.Call(context.Background(), "Item", rawArgs(t, "tomoe:s1", "owner"))
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := json.Marshal(v)
	v, err = calls.Call(context.Background(), "Item", rawArgs(t, "tomoe:s1", "shared-screen"))
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := json.Marshal(v)
	iv, ok := v.(*store.ItemView)
	if !ok {
		t.Fatalf("Item returned %T", v)
	}
	if len(owner) <= len(shared) || iv == nil {
		t.Errorf("shared-screen saw as much as owner: %d vs %d bytes", len(shared), len(owner))
	}
	// A method without an error result, and one returning only an error.
	if v, err := calls.Call(context.Background(), "StopAsk", nil); err != nil || v != nil {
		t.Errorf("StopAsk: %v %v", v, err)
	}
	// An error from the method comes back as the desktop would see it.
	a2 := &App{}
	c2 := phoneLive{a2}.Open(func(string, any) {})
	defer c2.Close()
	if _, err := c2.Call(context.Background(), "Topics", rawArgs(t, "owner")); err == nil || errors.Is(err, phone.ErrBadArgs) {
		t.Errorf("no dataset: %v", err)
	}
}

func TestPhoneGetsOnlyItsOwnAskEvents(t *testing.T) {
	a, seen, calls, desktop := phoneFixture(t)
	if _, err := calls.Call(context.Background(), "Ask", rawArgs(t, "p1", "What about the roadmap?", "owner")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !desktop.has("ask:done") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// The desktop's own question: the desktop gets its events, the phone
	// doesn't.
	a.emit("ask:token", map[string]string{"id": "d1", "text": "desktop"})
	a.emit("ask:done", map[string]any{"id": "d1"})
	a.emit("update:done", "1 new")
	a.emit("app:status", AppStatus{})
	evs, ids := seen.list()
	if !slices.Contains(evs, "ask:done") || slices.Contains(ids, "d1") || !slices.Contains(evs, "update:done") || slices.Contains(evs, "app:status") {
		t.Errorf("phone saw %v %v", evs, ids)
	}
	if !desktop.has("ask:done") || !desktop.has("app:status") {
		t.Error("the desktop's events changed")
	}
	// Closed: nothing more reaches the phone.
	calls.Close()
	a.emit("update:done", "again")
	if evs2, _ := seen.list(); len(evs2) != len(evs) {
		t.Errorf("events after close: %v", evs2)
	}
}

func TestPhoneLiveNeedsFrontend(t *testing.T) {
	a := &App{}
	if l, _ := a.phoneLiveFor(true); l != nil {
		t.Error("live without a frontend")
	}
	a.SetFrontend(fstest.MapFS{"index.html": {Data: []byte("x")}})
	if l, f := a.phoneLiveFor(true); l == nil || f == nil {
		t.Error("no live with a frontend")
	}
	if l, _ := a.phoneLiveFor(false); l != nil {
		t.Error("live with [phone] explore off")
	}
}
