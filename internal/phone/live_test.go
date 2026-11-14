package phone

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// fakeLive stands in for the app's allowlist: Search and Big are
// allowed, Ask emits an event, Block waits for release; anything else is
// refused.
type fakeLive struct {
	mu      sync.Mutex
	opened  int
	closed  int
	running atomic.Int64
	most    atomic.Int64
	release chan struct{}
}

type fakeCalls struct {
	l    *fakeLive
	emit func(string, any)
}

func (l *fakeLive) Open(emit func(string, any)) LiveCalls {
	l.mu.Lock()
	l.opened++
	l.mu.Unlock()
	return &fakeCalls{l: l, emit: emit}
}

func (c *fakeCalls) Close() {
	c.l.mu.Lock()
	c.l.closed++
	c.l.mu.Unlock()
}

func (c *fakeCalls) Call(ctx context.Context, method string, args []json.RawMessage) (any, error) {
	switch method {
	case "Search":
		var q string
		if len(args) != 1 || json.Unmarshal(args[0], &q) != nil {
			return nil, ErrBadArgs
		}
		return []map[string]string{{"title": "hit for " + q}}, nil
	case "Big":
		return strings.Repeat("x", 200<<10), nil
	case "Ask":
		c.emit("ask:token", map[string]string{"id": "1", "text": "invented"})
		c.emit("ask:done", map[string]string{"id": "1", "text": strings.Repeat("y", 100<<10)})
		return nil, nil
	case "Fail":
		return nil, errors.New("not ready: no dataset open")
	case "Block":
		n := c.l.running.Add(1)
		defer c.l.running.Add(-1)
		for {
			m := c.l.most.Load()
			if n <= m || c.l.most.CompareAndSwap(m, n) {
				break
			}
		}
		select {
		case <-c.l.release:
		case <-ctx.Done():
		}
		return "done", nil
	}
	return nil, ErrNotAllowed
}

var testAssets = fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><title>t</title>")},
	"assets/index-1.js":  {Data: []byte("console.log(1)")},
	"assets/index-1.css": {Data: []byte("body{}")},
	"assets/.hidden.js":  {Data: []byte("x")},
	"assets/notes.md":    {Data: []byte("x")},
}

// phoneSide is the test's end of a live connection: it sends requests
// and reads what the Mac sends, answering pings.
type phoneSide struct {
	t      *testing.T
	c      net.Conn
	id     int64
	mu     sync.Mutex
	events []string
	pings  int
}

type frame struct {
	m    map[string]any
	body []byte
}

func (p *phoneSide) send(v map[string]any) int64 {
	p.id++
	if _, ok := v["id"]; !ok {
		v["id"] = p.id
	}
	if err := writeMsg(p.c, v, nil); err != nil {
		p.t.Fatalf("send: %v", err)
	}
	return p.id
}

// wait reads until the reply to id, answering pings and keeping events.
func (p *phoneSide) wait(id int64) frame {
	p.t.Helper()
	p.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		m, body, err := readMsg(p.c)
		if err != nil {
			p.t.Fatalf("waiting for %d: %v", id, err)
		}
		if p.other(m) {
			continue
		}
		if m["id"] == float64(id) {
			return frame{m, body}
		}
		p.t.Fatalf("reply to %v while waiting for %d", m["id"], id)
	}
}

// other handles a ping or an event; false for a reply.
func (p *phoneSide) other(m map[string]any) bool {
	switch m["op"] {
	case "ping":
		p.mu.Lock()
		p.pings++
		p.mu.Unlock()
		writeMsg(p.c, map[string]any{"id": m["id"], "ok": true}, nil)
		return true
	case "event":
		p.mu.Lock()
		p.events = append(p.events, m["name"].(string))
		p.mu.Unlock()
		return true
	}
	return false
}

// liveHarness pairs a fake phone with live mode served by fakeLive.
// Its done channel closes once the phone's side has finished.
func liveHarness(t *testing.T, onLive func(p *phoneSide)) (*harness, *fakePhone, *fakeLive, chan struct{}) {
	h := newHarness(t)
	l := &fakeLive{release: make(chan struct{})}
	h.s.cfg.Live, h.s.cfg.Assets = l, testAssets
	h.s.livePing = 50 * time.Millisecond
	p := newFakePhone(t)
	h.pairWith(p)
	done := make(chan struct{})
	p.live = func(c net.Conn) {
		defer close(done)
		onLive(&phoneSide{t: t, c: c})
	}
	return h, p, l, done
}

