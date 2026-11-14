package phone

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeRecorder stands in for the recorder's import op.
type fakeRecorder struct {
	mu      sync.Mutex
	running bool
	state   string
	calls   []control.ImportArgs
}

func (f *fakeRecorder) Import(_ context.Context, a control.ImportArgs) (control.ImportStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, a)
	if !f.running {
		return control.ImportStatus{}, control.ErrNotRunning
	}
	return control.ImportStatus{Session: a.ID, State: f.state}, nil
}

type harness struct {
	t   *testing.T
	s   *Server
	h   http.Handler
	clk *clock
	rec *fakeRecorder
	dir string
	ads *fakeAdvertiser

	logMu sync.Mutex
	logs  []string
	asked []PairPrompt
}

// logged is everything the server logged.
func (h *harness) logged() string {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	return strings.Join(h.logs, "\n")
}

// fakeAdvertiser stands in for dns-sd.
type fakeAdvertiser struct {
	mu    sync.Mutex
	err   error
	calls []fakeAd
}

type fakeAd struct {
	instance string
	port     int
	txt      []string
	proc     *fakeProc
}

type fakeProc struct {
	once    sync.Once
	done    chan struct{}
	stopped bool
}

func (p *fakeProc) Stop()                 { p.stopped = true; p.end() }
func (p *fakeProc) end()                  { p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) Done() <-chan struct{} { return p.done }

func (f *fakeAdvertiser) start(instance string, port int, txt []string) (advertProc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	p := &fakeProc{done: make(chan struct{})}
	f.calls = append(f.calls, fakeAd{instance, port, txt, p})
	return p, nil
}

