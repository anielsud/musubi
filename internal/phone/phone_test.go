package phone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
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
	mu       sync.Mutex
	running  bool
	state    string
	imported map[string]bool // sessions saved before, found by id alone
	calls    []control.ImportArgs
}

func (f *fakeRecorder) Import(_ context.Context, a control.ImportArgs) (control.ImportStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, a)
	if !f.running {
		return control.ImportStatus{}, control.ErrNotRunning
	}
	if f.imported[a.ID] {
		return control.ImportStatus{Session: a.ID, State: control.ImportImported}, nil
	}
	if _, err := os.Lstat(a.Path); err != nil {
		return control.ImportStatus{}, errors.New("no such file in the inbox")
	}
	return control.ImportStatus{Session: a.ID, State: f.state}, nil
}

func (f *fakeRecorder) set(running bool, state string) {
	f.mu.Lock()
	f.running, f.state = running, state
	f.mu.Unlock()
}

type harness struct {
	t   *testing.T
	s   *Syncer
	clk *clock
	rec *fakeRecorder
	dir string

	logMu      sync.Mutex
	logs       []string
	asked      []PairPrompt
	agendaDays []int
	draftUntil []time.Time
}

func (h *harness) logged() string {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	return strings.Join(h.logs, "\n")
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	rec := &fakeRecorder{running: true, state: control.ImportWaiting, imported: map[string]bool{}}
	h := &harness{t: t, clk: clk, rec: rec, dir: dir}
	s, err := New(Config{Dir: filepath.Join(dir, "phone"), RecorderData: filepath.Join(dir, "recorder"), MacName: "Test Mac", Import: rec.Import, Now: clk.now,
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
		Agenda: func(_ context.Context, days int) ([]Event, error) {
			h.logMu.Lock()
			h.agendaDays = append(h.agendaDays, days)
			h.logMu.Unlock()
			return []Event{{ID: "calendar:1", Subject: "Booth demo", Attendees: []Attendee{}}}, nil
		},
		Drafting: func(until time.Time) int {
			h.logMu.Lock()
			h.draftUntil = append(h.draftUntil, until)
			h.logMu.Unlock()
			return 2
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.dnssd = func(args ...string) (dnssdProc, error) {
		t.Errorf("dns-sd %v run in a test without a fake", args)
		return nil, errDNSSDUnsupported
	}
	h.s = s
	return h
}

// pairWith makes p and the Mac paired with each other, as a pairing would.
func (h *harness) pairWith(p *fakePhone) {
	h.t.Helper()
	p.mu.Lock()
	p.pinnedMac = h.s.fpRaw
	p.mu.Unlock()
	if err := h.s.pins.pin(Device{ID: p.fpHex(), Name: "Old name", Paired: h.clk.now(), Addresses: []string{}}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) syncTarget(p *fakePhone) target {
	return target{fp16: p.fpHex()[:16], pin: p.fpHex(), addrs: []netip.AddrPort{p.addr()}}
}

func (h *harness) syncOnce(p *fakePhone) {
	h.s.session(context.Background(), h.syncTarget(p))
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// adts is n ADTS frames of body bytes each.
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

const (
	rid  = "1b4e28ba-2fa1-41d2-883f-0016d3cca427"
	rid2 = "6f9619ff-8b86-4011-b42d-00cf4fc964ff"
)

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

func TestPairCodeVectorV2(t *testing.T) {
	seq := func(from byte, n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = from + byte(i)
		}
		return b
	}
	mac, ph, mn, pn := seq(0x00, 32), seq(0x20, 32), seq(0x40, 16), seq(0x50, 16)
	h := sha256.New()
	h.Write([]byte("musubi-pair-v2"))
	for _, b := range [][]byte{mac, ph, mn, pn} {
		h.Write(b)
	}
	if got := hex.EncodeToString(h.Sum(nil)[:4]); got != "c1d59190" {
		t.Errorf("hash starts %s", got)
	}
	if got := PairCode(mac, ph, mn, pn); got != "999120" {
		t.Errorf("code %s, want 999120", got)
	}
	if PairCode(ph, mac, mn, pn) == "999120" {
		t.Error("the certificates' order doesn't matter")
	}
}

// pairInBackground starts a pairing with p and returns when the Mac asks
// the owner; done is closed when the pairing (and any sync after it) ends.
func pairInBackground(t *testing.T, h *harness, p *fakePhone) (prompt *PairPrompt, done chan struct{}) {
	t.Helper()
	p.mu.Lock()
	p.pairing = true
	p.mu.Unlock()
	h.logMu.Lock()
	before := len(h.asked)
	h.logMu.Unlock()
	done = make(chan struct{})
	if !h.s.start(context.Background(), target{fp16: p.fpHex()[:16], pair: true, addrs: []netip.AddrPort{p.addr()}}) {
		t.Fatal("not started")
	}
	go func() { h.s.wg.Wait(); close(done) }()
	waitFor(t, "the prompt", func() bool {
		h.logMu.Lock()
		asked := len(h.asked) > before
		h.logMu.Unlock()
		prompt = h.s.PairPrompt() // nil again at once when the phone hangs up
		return asked
	})
	return prompt, done
}

func TestPairingConfirmedOnBoth(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	p.addresses = []string{"10.1.2.3"}
	pr, done := pairInBackground(t, h, p)
	p.mu.Lock()
	code := p.code
	p.mu.Unlock()
	if pr.Code != code || len(code) != 6 || pr.DeviceName != "Test phone" {
		t.Fatalf("prompt %+v, phone's code %s", pr, code)
	}
	if len(h.asked) != 1 {
		t.Errorf("asked %d times", len(h.asked))
	}
	if err := h.s.AnswerPair("0123", true); err == nil {
		t.Error("a wrong id answered")
	}
	if err := h.s.AnswerPair(pr.ID, true); err != nil {
		t.Fatal(err)
	}
	<-done
	devs := h.s.Devices()
	if len(devs) != 1 || devs[0].ID != p.fpHex() || devs[0].Name != "Test phone" || !devs[0].Paired.Equal(h.clk.now()) {
		t.Fatalf("devices %+v", devs)
	}
	// Paired: it synced at once, and learnt the phone's addresses.
	if ops := strings.Join(p.opList(), " "); ops != "pair_request pair_confirm hello list agenda bye" {
		t.Errorf("ops %s", ops)
	}
	if got := h.s.Devices()[0]; !reflect.DeepEqual(got.Addresses, []string{"10.1.2.3"}) || !strings.HasPrefix(got.LastOutcome, "synced") {
		t.Errorf("after the sync %+v", got)
	}
	fi, err := os.Stat(filepath.Join(h.dir, "phone", pinsFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("phones.json %v %v", fi, err)
	}
	if h.s.PairPrompt() != nil {
		t.Error("prompt left open")
	}
	if l := h.logged(); strings.Contains(l, code) {
		t.Error("the code was logged")
	}
	// Reopened, the pin is there.
	s2, err := New(Config{Dir: filepath.Join(h.dir, "phone")})
	if err != nil || len(s2.Devices()) != 1 {
		t.Errorf("reopened: %v %v", s2.Devices(), err)
	}
}

func TestPairingNotConfirmedOnPhone(t *testing.T) {
	for _, code := range []string{"cancelled", "expired"} {
		h := newHarness(t)
		p := newFakePhone(t)
		p.confirm = code
		pr, done := pairInBackground(t, h, p)
		h.s.AnswerPair(pr.ID, true)
		<-done
		if len(h.s.Devices()) != 0 {
			t.Errorf("%s: pinned anyway", code)
		}
		if !strings.Contains(h.logged(), "not confirmed on the phone ("+code+")") {
			t.Errorf("%s: not logged", code)
		}
	}
}

func TestPairingDeniedThreeTimes(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	for i := 0; i < 3; i++ {
		pr, done := pairInBackground(t, h, p)
		if err := h.s.AnswerPair(pr.ID, false); err != nil {
			t.Fatal(err)
		}
		<-done
	}
	if len(h.s.Devices()) != 0 || p.cancels != 3 {
		t.Fatalf("devices %v, cancels %d", h.s.Devices(), p.cancels)
	}
	if !h.s.pairIgnored(p.fpHex()[:16]) {
		t.Fatal("not ignored after 3 denials")
	}
	// Seen again asking to pair: not even dialed.
	h.s.dnssd = func(args ...string) (dnssdProc, error) { t.Errorf("dns-sd %v", args); return nil, errDNSSDUnsupported }
	h.s.consider(context.Background(), sighting{host: "phone.local.", port: p.port, txt: map[string]string{"v": "2", "fp": p.fpHex()[:16], "pair": "1"}}, false)
	h.s.wg.Wait()
	// After 10 minutes it may ask again.
	h.clk.add(pairIgnoreFor)
	if h.s.pairIgnored(p.fpHex()[:16]) {
		t.Error("still ignored")
	}
}

func TestPairingExpires(t *testing.T) {
	h := newHarness(t)
	h.s.pairWait = 200 * time.Millisecond
	p := newFakePhone(t)
	_, done := pairInBackground(t, h, p)
	<-done
	if len(h.s.Devices()) != 0 || p.cancels != 1 || h.s.PairPrompt() != nil {
		t.Errorf("devices %v, cancels %d", h.s.Devices(), p.cancels)
	}
	if !strings.Contains(h.logged(), "not answered in time") {
		t.Error("expiry not logged")
	}
}

func TestPairingPhoneCancels(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	p.hangUpAfterRequest = true
	start := time.Now()
	_, done := pairInBackground(t, h, p)
	<-done
	if time.Since(start) > 10*time.Second || h.s.PairPrompt() != nil || len(h.s.Devices()) != 0 {
		t.Error("the prompt outlived the phone's connection")
	}
	if !strings.Contains(h.logged(), "the phone cancelled") {
		t.Error("not logged")
	}
}

func TestPairingWrongCert(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	p.pairing = true
	// Advertised under another certificate's fp: refused in the handshake.
	h.s.session(context.Background(), target{fp16: "0123456789abcdef", pair: true, addrs: []netip.AddrPort{p.addr()}})
	if len(p.opList()) != 0 || len(h.asked) != 0 {
		t.Errorf("ops %v, asked %d", p.opList(), len(h.asked))
	}
}

func TestOnePromptAtATime(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	pr, done := pairInBackground(t, h, p)
	q := newFakePhone(t)
	q.pairing = true
	if h.s.start(context.Background(), target{fp16: q.fpHex()[:16], pair: true, addrs: []netip.AddrPort{q.addr()}}) {
		t.Error("a second pairing started")
	}
	h.s.AnswerPair(pr.ID, false)
	<-done
}

func TestPinnedCertRefused(t *testing.T) {
	h := newHarness(t)
	paired := newFakePhone(t)
	h.pairWith(paired)
	other := newFakePhone(t)
	other.pinnedMac = h.s.fpRaw // it would talk; the Mac must not
	h.s.session(context.Background(), target{fp16: paired.fpHex()[:16], pin: paired.fpHex(), addrs: []netip.AddrPort{other.addr()}})
	if len(other.opList()) != 0 {
		t.Errorf("talked to the wrong phone: %v", other.opList())
	}
	if d := h.s.Devices()[0]; !strings.Contains(d.LastOutcome, "refused") {
		t.Errorf("outcome %q", d.LastOutcome)
	}
}

func TestSyncFetchImportReport(t *testing.T) {
	h := newHarness(t)
	h.rec.set(false, "")
	p := newFakePhone(t)
	p.addresses = []string{"192.168.1.20", "8.8.8.8", "fe80::1%wlan0", "100.101.102.103", "fd7a:115c:a1e0::5"}
	h.pairWith(p)
	c0, c1 := adts(4, 30, 1), append(adts(2, 30, 5), adts(1, 30, 7)[:12]...) // the last frame cut short
	p.add(rid, c0, c1)
	p.add(rid2, adts(3, 20, 9))

	// The recorder is off: both wait.
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list chunk chunk state chunk state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	if p.states[rid] != "waiting" || p.states[rid2] != "waiting" {
		t.Errorf("states %v", p.states)
	}
	inbox := filepath.Join(h.dir, "recorder", "inbox", rid+".aac")
	b, err := os.ReadFile(inbox)
	if want := append(append([]byte{}, c0...), adts(2, 30, 5)...); err != nil || !bytes.Equal(b, want) {
		t.Errorf("joined audio: %d bytes, want %d (%v)", len(b), len(want), err)
	}
	if fi, _ := os.Stat(inbox); fi.Mode().Perm() != 0o600 {
		t.Errorf("inbox mode %v", fi.Mode())
	}
	d := h.s.Devices()[0]
	if d.Name != "Test phone" || !reflect.DeepEqual(d.Addresses, []string{"192.168.1.20", "100.101.102.103", "fd7a:115c:a1e0::5"}) {
		t.Errorf("device %+v", d)
	}
	if !strings.HasPrefix(d.LastOutcome, "synced: 2 recordings on the phone, 2 fetched") || !d.LastSync.Equal(h.clk.now()) {
		t.Errorf("outcome %q", d.LastOutcome)
	}
	if len(p.agendas) != 1 || len(p.agendas[0].Events) != 1 || p.agendas[0].Drafting != 2 || !p.agendas[0].MadeAt.Equal(h.clk.now()) {
		t.Errorf("agenda %+v", p.agendas)
	}
	if !reflect.DeepEqual(h.agendaDays, []int{10}) || !h.draftUntil[0].Equal(h.clk.now().Add(10*24*time.Hour)) {
		t.Errorf("agenda asked %v until %v", h.agendaDays, h.draftUntil)
	}

	// The recorder is back and transcribes; then it has them saved.
	h.rec.set(true, control.ImportTranscribing)
	h.clk.add(time.Minute)
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list state state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	h.rec.set(true, control.ImportImported)
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list state state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	last := p.reports[len(p.reports)-1]
	if last["state"] != "imported" || last["session_id"] != rid2 {
		t.Errorf("report %v", last)
	}
	a := h.rec.calls[len(h.rec.calls)-2]
	if a.ID != rid || a.Path != inbox || a.EventID != "calendar:1a2b3c" || a.StartedAt.Format(time.RFC3339) != "2026-11-17T10:02:03+01:00" {
		t.Errorf("import args %+v", a)
	}
	if _, err := os.Stat(filepath.Join(h.s.recDir(rid), chunkName(0))); err == nil {
		t.Error("chunks kept after the import")
	}
	// Imported is final: nothing more is fetched or reported.
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list agenda bye" {
		t.Errorf("ops %s", ops)
	}
}

func TestChunkDamaged(t *testing.T) {
	for _, fault := range []string{"sha", "bytes", "short"} {
		t.Run(fault, func(t *testing.T) {
			h := newHarness(t)
			p := newFakePhone(t)
			h.pairWith(p)
			p.add(rid, adts(3, 30, 1), adts(3, 30, 2))
			p.fault[rid+"/1"] = fault
			h.syncOnce(p)
			if p.states[rid] != "failed" || p.reports[0]["error"] != "chunk 1 arrived damaged" {
				t.Fatalf("reports %v", p.reports)
			}
			if _, err := os.Stat(filepath.Join(h.s.recDir(rid), chunkName(1))); err == nil {
				t.Error("damaged chunk kept")
			}
			if _, err := os.Stat(filepath.Join(h.dir, "recorder", "inbox", rid+".aac")); err == nil {
				t.Error("joined with a damaged chunk")
			}
			// Sent right the next time: only that chunk is fetched.
			delete(p.fault, rid+"/1")
			p.ops = nil
			h.syncOnce(p)
			if ops := strings.Join(p.opList(), " "); ops != "hello list chunk state agenda bye" || p.states[rid] != "waiting" {
				t.Errorf("ops %s, state %s", ops, p.states[rid])
			}
		})
	}
}

func TestResumeAfterInterruption(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	h.pairWith(p)
	p.add(rid, adts(2, 30, 1), adts(2, 30, 2), adts(2, 30, 3))
	p.dropAfter = 2
	h.syncOnce(p)
	if len(p.reports) != 0 || len(h.rec.calls) != 1 { // only asked whether it was imported before
		t.Fatalf("reports %v, recorder calls %v", p.reports, h.rec.calls)
	}
	if n := len(must(os.ReadDir(h.s.recDir(rid)))); n != 3 { // state.json and 2 chunks
		t.Errorf("%d files staged", n)
	}
	if d := h.s.Devices()[0]; !strings.HasPrefix(d.LastOutcome, "failed") {
		t.Errorf("outcome %q", d.LastOutcome)
	}
	p.dropAfter = 0
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list chunk state agenda bye" || p.states[rid] != "waiting" {
		t.Errorf("ops %s, state %v", ops, p.states)
	}
}

func TestAlreadyImportedNotFetched(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	h.pairWith(p)
	p.add(rid, adts(2, 30, 1))
	p.states[rid] = "transcribing" // reported before the staging folder was lost
	h.rec.imported[rid] = true
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list state agenda bye" || p.states[rid] != "imported" {
		t.Errorf("ops %s, states %v", ops, p.states)
	}
}

func TestOtherPhonesRecording(t *testing.T) {
	h := newHarness(t)
	a, b := newFakePhone(t), newFakePhone(t)
	h.pairWith(a)
	h.pairWith(b)
	a.add(rid, adts(2, 30, 1))
	b.add(rid, adts(2, 30, 1))
	h.syncOnce(a)
	h.syncOnce(b)
	if ops := strings.Join(b.opList(), " "); ops != "hello list agenda bye" {
		t.Errorf("another phone's recording fetched: %s", ops)
	}
}

func TestDialAddresses(t *testing.T) {
	for in, want := range map[string]bool{
		"192.168.1.20": true, "10.0.0.1": true, "172.16.5.4": true, "172.32.0.1": false, "100.64.0.1": true,
		"100.128.0.1": false, "8.8.8.8": false, "127.0.0.1": true, "169.254.10.1": true, "::1": true,
		"fd7a:115c:a1e0::1": true, "fd7a:115c:a1e1::1": false, "fd00::1": false, "2001:db8::1": false,
		"FE80:0000:0000:0000:0071:83EA:D0E4:EB68%en0": true, "fe80::1": false, "fe80::1%bad zone": false,
		"0000:0000:0000:0000:0000:0000:0000:0001%<0>": true, "::ffff:192.168.0.1": true, "not an ip": false, "": false,
	} {
		if _, got := parseDialAddr(in); got != want {
			t.Errorf("parseDialAddr(%q) = %v, want %v", in, got, want)
		}
	}
	got := reportedAddrs([]string{"192.168.1.20", "192.168.1.20", "8.8.8.8", "127.0.0.1", "fe80::1%wlan0", "100.100.1.1"})
	if !reflect.DeepEqual(got, []string{"192.168.1.20", "100.100.1.1"}) {
		t.Errorf("reported %v", got)
	}
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("10.0.0.%d", i))
	}
	if n := len(reportedAddrs(many)); n != maxAddrs {
		t.Errorf("%d kept", n)
	}
}