func (h *harness) liveTarget(p *fakePhone) target {
	tg := h.syncTarget(p)
	tg.live = true
	return tg
}

func TestLiveCallsAssetsEventsAndEnd(t *testing.T) {
	var got struct {
		search, big, refused, failed, index, js, css, traversal, hidden, unknown, ask frame
		events                                                                        []string
		pings                                                                         int
		closed                                                                        bool
	}
	h, p, l, done := liveHarness(t, func(ps *phoneSide) {
		got.search = ps.wait(ps.send(map[string]any{"op": "rpc", "method": "Search", "args": []any{"roadmap"}}))
		got.big = ps.wait(ps.send(map[string]any{"op": "rpc", "method": "Big", "args": []any{}}))
		got.refused = ps.wait(ps.send(map[string]any{"op": "rpc", "method": "SetPreference", "args": []any{"a", "b", true}}))
		got.failed = ps.wait(ps.send(map[string]any{"op": "rpc", "method": "Fail", "args": []any{}}))
		got.index = ps.wait(ps.send(map[string]any{"op": "asset", "path": "/?phone=1"}))
		got.js = ps.wait(ps.send(map[string]any{"op": "asset", "path": "/assets/index-1.js"}))
		got.css = ps.wait(ps.send(map[string]any{"op": "asset", "path": "assets/index-1.css"}))
		got.traversal = ps.wait(ps.send(map[string]any{"op": "asset", "path": "/assets/../../phones.json"}))
		got.hidden = ps.wait(ps.send(map[string]any{"op": "asset", "path": "/assets/.hidden.js"}))
		got.unknown = ps.wait(ps.send(map[string]any{"op": "asset", "path": "/assets/notes.md"}))
		got.ask = ps.wait(ps.send(map[string]any{"op": "rpc", "method": "Ask", "args": []any{}}))
		// Wait for both events and a ping or two.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			ps.mu.Lock()
			n, pings := len(ps.events), ps.pings
			ps.mu.Unlock()
			if n >= 2 && pings >= 2 {
				break
			}
			ps.c.SetReadDeadline(time.Now().Add(time.Second))
			m, _, err := readMsg(ps.c)
			if err != nil {
				t.Errorf("reading: %v", err)
				break
			}
			ps.other(m)
		}
		end := ps.send(map[string]any{"op": "live_end"})
		ps.wait(end)
		got.events, got.pings = ps.events, ps.pings
		ps.c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err := readMsg(ps.c)
		got.closed = err != nil
	})
	h.s.session(context.Background(), h.liveTarget(p))
	<-done

	ops := p.opList()
	if !slices.Equal(ops, []string{"hello", "list", "agenda", "live_start"}) {
		t.Errorf("ops %v: a due sync, without bye, then live_start", ops)
	}
	if r := got.search.m["result"].([]any); len(r) != 1 || r[0].(map[string]any)["title"] != "hit for roadmap" || got.search.m["ok"] != true {
		t.Errorf("search %v", got.search.m)
	}
	var big string
	if got.big.m["result"] != nil || json.Unmarshal(got.big.body, &big) != nil || len(big) != 200<<10 {
		t.Errorf("a large result isn't its body: %v, %d bytes", got.big.m, len(got.big.body))
	}
	if got.refused.m["ok"] != false || got.refused.m["error"] != "not_allowed" {
		t.Errorf("refused %v", got.refused.m)
	}
	if got.failed.m["error"] != "failed" || got.failed.m["message"] != "not ready: no dataset open" {
		t.Errorf("failed %v", got.failed.m)
	}
	if got.index.m["type"] != "text/html; charset=utf-8" || !strings.Contains(string(got.index.body), "<title>") {
		t.Errorf("index %v %q", got.index.m, got.index.body)
	}
	if got.js.m["type"] != "text/javascript; charset=utf-8" || string(got.js.body) != "console.log(1)" || got.css.m["type"] != "text/css; charset=utf-8" {
		t.Errorf("js %v css %v", got.js.m, got.css.m)
	}
	for name, f := range map[string]frame{"traversal": got.traversal, "hidden": got.hidden, "unknown type": got.unknown} {
		if f.m["ok"] != false || f.m["error"] != "not_found" || f.body != nil {
			t.Errorf("%s served: %v", name, f.m)
		}
	}
	if !slices.Equal(got.events, []string{"ask:token", "ask:done"}) {
		t.Errorf("events %v", got.events)
	}
	if got.pings < 2 || !got.closed {
		t.Errorf("pings %d, closed after live_end %v", got.pings, got.closed)
	}
	if l.opened != 1 || l.closed != 1 {
		t.Errorf("opened %d closed %d", l.opened, l.closed)
	}
	if !strings.Contains(h.logged(), "Explore on \"Test phone\" ended (the phone left): 5 calls, 6 files, 4 refused, 1 failed") {
		t.Errorf("log:\n%s", h.logged())
	}
	if strings.Contains(h.logged(), "roadmap") {
		t.Error("a call's arguments reached the log")
	}
}