func (f *fakeAdvertiser) last() fakeAd {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeAd{}
	}
	return f.calls[len(f.calls)-1]
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	rec := &fakeRecorder{running: true, state: control.ImportWaiting}
	h := &harness{t: t, clk: clk, rec: rec, dir: dir, ads: &fakeAdvertiser{}}
	s, err := New(Config{Dir: filepath.Join(dir, "phone"), RecorderData: filepath.Join(dir, "recorder"), Import: rec.Import, Now: clk.now,
		Logf: func(f string, a ...any) {
			h.logMu.Lock()
			h.logs = append(h.logs, fmt.Sprintf(f, a...))
			h.logMu.Unlock()
			t.Logf(f, a...)
		},
		OnPairRequest: func(p PairPrompt) {
			h.logMu.Lock()
			h.asked = append(h.asked, p)
			h.logMu.Unlock()
		},
		Agenda: func(context.Context, int) ([]Event, error) { return []Event{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	s.advertise = h.ads.start // never the real dns-sd in tests
	h.s, h.h = s, s.Handler()
	return h
}

func (h *harness) do(method, path, token string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, req)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		h.t.Errorf("%s %s: Cache-Control %q", method, path, got)
	}
	return w
}

// pairRequest sends POST /v1/pair/request with a fresh nonce: the status,
// the request id, the phone's and the Mac's nonces.
func (h *harness) pairRequest(name string) (int, string, []byte, []byte) {
	h.t.Helper()
	nonce := make([]byte, 16)
	rand.Read(nonce)
	b, _ := json.Marshal(map[string]string{"device_name": name, "nonce": base64.StdEncoding.EncodeToString(nonce)})
	w := h.do("POST", "/v1/pair/request", "", b, nil)
	if w.Code != 200 {
		return w.Code, "", nonce, nil
	}
	var r struct {
		RequestID string `json:"request_id"`
		Nonce     string `json:"nonce"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		h.t.Fatal(err)
	}
	mac, err := base64.StdEncoding.DecodeString(r.Nonce)
	if err != nil || len(mac) != 16 || len(r.RequestID) != 32 {
		h.t.Fatalf("pair request reply %s", w.Body)
	}
	return w.Code, r.RequestID, nonce, mac
}

func (h *harness) confirm(id string) int {
	return h.do("POST", "/v1/pair/"+id+"/confirm", "", nil, nil).Code
}

// pairState is GET /v1/pair/{id}: the status and the reply.
func (h *harness) pairState(id string) (int, map[string]string) {
	w := h.do("GET", "/v1/pair/"+id, "", nil, nil)
	var r map[string]string
	json.Unmarshal(w.Body.Bytes(), &r)
	return w.Code, r
}

// pairPhone pairs a device and returns its token.
func (h *harness) pairPhone(name string) string {
	h.t.Helper()
	c, id, _, _ := h.pairRequest(name)
	if c != 200 {
		h.t.Fatalf("pair request: %d", c)
	}
	if err := h.s.AnswerPair(id, true); err != nil {
		h.t.Fatal(err)
	}
	if c := h.confirm(id); c != 204 {
		h.t.Fatalf("confirm: %d", c)
	}
	c, r := h.pairState(id)
	if c != 200 || r["state"] != PairPaired || r["device_id"] == "" || len(r["token"]) < 40 {
		h.t.Fatalf("pair state: %d %v", c, r)
	}
	return r["token"]
}

// The code derivation's fixed vector, also in docs/phone.md for the phone.
func TestPairCodeVector(t *testing.T) {
	seq := func(from, n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(from + i)
		}
		return b
	}
	if got := PairCode(seq(0x00, 32), seq(0x20, 16), seq(0x30, 16)); got != "452561" {
		t.Errorf("code %s, want 452561", got)
	}
	// Zero-padded to six digits.
	for i := 0; i < 2000; i++ {
		if c := PairCode(seq(i, 32), seq(i*7, 16), seq(i*13, 16)); len(c) != 6 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("code %q", c)
		}
	}
}

// The code the owner sees is the one the phone computes from the
// certificate it was shown and the two nonces.
func TestPairCodeMatchesPhone(t *testing.T) {
	h := newHarness(t)
	_, id, phoneNonce, macNonce := h.pairRequest("Test phone")
	p := h.s.PairPrompt()
	if p == nil || p.ID != id {
		t.Fatalf("prompt %+v", p)
	}
	fp, _ := hex.DecodeString(h.s.Fingerprint())
	if want := PairCode(fp, phoneNonce, macNonce); p.Code != want {
		t.Errorf("Mac shows %s, phone computes %s", p.Code, want)
	}
	if got := p.Expires.Sub(h.clk.now()); got != 2*time.Minute {
		t.Errorf("valid for %s", got)
	}
	if len(h.asked) != 1 || h.asked[0].Code != p.Code || h.asked[0].DeviceName != "Test phone" {
		t.Errorf("owner asked %+v", h.asked)
	}
}

func TestPairingBothOrders(t *testing.T) {
	for _, phoneFirst := range []bool{true, false} {
		h := newHarness(t)
		_, id, _, _ := h.pairRequest("Test phone")
		if _, r := h.pairState(id); r["state"] != PairPending {
			t.Fatalf("state %v", r)
		}
		first, second := func() { h.confirm(id) }, func() { h.s.AnswerPair(id, true) }
		if !phoneFirst {
			first, second = second, first
		}
		first()
		// One side alone gives nothing.
		if c, r := h.pairState(id); c != 200 || r["state"] != PairPending || r["token"] != "" {
			t.Fatalf("phoneFirst=%v one side: %d %v", phoneFirst, c, r)
		}
		if p := h.s.PairPrompt(); (p == nil) != !phoneFirst {
			t.Errorf("phoneFirst=%v: prompt %+v", phoneFirst, p)
		}
		second()
		if h.s.PairPrompt() != nil {
			t.Error("prompt after both confirmed")
		}
		c, r := h.pairState(id)
		if c != 200 || r["state"] != PairPaired || r["device_id"] == "" || r["token"] == "" {
			t.Fatalf("phoneFirst=%v both: %d %v", phoneFirst, c, r)
		}
		// The token is returned once: the request is then gone.
		if c, r2 := h.pairState(id); c != 404 || r2["token"] != "" {
			t.Errorf("second collection: %d %v", c, r2)
		}
		if c := h.do("GET", "/v1/agenda", r["token"], nil, nil).Code; c != 200 {
			t.Errorf("new token: %d", c)
		}
		devs := h.s.Devices()
		if len(devs) != 1 || devs[0].ID != r["device_id"] || devs[0].Name != "Test phone" || devs[0].TokenHash != "" {
			t.Errorf("devices %+v", devs)
		}
		// Nothing secret in the log.
		logs := h.logged()
		for _, secret := range []string{r["token"], id} {
			if strings.Contains(logs, secret) {
				t.Error("a token or request id was logged")
			}
		}
		if p := h.asked[0]; strings.Contains(logs, p.Code) {
			t.Error("the code was logged")
		}
	}
}

func TestPairingDeny(t *testing.T) {
	h := newHarness(t)
	_, id, _, _ := h.pairRequest("Test phone")
	if c := h.confirm(id); c != 204 {
		t.Fatalf("confirm: %d", c)
	}
	if err := h.s.AnswerPair(id, false); err != nil {
		t.Fatal(err)
	}
	if c, r := h.pairState(id); c != 200 || r["state"] != PairDenied || r["token"] != "" {
		t.Errorf("denied: %d %v", c, r)
	}
	if c := h.confirm(id); c != 409 {
		t.Errorf("confirm after deny: %d", c)
	}
	if err := h.s.AnswerPair(id, true); err == nil {
		t.Error("allowed after deny")
	}
	if n := len(h.s.Devices()); n != 0 {
		t.Errorf("%d devices", n)
	}
	// Unknown and malformed ids.
	if c := h.confirm(strings.Repeat("0", 32)); c != 404 {
		t.Errorf("unknown id: %d", c)
	}
	if c, _ := h.pairState(strings.Repeat("Z", 32)); c != 404 {
		t.Errorf("bad id: %d", c)
	}
	if err := h.s.AnswerPair("nope", true); err == nil {
		t.Error("answered an unknown request")
	}
}

func TestPairingExpiry(t *testing.T) {
	h := newHarness(t)
	_, id, _, _ := h.pairRequest("Test phone")
	h.confirm(id)
	h.clk.add(2 * time.Minute)
	if h.s.PairPrompt() != nil {
		t.Error("prompt after expiry")
	}
	if err := h.s.AnswerPair(id, true); err == nil {
		t.Error("allowed after expiry")
	}
	if c, r := h.pairState(id); c != 200 || r["state"] != PairExpired {
		t.Errorf("expired: %d %v", c, r)
	}
	// Both confirmed: the phone has two minutes to collect its token.
	h.clk.add(time.Minute)
	_, id, _, _ = h.pairRequest("Test phone")
	h.clk.add(time.Minute + 50*time.Second)
	h.s.AnswerPair(id, true)
	h.confirm(id)
	h.clk.add(2 * time.Minute)
	if c, r := h.pairState(id); c != 200 || r["state"] != PairExpired || r["token"] != "" {
		t.Errorf("uncollected: %d %v", c, r)
	}
	if n := len(h.s.Devices()); n != 0 {
		t.Errorf("%d devices", n)
	}
	// A finished request is forgotten after a while.
	h.clk.add(10 * time.Minute)
	if c, _ := h.pairState(id); c != 404 {
		t.Errorf("old request: %d", c)
	}
}

func TestPairingLimits(t *testing.T) {
	h := newHarness(t)
	// One pending request at a time.
	_, id, _, _ := h.pairRequest("First")
	if c, _, _, _ := h.pairRequest("Second"); c != 409 {
		t.Errorf("second pending request: %d", c)
	}
	if p := h.s.PairPrompt(); p == nil || p.ID != id {
		t.Errorf("prompt replaced: %+v", p)
	}
	h.s.AnswerPair(id, true)
	h.confirm(id)
	h.pairState(id)
	// Five requests per ten minutes.
	for i := 0; i < 4; i++ {
		h.clk.add(time.Second)
		h.pairPhone(fmt.Sprintf("Phone %d", i))
	}
	if c, _, _, _ := h.pairRequest("Sixth"); c != 429 {
		t.Errorf("sixth request: %d", c)
	}
	h.clk.add(10 * time.Minute)
	h.pairPhone("Later")

	// Three denials in ten minutes refuse requests for ten minutes.
	h = newHarness(t)
	for i := 0; i < 3; i++ {
		c, id, _, _ := h.pairRequest("Pest")
		if c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
		h.s.AnswerPair(id, false)
		h.clk.add(time.Minute)
	}
	if c, _, _, _ := h.pairRequest("Pest"); c != 429 {
		t.Errorf("after 3 denials: %d", c)
	}
	h.clk.add(9 * time.Minute) // ten minutes after the third denial
	h.pairPhone("Owner")
}

func TestPairRequestInput(t *testing.T) {
	h := newHarness(t)
	post := func(body string) int {
		return h.do("POST", "/v1/pair/request", "", []byte(body), nil).Code
	}
	for _, n := range []int{0, 15, 17, 32} {
		b, _ := json.Marshal(map[string]string{"device_name": "x", "nonce": base64.StdEncoding.EncodeToString(make([]byte, n))})
		if c := post(string(b)); c != 400 {
			t.Errorf("nonce of %d bytes: %d", n, c)
		}
	}
	if c := post(`{"device_name":"x","nonce":"not base64!!"}`); c != 400 {
		t.Errorf("bad base64: %d", c)
	}
	if c := post(`{"device_name":`); c != 400 {
		t.Errorf("bad JSON: %d", c)
	}
	if c := post(`{"device_name":"` + strings.Repeat("a", 5000) + `","nonce":"AAAAAAAAAAAAAAAAAAAAAA=="}`); c != 413 {
		t.Errorf("huge body: %d", c)
	}
	if h.s.PairPrompt() != nil {
		t.Fatal("a bad request was put to the owner")
	}
	// The name is made fit to show.
	b, _ := json.Marshal(map[string]string{"device_name": "  Evil\u202e\nphone\x00" + strings.Repeat("é", 100), "nonce": "AAAAAAAAAAAAAAAAAAAAAA"})
	if c := post(string(b)); c != 200 {
		t.Fatalf("unpadded nonce: %d", c)
	}
	p := h.s.PairPrompt()
	if p == nil || !strings.HasPrefix(p.DeviceName, "Evilphone") || len([]rune(p.DeviceName)) != maxDeviceName || strings.ContainsAny(p.DeviceName, "\u202e\n\x00") {
		t.Errorf("name %+v", p)
	}
}

func TestAuthAndRevoke(t *testing.T) {
	h := newHarness(t)
	tok := h.pairPhone("Test phone")
	b, _ := os.ReadFile(filepath.Join(h.dir, "phone", devicesFile))
	if strings.Contains(string(b), tok) {
		t.Fatal("token stored in the clear")
	}
	if c := h.do("GET", "/v1/agenda", "", nil, nil).Code; c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c := h.do("GET", "/v1/agenda", "x"+tok[1:], nil, nil).Code; c != 401 {
		t.Errorf("wrong token: %d", c)
	}
	if c := h.do("GET", "/v1/agenda?days=10", tok, nil, nil).Code; c != 200 {
		t.Errorf("good token: %d", c)
	}
	// The token never works in the URL.
	if c := h.do("GET", "/v1/agenda?token="+tok, "", nil, nil).Code; c != 401 {
		t.Errorf("token in URL: %d", c)
	}
	if err := h.s.Revoke(h.s.Devices()[0].ID); err != nil {
		t.Fatal(err)
	}
	if c := h.do("GET", "/v1/agenda", tok, nil, nil).Code; c != 401 {
		t.Errorf("revoked token: %d", c)
	}
	// Devices survive a restart.
	h2 := newHarness(t)
	tok2 := h2.pairPhone("Second phone")
	s, err := New(Config{Dir: filepath.Join(h2.dir, "phone"), Now: h2.clk.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.devs.auth(tok2, h2.clk.now()); !ok {
		t.Error("device lost on reopening")
	}
}

// adts is n ADTS frames with bodies of size body, plus extra bytes.
func adts(n, body int, seed byte) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		l := body + 7
		hdr := []byte{0xFF, 0xF1, 0x4C, byte(0x80 | (l>>11)&0x03), byte(l >> 3), byte((l&0x07)<<5 | 0x1F), 0xFC}
		out = append(out, hdr...)
		for j := 0; j < body; j++ {
			out = append(out, seed+byte(i+j))
		}
	}
	return out
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

const rid = "1b4e28ba-2fa1-41d2-883f-0016d3cca427"

func putChunk(h *harness, tok, id string, n string, b []byte, hash string) int {
	return h.do("PUT", "/v1/recordings/"+id+"/chunks/"+n, tok, b, map[string]string{"Content-Type": "audio/aac", "X-Sha256": hash}).Code
}

func TestChunks(t *testing.T) {
	h := newHarness(t)
	tok := h.pairPhone("Test phone")
	c0 := adts(3, 20, 1)
	if c := putChunk(h, tok, rid, "0", c0, sum(c0)); c != 204 {
		t.Fatalf("put: %d", c)
	}
	if c := putChunk(h, tok, rid, "0", c0, strings.ToUpper(sum(c0))); c != 204 {
		t.Errorf("same chunk again: %d", c)
	}
	other := adts(3, 20, 9)
	if c := putChunk(h, tok, rid, "0", other, sum(other)); c != 409 {
		t.Errorf("different bytes: %d", c)
	}
	if c := putChunk(h, tok, rid, "1", other, sum(c0)); c != 400 {
		t.Errorf("hash mismatch: %d", c)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "phone", "recordings", rid, chunkName(1))); err == nil {
		t.Error("mismatched chunk kept")
	}
	if c := h.do("PUT", "/v1/recordings/"+rid+"/chunks/1", tok, other, map[string]string{"Content-Type": "text/plain", "X-Sha256": sum(other)}).Code; c != 415 {
		t.Errorf("wrong type: %d", c)
	}
	if c := h.do("PUT", "/v1/recordings/"+rid+"/chunks/1", tok, other, map[string]string{"Content-Type": "audio/aac"}).Code; c != 400 {
		t.Errorf("no X-Sha256: %d", c)
	}
	for _, bad := range []struct{ id, n string }{{"not-a-uuid", "0"}, {rid, "10000"}, {rid, "-1"}, {rid, "1e3"}, {"..", "0"}, {"%2e%2e", "0"}} {
		// 307: the mux redirects a path with ".." to its clean form.
		if c := putChunk(h, tok, bad.id, bad.n, c0, sum(c0)); c != 400 && c != 404 && c != 307 {
			t.Errorf("rid %q n %q: %d", bad.id, bad.n, c)
		}
	}
	// Over 64 MB is refused before it is read.
	req := httptest.NewRequest("PUT", "/v1/recordings/"+rid+"/chunks/2", bytes.NewReader(c0))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "audio/aac")
	req.Header.Set("X-Sha256", sum(c0))
	req.ContentLength = maxChunkBytes + 1
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, req)
	if w.Code != 413 {
		t.Errorf("oversize: %d", w.Code)
	}
	// Another paired phone can't touch it.
	tok2 := h.pairPhone("Other phone")
	if c := putChunk(h, tok2, rid, "1", other, sum(other)); c != 404 {
		t.Errorf("other device's chunk: %d", c)
	}
	if c := h.do("GET", "/v1/recordings/"+rid, tok2, nil, nil).Code; c != 404 {
		t.Errorf("other device's state: %d", c)
	}
	if c := h.do("GET", "/v1/recordings/"+rid, tok, nil, nil).Code; c != 200 {
		t.Errorf("own state: %d", c)
	}
}

func metaJSON(id string, chunks ...[]byte) []byte {
	type ch struct {
		N      int    `json:"n"`
		Name   string `json:"name"`
		Size   int    `json:"size"`
		SHA256 string `json:"sha256"`
	}
	m := map[string]any{"version": 1, "id": id, "start": "2026-11-17T10:02:03.120+01:00", "end": "2026-11-17T10:52:03.120+01:00",
		"device_name": "Test phone", "event": map[string]any{"id": "calendar:1a2b3c", "subject": "Booth demo", "start": "2026-11-17T10:00:00+01:00"}}
	var cs []ch
	for i, c := range chunks {
		cs = append(cs, ch{i, chunkName(i), len(c), sum(c)})
	}
	m["chunks"] = cs
	b, _ := json.Marshal(m)
	return b
}

func TestComplete(t *testing.T) {
	h := newHarness(t)
	tok := h.pairPhone("Test phone")
	c0 := adts(4, 30, 1)
	c1 := append(adts(2, 30, 5), adts(1, 30, 7)[:12]...) // the last frame cut short
	putChunk(h, tok, rid, "0", c0, sum(c0))
	meta := metaJSON(rid, c0, c1)
	complete := func(b []byte) *httptest.ResponseRecorder {
		return h.do("POST", "/v1/recordings/"+rid+"/complete", tok, b, map[string]string{"Content-Type": "application/json"})
	}

	// A missing chunk is named.
	w := complete(meta)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"chunks":[1]`) {
		t.Fatalf("missing chunk: %d %s", w.Code, w.Body)
	}
	putChunk(h, tok, rid, "1", c1, sum(c1))
	// A chunk damaged on disk is dropped and asked for again.
	path := filepath.Join(h.dir, "phone", "recordings", rid, chunkName(0))
	os.WriteFile(path, adts(4, 30, 99), 0o600)
	w = complete(meta)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"chunks":[0]`) {
		t.Fatalf("corrupt chunk: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("corrupt chunk kept")
	}
	putChunk(h, tok, rid, "0", c0, sum(c0))
	// Meta for another recording, or nonsense, is refused.
	if c := complete(metaJSON("2b4e28ba-2fa1-41d2-883f-0016d3cca427", c0, c1)).Code; c != 400 {
		t.Errorf("other id: %d", c)
	}
	if c := complete([]byte("{")).Code; c != 400 {
		t.Errorf("bad json: %d", c)
	}

	w = complete(meta)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"state":"waiting"`) {
		t.Fatalf("complete: %d %s", w.Code, w.Body)
	}
	inbox := filepath.Join(h.dir, "recorder", "inbox", rid+".aac")
	joined, err := os.ReadFile(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte{}, c0...), c1[:len(c1)-12]...); !bytes.Equal(joined, want) {
		t.Errorf("joined %d bytes, want %d (whole frames only)", len(joined), len(want))
	}
	a := h.rec.calls[len(h.rec.calls)-1]
	if a.ID != rid || a.Path != inbox || a.EventID != "calendar:1a2b3c" || !a.Retry ||
		!a.StartedAt.Equal(time.Date(2026, 11, 17, 9, 2, 3, 120e6, time.UTC)) {
		t.Errorf("import args %+v", a)
	}
	if _, off := a.StartedAt.Zone(); off != 3600 {
		t.Errorf("start lost its zone offset: %d", off)
	}
	// Completing again asks nothing new; no more chunks are taken.
	n := len(h.rec.calls)
	if c := complete(meta).Code; c != 202 || len(h.rec.calls) != n {
		t.Errorf("complete again: %d, %d new calls", c, len(h.rec.calls)-n)
	}
	if c := putChunk(h, tok, rid, "2", c0, sum(c0)); c != 409 {
		t.Errorf("chunk after complete: %d", c)
	}

	// The recorder down: still waiting. Then imported: chunks dropped.
	h.rec.running = false
	state := func() string { return h.do("GET", "/v1/recordings/"+rid, tok, nil, nil).Body.String() }
	if s := state(); !strings.Contains(s, `"state":"waiting"`) {
		t.Errorf("recorder down: %s", s)
	}
	h.rec.running, h.rec.state = true, control.ImportImported
	if s := state(); !strings.Contains(s, `"state":"imported"`) || !strings.Contains(s, `"session_id":"`+rid+`"`) {
		t.Errorf("imported: %s", s)
	}
	if h.rec.calls[len(h.rec.calls)-1].Retry {
		t.Error("a state poll asked for a retry")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("chunks kept after the import")
	}
}