func TestDialKnownAddresses(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	h.pairWith(p)
	h.s.pins.update(p.fpHex(), func(d *Device) { d.Addresses = []string{"8.8.8.8", "127.0.0.1"} })
	h.s.phonePort = p.port
	h.s.dialKnown(context.Background())
	h.s.wg.Wait()
	if ops := strings.Join(p.opList(), " "); ops != "hello list agenda bye" {
		t.Errorf("ops %s", ops)
	}
	// Seen on the local network lately: not dialed.
	h.s.mu.Lock()
	h.s.seen[p.fpHex()[:16]] = h.clk.now()
	h.s.mu.Unlock()
	h.s.pins.update(p.fpHex(), func(d *Device) { d.Addresses = []string{"127.0.0.1"} })
	p.ops = nil
	h.s.dialKnown(context.Background())
	h.s.wg.Wait()
	if len(p.opList()) != 0 {
		t.Errorf("dialed though seen: %v", p.opList())
	}
}

// pipeConn is a frameConn over an in-memory pipe, with the far end.
func pipeConn(t *testing.T) (*frameConn, net.Conn) {
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return newFrameConn(a), b
}

func TestFrameBounds(t *testing.T) {
	frame := func(n uint32, b []byte) []byte {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], n)
		return append(hdr[:], b...)
	}
	msg := func(b []byte) []byte { return frame(uint32(len(b)), b) }
	for name, c := range map[string]struct {
		in     []byte
		bodyOK bool
		proto  bool
	}{
		"oversize":       {frame(maxJSON+1, nil), false, true},
		"tiny":           {frame(1, []byte("{")), false, true},
		"malformed":      {msg([]byte(`{"ok"`)), false, true},
		"not an object":  {msg([]byte(`[]`)), false, true},
		"no ok":          {msg([]byte(`{}`)), false, true},
		"ok a string":    {msg([]byte(`{"ok":"ye"}`)), false, true},
		"body too large": {msg([]byte(`{"ok":true,"body":67108865}  `)), true, true},
		"negative body":  {msg([]byte(`{"ok":true,"body":-1}`)), true, true},
		"unasked body":   {msg([]byte(`{"ok":true,"body":3}`)), false, true},
		"body on error":  {msg([]byte(`{"ok":false,"error":"x","body":3}`)), true, true},
		"not UTF-8":      {msg([]byte("{\"ok\":true,\"\xff\"}")), false, true},
		"phone error":    {msg([]byte(`{"ok":false,"error":"Bad Code!!"}`)), false, false},
	} {
		fc, far := pipeConn(t)
		go far.Write(c.in)
		_, err := fc.recv(time.Second, nil, c.bodyOK)
		if c.proto != errors.Is(err, errProtocol) {
			t.Errorf("%s: %v", name, err)
		}
		var pe *phoneError
		if name == "phone error" && (!errors.As(err, &pe) || pe.code != "unknown") {
			t.Errorf("phone error: %v", err)
		}
	}
	// A body is read exactly, and nothing more is read before it is.
	fc, far := pipeConn(t)
	go far.Write(append(msg([]byte(`{"ok":true,"body":3}`)), []byte("abc")...))
	if n, err := fc.recv(time.Second, nil, true); err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := fc.readFrame(time.Second); err == nil {
		t.Error("read past an unread body")
	}
	var buf bytes.Buffer
	if err := fc.readBody(&buf); err != nil || buf.String() != "abc" {
		t.Errorf("body %q %v", buf.String(), err)
	}
	// A frame cut short times out rather than waiting for ever.
	fc, far = pipeConn(t)
	go far.Write(msg([]byte(`{"ok"`)))
	start := time.Now()
	if _, err := fc.recv(200*time.Millisecond, nil, false); err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("cut short: %v", err)
	}
	// The Mac never sends more than the protocol allows.
	fc, _ = pipeConn(t)
	if err := fc.send(map[string]string{"op": strings.Repeat("x", maxJSON)}, nil, 0); err == nil {
		t.Error("oversize request sent")
	}
}