func TestLiveSkipsSyncWhenNotDue(t *testing.T) {
	h, p, _, done := liveHarness(t, func(ps *phoneSide) { ps.wait(ps.send(map[string]any{"op": "live_end"})) })
	h.syncOnce(p) // a sync just now
	p.mu.Lock()
	p.ops = nil
	p.mu.Unlock()
	h.s.session(context.Background(), h.liveTarget(p))
	<-done
	if ops := p.opList(); !slices.Equal(ops, []string{"live_start"}) {
		t.Errorf("ops %v", ops)
	}
}

func TestLiveRefusedByPhone(t *testing.T) {
	h, p, l, _ := liveHarness(t, nil)
	p.live = nil
	h.syncOnce(p)
	h.s.session(context.Background(), h.liveTarget(p))
	if l.opened != 0 || !strings.Contains(h.logged(), "declined live mode (not_live)") {
		t.Errorf("opened %d; log:\n%s", l.opened, h.logged())
	}
}

func TestLiveIdleClose(t *testing.T) {
	closed := make(chan bool, 1)
	h, p, _, done := liveHarness(t, func(ps *phoneSide) {
		// One request, then only answers to pings.
		ps.wait(ps.send(map[string]any{"op": "rpc", "method": "Search", "args": []any{"x"}}))
		ps.c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			m, _, err := readMsg(ps.c)
			if err != nil {
				closed <- true
				return
			}
			ps.other(m)
		}
	})
	h.s.liveIdle = 300 * time.Millisecond
	h.syncOnce(p)
	start := time.Now()
	h.s.session(context.Background(), h.liveTarget(p))
	<-done
	if !<-closed || time.Since(start) > 3*time.Second {
		t.Fatalf("not closed when idle (%s)", time.Since(start))
	}
	if !strings.Contains(h.logged(), "ended (idle)") {
		t.Errorf("log:\n%s", h.logged())
	}
	// Not dialed for live mode again until the phone advertises anew.
	fp16 := p.fpHex()[:16]
	h.s.mu.Lock()
	idled := h.s.liveIdled[fp16]
	h.s.mu.Unlock()
	if !idled {
		t.Fatal("idle close not remembered")
	}
	r := sighting{host: "phone.local.", port: p.port, txt: map[string]string{"v": "2", "fp": fp16, "live": "1"}}
	h.s.dnssd = func(args ...string) (dnssdProc, error) { return nil, errors.New("no lookups here") }
	h.s.consider(context.Background(), r, true)
	h.s.mu.Lock()
	still := h.s.liveIdled[fp16]
	h.s.mu.Unlock()
	if !still {
		t.Error("a periodic sighting forgot the idle close")
	}
	h.s.consider(context.Background(), r, false)
	h.s.mu.Lock()
	still = h.s.liveIdled[fp16]
	h.s.mu.Unlock()
	if still {
		t.Error("a new advertisement didn't clear the idle close")
	}
}

func TestLiveSilentPhoneClosed(t *testing.T) {
	h, p, _, done := liveHarness(t, func(ps *phoneSide) {
		// Never answers a ping.
		ps.c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := readMsg(ps.c); err != nil {
				return
			}
		}
	})
	h.s.liveReadWait = 300 * time.Millisecond
	h.syncOnce(p)
	start := time.Now()
	h.s.session(context.Background(), h.liveTarget(p))
	<-done
	if time.Since(start) > 3*time.Second || !strings.Contains(h.logged(), "ended (connection lost)") {
		t.Errorf("after %s; log:\n%s", time.Since(start), h.logged())
	}
}

