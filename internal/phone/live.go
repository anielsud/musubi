package phone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Explore on the phone (docs/phone.md, part 3): while the phone's app is
// in the foreground it advertises live=1; the Mac dials it, syncs if a
// sync is due, sends live_start and from then on the connection carries
// both directions. The phone asks (rpc: an allowlisted app method; asset:
// a file of the Mac's built frontend); the Mac answers each, sends the
// frontend's events and pings. Every frame has an id (the sender's
// counter; a reply repeats it).

const (
	// liveIdleClose: a live connection with no request for this long is
	// closed.
	liveIdleClose = 10 * time.Minute
	livePingEvery = 30 * time.Second
	// liveReadWait: the phone answers each ping, so a connection silent
	// for longer than two pings is dead.
	liveReadWait = 2*livePingEvery + 15*time.Second
	// liveCallTimeout bounds one method call; liveInFlight is how many
	// calls and assets run at once (more wait their turn).
	liveCallTimeout = 60 * time.Second
	liveInFlight    = 4
	// maxLiveValue bounds a result, an event's data and an asset; one
	// that doesn't fit a frame's JSON follows as its body.
	maxLiveValue = 16 << 20
	maxLiveArgs  = 16
	maxAssetPath = 200
	// maxLiveEvents: events waiting to be sent; more are dropped (Ask's
	// final event carries the whole answer, so a dropped token is
	// repaired).
	maxLiveEvents = 512
	maxErrMessage = 300
)

// ErrNotAllowed: the method is not one the phone may call.
var ErrNotAllowed = errors.New("not_allowed")

// ErrBadArgs: the arguments don't fit the method.
var ErrBadArgs = errors.New("bad_args")

// LiveHandler is the app's side of live mode: the allowlisted methods and
// the events the screens listen to.
type LiveHandler interface {
	// Open starts one live connection's calls; emit sends an event to
	// the phone (it never blocks).
	Open(emit func(name string, data any)) LiveCalls
}

// LiveCalls are one live connection's method calls.
type LiveCalls interface {
	// Call runs method with args (JSON, one per parameter) and returns
	// what Wails would hand the screen; ErrNotAllowed or ErrBadArgs when
	// the method or its arguments are refused.
	Call(ctx context.Context, method string, args []json.RawMessage) (any, error)
	// Close stops the connection's events.
	Close()
}

var methodPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,63}$`)

// liveFrame is any frame the phone sends in live mode.
type liveFrame struct {
	ID     *int64            `json:"id"`
	Op     string            `json:"op"`
	OK     *bool             `json:"ok"`
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
	Path   *string           `json:"path"`
	Body   *int64            `json:"body"`
	// Sync on live_end: the phone has something to sync and wants
	// Explore back right after.
	Sync bool `json:"sync"`
}

// liveStats is what a live connection did, for the log: counts only.
type liveStats struct {
	calls, assets, refused, failed, dropped atomic.Int64
}

func (st *liveStats) String() string {
	return fmt.Sprintf("%d calls, %d files, %d refused, %d failed, %d events dropped",
		st.calls.Load(), st.assets.Load(), st.refused.Load(), st.failed.Load(), st.dropped.Load())
}

// liveConn is one live connection.
type liveConn struct {
	s     *Syncer
	fc    *frameConn
	calls LiveCalls
	// wmu serializes writes; nextID is the Mac's counter.
	wmu    sync.Mutex
	nextID atomic.Int64
	sem    chan struct{}
	events chan liveEvent
	done   chan struct{}
	stats  liveStats

	mu       sync.Mutex
	lastReq  time.Time
	pings    map[int64]bool
	closeWhy string
}

type liveEvent struct {
	name string
	data any
}

// Why a live connection ended, when it matters to what follows.
const (
	liveEndIdle = "idle"
	liveEndSync = "the phone has something to sync"
	// maxLiveResyncs bounds the syncs one dial runs for a phone that
	// ends Explore to sync, so a phone that keeps asking can't hold the
	// Mac in a loop.
	maxLiveResyncs = 5
)

// startLive asks the phone for live mode and serves it until the phone
// ends it, it falls silent or idle, or ctx ends. It returns why it ended
// ("" when it never started).
func (s *Syncer) startLive(ctx context.Context, fc *frameConn, name string) string {
	if err := fc.call(map[string]any{"op": "live_start"}, nil); err != nil {
		var pe *phoneError
		if errors.As(err, &pe) {
			s.cfg.Logf("phone: %q declined live mode (%s)", name, pe.code)
		} else {
			s.cfg.Logf("phone: live mode with %q not started: %v", name, err)
		}
		return ""
	}
	lc := &liveConn{s: s, fc: fc, sem: make(chan struct{}, liveInFlight), events: make(chan liveEvent, maxLiveEvents),
		done: make(chan struct{}), lastReq: time.Now(), pings: map[int64]bool{}}
	lc.calls = s.cfg.Live.Open(lc.emit)
	s.cfg.Logf("phone: Explore open on %q", name)
	why := lc.run(ctx)
	lc.calls.Close()
	s.cfg.Logf("phone: Explore on %q ended (%s): %s", name, why, &lc.stats)
	return why
}

func (lc *liveConn) end(why string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.closeWhy == "" {
		lc.closeWhy = why
		close(lc.done)
		lc.fc.c.Close()
	}
}

func (lc *liveConn) why() string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.closeWhy
}

// run reads the phone's frames until the connection ends; the pings,
// the idle check and the events run beside it.
func (lc *liveConn) run(ctx context.Context) string {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		lc.keepAlive(ctx)
	}()
	go func() {
		defer wg.Done()
		lc.pumpEvents()
	}()
	err := lc.read(ctx)
	switch {
	case ctx.Err() != nil:
		lc.end("sync off")
	case errors.Is(err, errProtocol):
		lc.s.cfg.Logf("phone: live mode: %v", err)
		lc.end("the phone broke the protocol")
	case err != nil:
		lc.end("connection lost")
	}
	wg.Wait()
	// Calls still running finish into a closed connection; wait for them
	// so none outlives the syncer.
	for i := 0; i < liveInFlight; i++ {
		lc.sem <- struct{}{}
	}
	return lc.why()
}

func (lc *liveConn) read(ctx context.Context) error {
	for {
		b, err := lc.fc.readFrame(lc.s.liveReadWait)
		if err != nil {
			if lc.why() != "" {
				return nil // closed on purpose
			}
			return err
		}
		var f liveFrame
		if err := json.Unmarshal(b, &f); err != nil {
			return protoErr("live frame isn't a JSON object")
		}
		if f.Body != nil {
			return protoErr("live frame with a body")
		}
		if f.ID == nil || *f.ID < 0 {
			return protoErr("live frame without an id")
		}
		id := *f.ID
		if f.OK != nil {
			// A reply: only pings are asked of the phone.
			lc.mu.Lock()
			asked := lc.pings[id]
			delete(lc.pings, id)
			lc.mu.Unlock()
			if !asked || f.Op != "" {
				return protoErr("reply to nothing asked")
			}
			continue
		}
		switch f.Op {
		case "rpc":
			if !methodPattern.MatchString(f.Method) || len(f.Args) > maxLiveArgs {
				return protoErr("rpc without a method name or with %d args", len(f.Args))
			}
		case "asset":
			if f.Path == nil || len(*f.Path) > maxAssetPath+16 {
				return protoErr("asset without a path")
			}
		case "live_end":
			lc.reply(id, map[string]any{"ok": true}, nil)
			if f.Sync {
				lc.end(liveEndSync)
			} else {
				lc.end("the phone left")
			}
			return nil
		default:
			return protoErr("live op %q", cleanText(f.Op, 20))
		}
		lc.mu.Lock()
		lc.lastReq = time.Now()
		lc.mu.Unlock()
		select {
		case lc.sem <- struct{}{}:
		case <-lc.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
		if f.Op == "rpc" {
			go lc.rpc(ctx, id, f.Method, f.Args)
		} else {
			go lc.asset(id, *f.Path)
		}
	}
}

// keepAlive pings every livePingEvery and closes an idle connection.
func (lc *liveConn) keepAlive(ctx context.Context) {
	t := time.NewTicker(lc.s.livePing)
	defer t.Stop()
	for {
		select {
		case <-lc.done:
			return
		case <-ctx.Done():
			lc.end("sync off")
			return
		case <-t.C:
		}
		lc.mu.Lock()
		idle := time.Since(lc.lastReq) >= lc.s.liveIdle
		lc.mu.Unlock()
		if idle {
			lc.end(liveEndIdle)
			return
		}
		id := lc.nextID.Add(1)
		lc.mu.Lock()
		lc.pings[id] = true
		lc.mu.Unlock()
		if err := lc.send(map[string]any{"op": "ping", "id": id}, nil); err != nil {
			lc.end("connection lost")
			return
		}
	}
}

// emit queues an event for the phone; it never blocks the app.
func (lc *liveConn) emit(name string, data any) {
	select {
	case <-lc.done:
	case lc.events <- liveEvent{name, data}:
	default:
		lc.stats.dropped.Add(1)
	}
}

func (lc *liveConn) pumpEvents() {
	for {
		select {
		case <-lc.done:
			return
		case ev := <-lc.events:
			b, err := json.Marshal(ev.data)
			if err != nil || len(b) > maxLiveValue {
				lc.stats.dropped.Add(1)
				continue
			}
			head := map[string]any{"op": "event", "id": lc.nextID.Add(1), "name": ev.name}
			if err := lc.sendValue(head, "data", b); err != nil {
				lc.end("connection lost")
				return
			}
		}
	}
}

// rpc runs one method call and answers it; the call holds its slot until
// it returns, even after its answer timed out.
func (lc *liveConn) rpc(ctx context.Context, id int64, method string, args []json.RawMessage) {
	lc.stats.calls.Add(1)
	cctx, cancel := context.WithTimeout(ctx, lc.s.liveCallTimeout)
	defer cancel()
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() { <-lc.sem }()
		defer func() {
			if r := recover(); r != nil {
				lc.s.cfg.Logf("phone: live call %s panicked: %v", method, r)
				ch <- result{err: errors.New("the call failed")}
			}
		}()
		v, err := lc.calls.Call(cctx, method, args)
		ch <- result{v, err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-cctx.Done():
		r.err = cctx.Err()
	}
	head := map[string]any{"id": id}
	switch {
	case errors.Is(r.err, ErrNotAllowed):
		lc.stats.refused.Add(1)
		head["ok"], head["error"] = false, "not_allowed"
	case errors.Is(r.err, ErrBadArgs):
		lc.stats.refused.Add(1)
		head["ok"], head["error"] = false, "bad_args"
	case errors.Is(r.err, context.DeadlineExceeded):
		lc.stats.failed.Add(1)
		head["ok"], head["error"] = false, "timeout"
	case r.err != nil:
		// The message is what the desktop shows for the same failure.
		lc.stats.failed.Add(1)
		head["ok"], head["error"], head["message"] = false, "failed", cleanText(r.err.Error(), maxErrMessage)
	}
	if r.err != nil {
		lc.reply(id, head, nil)
		return
	}
	b, err := json.Marshal(r.v)
	if err != nil || len(b) > maxLiveValue {
		lc.stats.failed.Add(1)
		lc.reply(id, map[string]any{"id": id, "ok": false, "error": "too_large"}, nil)
		return
	}
	head["ok"] = true
	if err := lc.sendValue(head, "result", b); err != nil {
		lc.end("connection lost")
	}
}

// asset answers with one file of the built frontend.
func (lc *liveConn) asset(id int64, p string) {
	defer func() { <-lc.sem }()
	lc.stats.assets.Add(1)
	b, typ, err := readAsset(lc.s.cfg.Assets, p)
	if err != nil {
		lc.stats.refused.Add(1)
		lc.reply(id, map[string]any{"id": id, "ok": false, "error": "not_found"}, nil)
		return
	}
	lc.reply(id, map[string]any{"id": id, "ok": true, "type": typ, "body": len(b)}, b)
}

func (lc *liveConn) reply(id int64, head map[string]any, body []byte) {
	head["id"] = id
	if err := lc.send(head, body); err != nil {
		lc.end("connection lost")
	}
}

// sendValue sends head with the JSON value b as key, or, when that frame
// would be too large, as its body.
func (lc *liveConn) sendValue(head map[string]any, key string, b []byte) error {
	head[key] = json.RawMessage(b)
	if hb, err := json.Marshal(head); err == nil && len(hb) <= maxJSON {
		return lc.send(head, nil)
	}
	delete(head, key)
	head["body"] = len(b)
	return lc.send(head, b)
}

func (lc *liveConn) send(head map[string]any, body []byte) error {
	lc.wmu.Lock()
	defer lc.wmu.Unlock()
	if body == nil {
		return lc.fc.send(head, nil, 0)
	}
	return lc.fc.send(head, strings.NewReader(string(body)), int64(len(body)))
}

// assetTypes are the files the frontend is built from; anything else is
// not served.
var assetTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
}

var assetPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// assetPath maps a requested path to a file in the built frontend: "/"
// (or "") is index.html, a query is ignored; anything that could leave
// the folder, a hidden file or an unknown type is refused.
func assetPath(p string) (string, bool) {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		p = "index.html"
	}
	if len(p) > maxAssetPath || !assetPathPattern.MatchString(p) || !fs.ValidPath(p) {
		return "", false
	}
	for _, el := range strings.Split(p, "/") {
		if strings.HasPrefix(el, ".") {
			return "", false
		}
	}
	if _, ok := assetTypes[path.Ext(p)]; !ok {
		return "", false
	}
	return p, true
}

// readAsset reads one file of the built frontend, with its content type.
func readAsset(fsys fs.FS, p string) ([]byte, string, error) {
	if fsys == nil {
		return nil, "", fs.ErrNotExist
	}
	name, ok := assetPath(p)
	if !ok {
		return nil, "", fs.ErrPermission
	}
	st, err := fs.Stat(fsys, name)
	if err != nil {
		return nil, "", err
	}
	if !st.Mode().IsRegular() || st.Size() > maxLiveValue {
		return nil, "", fs.ErrPermission
	}
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, "", err
	}
	return b, assetTypes[path.Ext(name)], nil
}