func TestSyncProtocolViolation(t *testing.T) {
	// A phone whose hello isn't v2 is dropped, and the outcome says so.
	h := newHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cert, fp := selfSigned(t, "odd phone")
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAnyClientCert})
		defer tc.Close()
		if _, _, err := readMsg(tc); err != nil {
			return
		}
		writeMsg(tc, map[string]any{"ok": true, "v": 1}, nil)
		readMsg(tc)
	}()
	h.s.pins.pin(Device{ID: hex.EncodeToString(fp), Name: "Odd"})
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	h.s.session(context.Background(), target{fp16: hex.EncodeToString(fp)[:16], pin: hex.EncodeToString(fp), addrs: []netip.AddrPort{netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port)}})
	if d := h.s.Devices()[0]; d.LastOutcome != "failed: the phone's answer didn't fit the protocol" {
		t.Errorf("outcome %q", d.LastOutcome)
	}
}

func TestUnpair(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	h.pairWith(p)
	if err := h.s.Unpair(p.fpHex()); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Unpair(p.fpHex()); err == nil {
		t.Error("unpaired twice")
	}
	b, _ := os.ReadFile(filepath.Join(h.dir, "phone", pinsFile))
	if strings.Contains(string(b), p.fpHex()) {
		t.Error("pin still saved")
	}
	// The phone still trusts the Mac, but the Mac no longer dials it as
	// paired: the advertisement alone is ignored.
	h.s.consider(context.Background(), sighting{host: "phone.local.", port: p.port, txt: map[string]string{"v": "2", "fp": p.fpHex()[:16]}}, false)
	h.s.wg.Wait()
	if len(p.opList()) != 0 {
		t.Error("dialed after unpairing")
	}
}