func TestCompleteNotADTS(t *testing.T) {
	h := newHarness(t)
	tok := h.pairPhone("Test phone")
	junk := []byte("this is not audio at all, just text pretending")
	putChunk(h, tok, rid, "0", junk, sum(junk))
	w := h.do("POST", "/v1/recordings/"+rid+"/complete", tok, metaJSON(rid, junk), nil)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"state":"failed"`) {
		t.Fatalf("junk: %d %s", w.Code, w.Body)
	}
	if len(h.rec.calls) != 0 {
		t.Error("junk sent to the recorder")
	}
	// A failed recording takes a chunk again and completes again.
	if c := putChunk(h, tok, rid, "1", junk, sum(junk)); c != 204 {
		t.Errorf("chunk after failure: %d", c)
	}
}

func TestPrivateAddresses(t *testing.T) {
	for s, want := range map[string]bool{
		"10.1.2.3": true, "172.16.0.1": true, "172.31.255.255": true, "172.32.0.1": false, "192.168.1.20": true,
		"100.64.0.1": true, "100.127.255.254": true, "100.128.0.1": false, "169.254.10.1": true, "127.0.0.1": true,
		"::1": true, "fe80::1": true, "::ffff:192.168.1.2": true, "::ffff:8.8.8.8": false,
		"8.8.8.8": false, "1.1.1.1": false, "2001:db8::1": false, "fd7a:115c:a1e0::1": false,
	} {
		if got := PrivateAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: %v", s, got)
		}
	}
	if remoteAllowed(&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 5000}) {
		t.Error("public peer allowed")
	}
	if !remoteAllowed(&net.TCPAddr{IP: net.ParseIP("192.168.0.9"), Port: 5000}) {
		t.Error("private peer refused")
	}
}

// fakeListener hands out connections from a given peer.
type fakeListener struct {
	conns chan net.Conn
}

type peerConn struct {
	net.Conn
	peer net.Addr
}

func (c peerConn) RemoteAddr() net.Addr { return c.peer }

func (l *fakeListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *fakeListener) Close() error   { return nil }
func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestListenerClosesPublicPeers(t *testing.T) {
	fl := &fakeListener{conns: make(chan net.Conn, 2)}
	pubA, pubB := net.Pipe()
	privA, privB := net.Pipe()
	defer pubB.Close()
	defer privB.Close()
	fl.conns <- peerConn{pubA, &net.TCPAddr{IP: net.ParseIP("198.51.100.4"), Port: 1}}
	fl.conns <- peerConn{privA, &net.TCPAddr{IP: net.ParseIP("192.168.1.4"), Port: 1}}
	refused := 0
	c, err := privateListener{Listener: fl, refused: func(net.Addr) { refused++ }}.Accept()
	if err != nil || c.RemoteAddr().String() != "192.168.1.4:1" || refused != 1 {
		t.Fatalf("accepted %v (%v), refused %d", c, err, refused)
	}
	// The public peer's connection was closed.
	pubB.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := pubB.Read(make([]byte, 1)); err == nil {
		t.Error("public connection left open")
	}
}

// Serving for real: TLS with the stored certificate, which a client
// pinning its fingerprint accepts.
func TestServeTLSPinned(t *testing.T) {
	h := newHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.s.serveOn(ctx, ln) }()
	defer func() { cancel(); <-done }()
	fp := h.s.Fingerprint()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin below, as the phone does
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			s := sha256.Sum256(raw[0])
			if hex.EncodeToString(s[:]) != fp {
				return errors.New("pin mismatch")
			}
			return nil
		},
	}}}
	resp, err := client.Get(fmt.Sprintf("https://%s/v1/agenda", ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("unauthenticated: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	// Plain HTTP gets nowhere.
	if resp, err := http.Get(fmt.Sprintf("http://%s/v1/agenda", ln.Addr())); err == nil {
		if resp.StatusCode == 401 || resp.StatusCode == 200 {
			t.Errorf("plain HTTP answered %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// fakeAgendaStore holds synthetic events, labels and briefs.
type fakeAgendaStore struct {
	items  []item.Item
	labels map[string]sensitivity.Label
	briefs map[string]store.PrepBrief
}

func (f fakeAgendaStore) AllItems() ([]item.Item, error) { return f.items, nil }
func (f fakeAgendaStore) ItemLabel(id string) (sensitivity.Label, []string, error) {
	l, ok := f.labels[id]
	if !ok {
		return sensitivity.Label{}, nil, errors.New("no label")
	}
	return l, nil, nil
}
func (f fakeAgendaStore) PrepBriefFor(sc sensitivity.Scope, id string) (*store.PrepBrief, error) {
	b, ok := f.briefs[id]
	if !ok || !sensitivity.Visible(b.Label, nil, sc) {
		return nil, nil
	}
	return &b, nil
}

func TestAgendaScope(t *testing.T) {
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	ev := func(id string, in time.Duration, native map[string]string) item.Item {
		return item.Item{ID: id, Kind: item.KindEvent, Title: "Subject " + id, OccurredAt: now.Add(in), EndedAt: now.Add(in + time.Hour), Native: native,
			Participants: []item.Participant{{Name: "Robin Vale", Role: "organizer"}, {Name: "Kai Moreno", Role: "attendee"}, {Name: "Sam Owner", Role: "attendee", Owner: true}}}
	}
	day := 24 * time.Hour
	f := fakeAgendaStore{
		items: []item.Item{
			ev("calendar:later", 3*day, map[string]string{"location": "Hall B"}),
			ev("calendar:now", -30*time.Minute, nil), // under way: kept
			ev("calendar:over", -2*time.Hour, nil),   // ended: dropped
			ev("calendar:far", 13*day, nil),          // inside 14 days
			ev("calendar:too-far", 15*day, nil),      // beyond the cap
			ev("calendar:cancelled", day, map[string]string{"cancelled": "true"}),
			ev("calendar:private", 2*day, nil),    // confidential event
			ev("calendar:unlabelled", 2*day, nil), // no label: confidential
			{ID: "tomoe:m", Kind: item.KindMeeting, OccurredAt: now.Add(day), EndedAt: now.Add(day + time.Hour)},
		},
		labels: map[string]sensitivity.Label{
			"calendar:later": {Level: sensitivity.Internal}, "calendar:now": {Level: sensitivity.Internal},
			"calendar:over": {Level: sensitivity.Internal}, "calendar:far": {Level: sensitivity.Internal},
			"calendar:too-far": {Level: sensitivity.Internal}, "calendar:cancelled": {Level: sensitivity.Internal},
			"calendar:private": {Level: sensitivity.Confidential},
		},
		briefs: map[string]store.PrepBrief{
			"calendar:later": {ItemID: "calendar:later", Text: "Ask about the rollout.", Label: sensitivity.Label{Level: sensitivity.Confidential}, MadeAt: now},
			"calendar:far":   {ItemID: "calendar:far", Text: "Public plan.", Label: sensitivity.Label{Level: sensitivity.Public}, MadeAt: now},
		},
	}
	ids := func(evs []Event) string {
		var s []string
		for _, e := range evs {
			s = append(s, e.ID)
		}
		return strings.Join(s, " ")
	}
	evs, err := BuildAgenda(f, sensitivity.Owner(), now, 30)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(evs); got != "calendar:now calendar:private calendar:unlabelled calendar:later calendar:far" {
		t.Errorf("owner, 30 days (capped at 14): %s", got)
	}
	if got := ids(must(BuildAgenda(f, sensitivity.Owner(), now, 10))); got != "calendar:now calendar:private calendar:unlabelled calendar:later" {
		t.Errorf("owner, 10 days: %s", got)
	}
	later := evs[3]
	if later.Location != "Hall B" || later.Organizer != "Robin Vale" || len(later.Attendees) != 2 || later.Brief == nil {
		t.Fatalf("event %+v", later)
	}
	if b := later.Brief.Sensitivity; b.Level != "confidential" || b.Share != sensitivity.ShareParty || b.Why == "" {
		t.Errorf("brief label %+v", b)
	}
	// A narrower scope sees neither the confidential events nor the
	// confidential brief.
	narrow := must(BuildAgenda(f, sensitivity.UpTo(sensitivity.ShareInternal), now, 14))
	if got := ids(narrow); got != "calendar:now calendar:later calendar:far" {
		t.Errorf("internal scope: %s", got)
	}
	if narrow[1].Brief != nil || narrow[2].Brief == nil {
		t.Error("internal scope: briefs not scoped")
	}
	// The reply's JSON shape is the contract's.
	b, _ := json.Marshal(Agenda{Events: evs[3:4], Drafting: 2})
	for _, k := range []string{`"id":`, `"start":`, `"end":`, `"subject":`, `"location":`, `"organizer":`, `"attendees":[{"name":`, `"brief":{"text":`, `"made_at":`, `"sensitivity":{"level":`, `"share":`, `"why":`, `"drafting":2`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("agenda JSON lacks %s: %s", k, b)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestAgendaDays(t *testing.T) {
	h := newHarness(t)
	var asked []int
	h.s.cfg.Agenda = func(_ context.Context, days int) ([]Event, error) { asked = append(asked, days); return []Event{}, nil }
	var until []time.Time
	h.s.cfg.Drafting = func(u time.Time) int { until = append(until, u); return 3 }
	h.h = h.s.Handler()
	tok := h.pairPhone("Test phone")
	for q, want := range map[string]int{"": 10, "?days=3": 3, "?days=14": 14, "?days=99": 14} {
		asked = nil
		w := h.do("GET", "/v1/agenda"+q, tok, nil, nil)
		if w.Code != 200 || len(asked) != 1 || asked[0] != want || !strings.Contains(w.Body.String(), `"drafting":3`) {
			t.Errorf("%q: %d %v %s", q, w.Code, asked, w.Body)
		}
	}
	for _, q := range []string{"?days=0", "?days=-1", "?days=abc", "?days=99999"} {
		if c := h.do("GET", "/v1/agenda"+q, tok, nil, nil).Code; c != 400 {
			t.Errorf("%q: %d", q, c)
		}
	}
	var longest time.Duration
	for _, u := range until {
		longest = max(longest, u.Sub(h.clk.now()))
	}
	if longest != 14*24*time.Hour {
		t.Errorf("drafting window %s", longest)
	}
}

func TestDrafter(t *testing.T) {
	d := &Drafter{Every: time.Hour}
	now := time.Now()
	release := make(chan struct{})
	var visit func(string)
	got := make(chan struct{})
	n := d.Start(now, func() []string { return []string{"calendar:a", "calendar:b"} }, func(v func(string)) {
		visit = v
		close(got)
		<-release
	})
	<-got
	if n != 2 {
		t.Fatalf("first: %d", n)
	}
	visit("calendar:a")
	if n := d.Start(now, func() []string { t.Error("missing asked during a pass"); return nil }, nil); n != 1 {
		t.Errorf("during the pass: %d", n)
	}
	close(release)
	for i := 0; i < 100; i++ {
		d.mu.Lock()
		running := d.running
		d.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Within Every of the last pass, nothing starts.
	if n := d.Start(time.Now(), func() []string { t.Error("missing asked too soon"); return nil }, nil); n != 0 {
		t.Errorf("too soon: %d", n)
	}
	// Later, with nothing missing, nothing runs.
	if n := d.Start(time.Now().Add(2*time.Hour), func() []string { return nil }, func(func(string)) { t.Error("ran with nothing missing") }); n != 0 {
		t.Errorf("nothing missing: %d", n)
	}
}
