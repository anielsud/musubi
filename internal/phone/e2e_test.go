package phone

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// TestPhoneEndToEnd runs the real syncer against the real Android app on
// an EMULATOR (docs/phone.md, "Sync: the Mac dials the phone"): pairing
// (cancelled, then confirmed on both, codes compared with the phone's
// screen), a short recording fetched, joined and imported, the agenda on
// Today, an interrupted sync resumed, the periodic listening window, a
// sync ending a window early, and the Mac's dial after the phone unpaired.
//
//	MUSUBI_PHONE_E2E=1 MUSUBI_E2E_SERIAL=emulator-5610 go test -run TestPhoneEndToEnd -v -timeout 45m ./internal/phone/
//
// It takes about 20 minutes: the periodic listening job can only be
// forced once it is due (15 minutes after the app first runs).
// The app must be a debug build with short chunks (./gradlew assembleDebug
// -PchunkSeconds=10) installed on the emulator. The test clears the app's
// data and refuses any device that is not an emulator.
func TestPhoneEndToEnd(t *testing.T) {
	if os.Getenv("MUSUBI_PHONE_E2E") != "1" {
		t.Skip("MUSUBI_PHONE_E2E=1 runs it against an emulator")
	}
	serial := os.Getenv("MUSUBI_E2E_SERIAL")
	if serial == "" {
		t.Fatal("MUSUBI_E2E_SERIAL names the emulator (adb devices)")
	}
	d := &emu{t: t, serial: serial, adb: filepath.Join(envOr("ANDROID_HOME", "/opt/homebrew/share/android-commandlinetools"), "platform-tools", "adb")}
	if d.shell("getprop", "ro.kernel.qemu") != "1" && d.shell("getprop", "ro.boot.qemu") != "1" {
		t.Fatalf("refusing: %s is not an emulator", serial)
	}

	h := newHarness(t)
	h.rec.set(true, control.ImportImported)
	start := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	h.s.cfg.Agenda = func(context.Context, int) ([]Event, error) {
		return []Event{{ID: "calendar:e2e-1", Start: start, End: start.Add(time.Hour), Subject: "Invented planning session",
			Location: "Room 1", Organizer: "Test Organizer", Attendees: []Attendee{{Name: "Test Person"}},
			Brief: &Brief{Text: "An invented brief.", MadeAt: time.Now(), Sensitivity: Sensitivity{Level: "internal", Share: "internal"}}}}, nil
	}

	port := freePort(t)
	d.adbRun("forward", fmt.Sprintf("tcp:%d", port), "tcp:47616")
	t.Cleanup(func() { exec.Command(d.adb, "-s", serial, "forward", "--remove", fmt.Sprintf("tcp:%d", port)).Run() })
	phoneAddr := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))

	t.Log("setup: clear the app, grant, open")
	d.shell("pm", "clear", "com.musubi.phone")
	for _, p := range []string{"android.permission.RECORD_AUDIO", "android.permission.POST_NOTIFICATIONS"} {
		d.shell("pm", "grant", "com.musubi.phone", p)
	}
	d.shell("am", "start", "-n", "com.musubi.phone/.MainActivity")
	time.Sleep(3 * time.Second)

	t.Log("record a short meeting")
	d.record(25 * time.Second)

	// pair runs one pairing from the phone's "Pair with your Mac" to the
	// end, the phone's owner tapping confirm or not.
	pair := func(confirm bool) {
		t.Helper()
		d.tap("Mac")
		d.tap("Pair with your Mac")
		d.waitText(`Waiting for your Mac`, 15*time.Second)
		fp16 := probeFP(t, h.s, phoneAddr)
		if !h.s.start(context.Background(), target{fp16: fp16, pair: true, addrs: []netip.AddrPort{phoneAddr}}) {
			t.Fatal("pairing not started")
		}
		var pr *PairPrompt
		waitFor(t, "the Mac's prompt", func() bool { pr = h.s.PairPrompt(); return pr != nil })
		m := d.waitText(`\b(\d{3}) (\d{3})\b`, 15*time.Second)
		if shown := m[1] + m[2]; shown != pr.Code {
			t.Fatalf("the phone shows %s, the Mac %s", shown, pr.Code)
		}
		t.Logf("  ok: the Mac's prompt and the phone show the same code; device %q", pr.DeviceName)
		if confirm {
			d.tap("Confirm")
		} else {
			d.tap("Cancel")
		}
		if err := h.s.AnswerPair(pr.ID, true); err != nil {
			t.Fatal(err)
		}
		h.s.wg.Wait()
	}

	t.Log("(1) pairing cancelled on the phone")
	pair(false)
	if len(h.s.Devices()) != 0 || !strings.Contains(h.logged(), "not confirmed on the phone (cancelled)") {
		t.Fatalf("after a cancel: devices %v", h.s.Devices())
	}
	d.waitText(`Pairing cancelled`, 10*time.Second)
	t.Log("  ok: not paired; the Mac logged the phone's cancel")

	t.Log("(1) pairing confirmed on both, then (2) the recording synced and (3) the agenda")
	pair(true)
	devs := h.s.Devices()
	if len(devs) != 1 {
		t.Fatalf("devices %v", devs)
	}
	dev := devs[0]
	t.Logf("  ok: paired with %q (%s), outcome %q, addresses %v", dev.Name, dev.ID[:16], dev.LastOutcome, dev.Addresses)
	if !strings.HasPrefix(dev.LastOutcome, "synced: 1 recordings on the phone, 1 fetched, 1 imported; agenda of 1 events sent") {
		t.Fatalf("outcome %q", dev.LastOutcome)
	}
	rid1 := h.lastImport().ID
	checkImported(t, h, d, rid1)
	d.tap("Today")
	d.waitText(`Invented planning session`, 10*time.Second)
	t.Log("  ok: the agenda is on Today")
	d.tap("Mac")
	d.waitText(`Paired with Test Mac`, 10*time.Second)
	d.waitText(`Synced: \d+ parts sent, 1 transcribed, agenda updated`, 10*time.Second)
	t.Log("  ok: the phone's Mac screen shows the sync")

	t.Log("(5) an interrupted sync resumes")
	d.record(35 * time.Second)
	pinned := target{fp16: dev.ID[:16], pin: dev.ID, addrs: nil}
	cut := startProxy(t, port, 180<<10) // after hello, list and the first chunk, mid the second
	pinned.addrs = []netip.AddrPort{cut.addr}
	h.s.session(context.Background(), pinned)
	if o := h.s.Devices()[0].LastOutcome; o != "failed: the connection broke off" {
		t.Fatalf("interrupted: outcome %q", o)
	}
	rid2 := ""
	ents, _ := os.ReadDir(filepath.Join(h.s.cfg.Dir, "recordings"))
	for _, e := range ents {
		if e.Name() != rid1 {
			rid2 = e.Name()
		}
	}
	staged := stagedChunks(t, h, rid2)
	if len(staged) == 0 {
		t.Fatalf("nothing staged before the cut (%d bytes passed)", cut.passed.Load())
	}
	t.Logf("  ok: cut after %d bytes; %v staged, no temporary files", cut.passed.Load(), staged)
	var total int64
	for _, n := range staged {
		fi, _ := os.Stat(filepath.Join(h.s.recDir(rid2), n))
		total += fi.Size()
	}
	full := startProxy(t, port, 0)
	pinned.addrs = []netip.AddrPort{full.addr}
	var outcome string
	for try := 0; try < 5; try++ { // the phone may still be closing the cut connection
		h.s.session(context.Background(), pinned)
		if outcome = h.s.Devices()[0].LastOutcome; strings.HasPrefix(outcome, "synced") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !strings.HasPrefix(outcome, "synced: 1 recordings on the phone, 1 fetched, 1 imported") {
		t.Fatalf("resumed: outcome %q", outcome)
	}
	if h.lastImport().ID != rid2 {
		t.Fatalf("imported %s, want %s", h.lastImport().ID, rid2)
	}
	t.Logf("  ok: resumed: %d bytes from the phone this time; the %d bytes staged before were not fetched again", full.passed.Load(), total)
	checkImported(t, h, d, rid2)

	t.Log("periodic listening window, app in the background")
	d.shell("input", "keyevent", "HOME")
	time.Sleep(3 * time.Second)
	if listening(t, port) || d.advertised() {
		t.Fatalf("still listening in the background after a sync and Home")
	}
	// WorkManager ignores a periodic job forced before it is due ("being
	// executed before schedule"), so wait until the scheduler's timing
	// constraint is met, then force it.
	job := d.dueJob(17 * time.Minute)
	runAt := time.Now()
	d.shell("cmd", "jobscheduler", "run", "-f", "com.musubi.phone", job)
	waitFor(t, "the periodic window", func() bool { return listening(t, port) && d.advertised() })
	t.Logf("  ok: listening and advertised %s after the job was forced", time.Since(runAt).Round(time.Second))
	var portOff, adOff time.Duration
	for time.Since(runAt) < 6*time.Minute && (portOff == 0 || adOff == 0) {
		time.Sleep(5 * time.Second)
		if portOff == 0 && !listening(t, port) {
			portOff = time.Since(runAt)
		}
		if adOff == 0 && !d.advertised() {
			adOff = time.Since(runAt)
		}
	}
	t.Logf("  periodic window: port closed after %s, advertisement withdrawn after %s", portOff.Round(time.Second), adOff.Round(time.Second))
	if portOff == 0 || adOff == 0 || portOff > 3*time.Minute+20*time.Second || adOff > 3*time.Minute+20*time.Second {
		t.Errorf("the periodic window didn't end within ~3 minutes (port %s, advertisement %s)", portOff, adOff)
	}

	t.Log("a sync ends a window early (after a recording, app in the background)")
	d.shell("am", "start", "-n", "com.musubi.phone/.MainActivity")
	d.record(15 * time.Second)
	d.shell("input", "keyevent", "HOME")
	time.Sleep(3 * time.Second)
	if !listening(t, port) || !d.advertised() {
		t.Fatal("not listening in the 8 minutes after a recording")
	}
	pinned.addrs = []netip.AddrPort{phoneAddr}
	h.s.session(context.Background(), pinned)
	if o := h.s.Devices()[0].LastOutcome; !strings.HasPrefix(o, "synced: 1 recordings on the phone, 1 fetched, 1 imported") {
		t.Fatalf("outcome %q", o)
	}
	synced := time.Now()
	waitFor(t, "the window's end after bye", func() bool { return !listening(t, port) && !d.advertised() })
	t.Logf("  ok: window ended %s after the sync's bye", time.Since(synced).Round(100*time.Millisecond))

	t.Log("(4) unpaired on the phone: the Mac's next dial")
	d.shell("am", "start", "-n", "com.musubi.phone/.MainActivity")
	d.tap("Mac")
	d.tap("Unpair")
	if d.hasText("Unpair") {
		d.tap("Unpair") // a confirmation, if the phone asks
	}
	d.waitText(`Pair with your Mac`, 10*time.Second)
	before := h.s.Devices()[0].LastOutcome
	h.s.unreachableLog = everyMinute{}
	h.s.session(context.Background(), pinned)
	after := h.s.Devices()[0]
	t.Logf("  phone not listening (unpaired, no pairing open): outcome %q; log: %s", after.LastOutcome, lastLines(h.logged(), 1))
	if after.LastOutcome != before || !strings.Contains(lastLines(h.logged(), 1), "not reachable") {
		t.Errorf("an unreachable phone: outcome %q", after.LastOutcome)
	}
	t.Log("  then while the phone has pairing open (it accepts any certificate, for pairing only)")
	d.tap("Pair with your Mac")
	d.waitText(`Waiting for your Mac`, 15*time.Second)
	h.s.session(context.Background(), pinned)
	after = h.s.Devices()[0]
	t.Logf("  outcome %q; log: %s", after.LastOutcome, lastLines(h.logged(), 1))
	if after.LastOutcome != refusedOutcome {
		t.Errorf("the Mac didn't say the phone refused it: %q", after.LastOutcome)
	}
	d.tap("Cancel")
}