func TestLiveConcurrencyAndTimeout(t *testing.T) {
	var replies []frame
	h, p, l, done := liveHarness(t, func(ps *phoneSide) {
		var ids []int64
		for i := 0; i < 10; i++ {
			ids = append(ids, ps.send(map[string]any{"op": "rpc", "method": "Block", "args": []any{}}))
		}
		ps.c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for len(replies) < len(ids) {
			m, _, err := readMsg(ps.c)
			if err != nil {
				t.Errorf("reading: %v", err)
				return
			}
			if !ps.other(m) {
				replies = append(replies, frame{m: m})
			}
		}
		ps.wait(ps.send(map[string]any{"op": "live_end"}))
	})
	h.s.liveCallTimeout = 2 * time.Second
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(l.release)
	}()
	h.syncOnce(p)
	h.s.session(context.Background(), h.liveTarget(p))
	<-done
	if m := l.most.Load(); m > liveInFlight || m < 2 {
		t.Errorf("%d calls at once", m)
	}
	if len(replies) != 10 {
		t.Fatalf("%d replies", len(replies))
	}
	for _, r := range replies {
		if r.m["result"] != "done" {
			t.Errorf("reply %v", r.m)
		}
	}

	// A call that outlasts its timeout is answered "timeout".
	h2, p2, _, done2 := liveHarness(t, func(ps *phoneSide) {
		replies = []frame{ps.wait(ps.send(map[string]any{"op": "rpc", "method": "Block", "args": []any{}}))}
		ps.wait(ps.send(map[string]any{"op": "live_end"}))
	})
	h2.s.liveCallTimeout = 200 * time.Millisecond
	h2.syncOnce(p2)
	h2.s.session(context.Background(), h2.liveTarget(p2))
	<-done2
	if replies[0].m["error"] != "timeout" {
		t.Errorf("timeout reply %v", replies[0].m)
	}
}

func TestLiveProtocolViolations(t *testing.T) {
	for name, req := range map[string]map[string]any{
		"unknown op":        {"op": "settings", "id": 1},
		"no id":             {"op": "rpc", "id": nil, "method": "Search", "args": []any{}},
		"bad method":        {"op": "rpc", "method": "../Search", "args": []any{}},
		"a body":            {"op": "rpc", "method": "Search", "args": []any{}, "body": 0},
		"reply to nothing":  {"ok": true, "id": 99},
		"asset no path":     {"op": "asset"},
		"too many args":     {"op": "rpc", "method": "Search", "args": make([]any, 17)},
		"a sync op in live": {"op": "list"},
	} {
		t.Run(name, func(t *testing.T) {
			closed := make(chan bool, 1)
			h, p, _, done := liveHarness(t, func(ps *phoneSide) {
				ps.send(req)
				ps.c.SetReadDeadline(time.Now().Add(3 * time.Second))
				for {
					m, _, err := readMsg(ps.c)
					if err != nil {
						closed <- !errors.Is(err, os.ErrDeadlineExceeded)
						return
					}
					if !ps.other(m) {
						t.Errorf("answered %v", m)
					}
				}
			})
			h.syncOnce(p)
			h.s.session(context.Background(), h.liveTarget(p))
			<-done
			if c := <-closed; !c || !strings.Contains(h.logged(), "broke the protocol") {
				t.Errorf("closed %v; log:\n%s", c, h.logged())
			}
		})
	}
}

func TestAssetPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                                     "index.html",
		"/":                                    "index.html",
		"/?phone=1":                            "index.html",
		"/index.html#x":                        "index.html",
		"assets/index-1.js":                    "assets/index-1.js",
		"/assets/index-1.css?v=":               "assets/index-1.css",
		"/../etc/passwd.txt":                   "",
		"assets/../index.html":                 "",
		"assets//index-1.js":                   "",
		"assets/./index-1.js":                  "",
		"/.git/config.txt":                     "",
		"assets\\index-1.js":                   "",
		"assets/index%2e.js":                   "",
		"assets/x.go":                          "",
		"assets/x":                             "",
		"/" + strings.Repeat("a", 300) + ".js": "",
	} {
		got, ok := assetPath(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
	if _, _, err := readAsset(nil, "/"); err == nil {
		t.Error("served without a frontend")
	}
}