func (h *harness) lastImport() control.ImportArgs {
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	if len(h.rec.calls) == 0 {
		h.t.Fatal("nothing imported")
	}
	return h.rec.calls[len(h.rec.calls)-1]
}

// checkImported checks the Mac joined rid's chunks into the inbox and that
// the phone, told "imported", deleted its audio and kept meta.json.
func checkImported(t *testing.T, h *harness, d *emu, rid string) {
	t.Helper()
	inbox := filepath.Join(h.dir, "recorder", "inbox", rid+".aac")
	f, err := os.Open(inbox)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := copyADTS(io.Discard, f)
	f.Close()
	if err != nil || frames < 100 {
		t.Fatalf("joined audio: %d frames, %v", frames, err)
	}
	m := regexp.MustCompile(`recording ` + rid + `: (\d+) chunks, (\d+) frames`).FindStringSubmatch(h.logged())
	if m == nil {
		t.Fatal("no join logged")
	}
	if n, _ := strconv.Atoi(m[1]); n < 2 {
		t.Errorf("only %s chunks: short chunks need -PchunkSeconds=10", m[1])
	}
	t.Logf("  ok: %s chunks fetched and checked against meta.json, joined into %d ADTS frames, imported", m[1], frames)
	if ents, _ := os.ReadDir(h.s.recDir(rid)); len(ents) != 1 {
		t.Errorf("staging not cleaned: %d files", len(ents))
	}
	var files string
	waitFor(t, "the phone dropping the audio", func() bool {
		files = d.shell("run-as", "com.musubi.phone", "ls", "files/recordings/"+rid)
		return !strings.Contains(files, "chunk-")
	})
	if !strings.Contains(files, "meta.json") {
		t.Errorf("phone files %q", files)
	}
	t.Log("  ok: the phone deleted the audio and kept meta.json")
}

func stagedChunks(t *testing.T, h *harness, rid string) []string {
	ents, err := os.ReadDir(h.s.recDir(rid))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temporary file %s left", e.Name())
		}
		if strings.HasPrefix(e.Name(), "chunk-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// probeFP is the phone's certificate fingerprint's first 16 hex digits,
// as its advertisement carries them (the emulator's NSD isn't visible on
// the Mac's network).
func probeFP(t *testing.T, s *Syncer, ap netip.AddrPort) string {
	t.Helper()
	var fp string
	for try := 0; try < 20; try++ {
		c, err := tls.Dial("tcp", ap.String(), s.tlsConfig(func(sum []byte) error { fp = hex.EncodeToString(sum); return nil }))
		if err == nil {
			c.Close()
			return fp[:16]
		}
		time.Sleep(time.Second)
	}
	t.Fatal("the phone never answered")
	return ""
}

// listening: adb accepts the forwarded connection at once and closes it
// when nothing listens on the phone; a TLS server instead waits for the
// ClientHello.
func listening(t *testing.T, port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	_, err = c.Read(make([]byte, 1))
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// proxy relays to the forwarded port, counting the phone's bytes and
// cutting the connection after cutAt of them (0: never).
type proxy struct {
	addr   netip.AddrPort
	passed atomic.Int64
}

func startProxy(t *testing.T, port int, cutAt int64) *proxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	p := &proxy{addr: ln.Addr().(*net.TCPAddr).AddrPort()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if err != nil {
					return
				}
				defer up.Close()
				go io.Copy(up, c)
				buf := make([]byte, 16<<10)
				for {
					n, err := up.Read(buf)
					if n > 0 {
						if cutAt > 0 && p.passed.Load()+int64(n) > cutAt {
							n = int(cutAt - p.passed.Load())
							c.Write(buf[:n])
							p.passed.Add(int64(n))
							return
						}
						c.Write(buf[:n])
						p.passed.Add(int64(n))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return p
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " | ")
}

// emu drives the emulator over adb.
type emu struct {
	t      *testing.T
	serial string
	adb    string
}

func (d *emu) adbRun(args ...string) string {
	d.t.Helper()
	out, err := exec.Command(d.adb, append([]string{"-s", d.serial}, args...)...).Output()
	if err != nil {
		d.t.Fatalf("adb %s: %v", args[0], err)
	}
	return string(out)
}

// shell runs an adb shell command, at most 30 seconds (uiautomator can
// wait long for a screen that never goes idle).
func (d *emu) shell(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, d.adb, append([]string{"-s", d.serial, "shell"}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

type uiNode struct {
	text, desc string
	x, y       int
}

var (
	nodeRe   = regexp.MustCompile(`<node [^>]*>`)
	attrRe   = regexp.MustCompile(` (text|content-desc|bounds)="([^"]*)"`)
	boundsRe = regexp.MustCompile(`\d+`)
)

func (d *emu) nodes() []uiNode {
	d.shell("rm", "-f", "/sdcard/ui.xml")
	d.shell("uiautomator", "dump", "/sdcard/ui.xml")
	xml, err := exec.Command(d.adb, "-s", d.serial, "exec-out", "cat", "/sdcard/ui.xml").Output()
	if err != nil {
		return nil
	}
	var out []uiNode
	for _, n := range nodeRe.FindAllString(string(xml), -1) {
		var u uiNode
		for _, a := range attrRe.FindAllStringSubmatch(n, -1) {
			switch a[1] {
			case "text":
				u.text = a[2]
			case "content-desc":
				u.desc = a[2]
			case "bounds":
				b := boundsRe.FindAllString(a[2], -1)
				if len(b) == 4 {
					x0, _ := strconv.Atoi(b[0])
					y0, _ := strconv.Atoi(b[1])
					x1, _ := strconv.Atoi(b[2])
					y1, _ := strconv.Atoi(b[3])
					u.x, u.y = (x0+x1)/2, (y0+y1)/2
				}
			}
		}
		out = append(out, u)
	}
	return out
}

func (d *emu) screenText() string {
	var s []string
	for _, n := range d.nodes() {
		if n.text != "" {
			s = append(s, n.text)
		}
	}
	return strings.Join(s, "\n")
}

func (d *emu) hasText(label string) bool {
	for _, n := range d.nodes() {
		if strings.EqualFold(n.text, label) || strings.EqualFold(n.desc, label) {
			return true
		}
	}
	return false
}

func (d *emu) tap(label string) {
	d.t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		for _, n := range d.nodes() {
			if strings.EqualFold(n.text, label) || strings.EqualFold(n.desc, label) {
				d.shell("input", "tap", strconv.Itoa(n.x), strconv.Itoa(n.y))
				time.Sleep(700 * time.Millisecond)
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	d.t.Fatalf("no %q on screen; it shows:\n%s", label, d.screenText())
}

func (d *emu) waitText(pattern string, timeout time.Duration) []string {
	d.t.Helper()
	re := regexp.MustCompile(pattern)
	end := time.Now().Add(timeout)
	var s string
	for time.Now().Before(end) {
		s = d.screenText()
		if m := re.FindStringSubmatch(s); m != nil {
			return m
		}
		time.Sleep(500 * time.Millisecond)
	}
	d.t.Fatalf("the screen never showed %q; it shows:\n%s", pattern, s)
	return nil
}

// record makes an ad hoc recording of about length. The Recording screen
// never goes idle, so uiautomator can't read it: Stop is tapped where it
// is on a phone-shaped screen.
func (d *emu) record(length time.Duration) {
	d.t.Helper()
	d.tap("Today")
	d.tap("Record (ad hoc)")
	time.Sleep(length)
	f := boundsRe.FindAllString(d.shell("wm", "size"), -1)
	w, _ := strconv.Atoi(f[0])
	hgt, _ := strconv.Atoi(f[1])
	d.shell("input", "tap", strconv.Itoa(w/2), strconv.Itoa(hgt*49/100))
	time.Sleep(3 * time.Second)
	d.waitText(`Record \(ad hoc\)`, 15*time.Second)
}

// advertised reports whether the app's NSD registration is up: the last
// register or unregister in NsdService's records (dumpsys's log lines
// keep old registrations, so they can't tell).
func (d *emu) advertised() bool {
	recs := regexp.MustCompile(`what=(REGISTER_SERVICE|UNREGISTER_SERVICE)`).FindAllStringSubmatch(d.shell("dumpsys", "servicediscovery"), -1)
	return len(recs) > 0 && recs[len(recs)-1][1] == "REGISTER_SERVICE"
}

// dueJob is the app's WorkManager job once its timing constraint is met
// (only the periodic listening job is scheduled), waiting at most wait.
func (d *emu) dueJob(wait time.Duration) string {
	d.t.Helper()
	head := regexp.MustCompile(`JOB #u\d+a\d+/(\d+): [0-9a-f]+ com\.musubi\.phone/androidx\.work\.impl\.background\.systemjob\.SystemJobService`)
	end := time.Now().Add(wait)
	for {
		out := d.shell("dumpsys", "jobscheduler", "com.musubi.phone")
		if loc := head.FindStringSubmatchIndex(out); loc != nil {
			block := out[loc[1]:]
			if i := strings.Index(block, "JOB #"); i >= 0 {
				block = block[:i]
			}
			unsat := ""
			if i := strings.Index(block, "Unsatisfied constraints:"); i >= 0 {
				unsat, _, _ = strings.Cut(block[i:], "\n")
			}
			if !strings.Contains(unsat, "TIMING_DELAY") {
				return out[loc[2]:loc[3]]
			}
		}
		if time.Now().After(end) {
			d.t.Fatal("the listening job never came due")
		}
		time.Sleep(10 * time.Second)
	}
}
