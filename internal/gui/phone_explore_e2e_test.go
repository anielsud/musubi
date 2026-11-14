//go:build musubi_e2e

package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/phone"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// TestPhoneExploreEndToEnd opens Explore on the Android app on an
// EMULATOR against the real Mac code (docs/phone.md, part 3): phone sync
// with this app's allowlist and its built frontend, over a temporary
// dataset of invented meetings. It pairs, lets the Mac dial the phone for
// Explore, opens the Explore tab, runs a search; then a finished recording
// appears on the phone while Explore is open, and the phone ends Explore
// for a sync, the Mac syncs it and opens Explore again (the page keeps its
// search, and searches again on the new connection); then an Ask (no model
// is configured, so the answer is its sources only), screenshots, and
// leaving the app ends Explore.
//
//	make frontend
//	(cd android && ./gradlew assembleDebug) && adb -s emulator-5662 install -r android/app/build/outputs/apk/debug/app-debug.apk
//	MUSUBI_PHONE_E2E=1 MUSUBI_E2E_SERIAL=emulator-5662 go test -tags sqlite_fts5,musubi_e2e -run TestPhoneExploreEndToEnd -v -timeout 15m ./internal/gui/
//
// Screenshots go to MUSUBI_E2E_SHOTS (default: the test's temp folder).
// The test clears the app's data and refuses any device that is not an
// emulator.
func TestPhoneExploreEndToEnd(t *testing.T) {
	if os.Getenv("MUSUBI_PHONE_E2E") != "1" {
		t.Skip("MUSUBI_PHONE_E2E=1 runs it against an emulator")
	}
	serial := os.Getenv("MUSUBI_E2E_SERIAL")
	if serial == "" || !strings.HasPrefix(serial, "emulator-") {
		t.Fatal("MUSUBI_E2E_SERIAL names the emulator (emulator-NNNN)")
	}
	sdk := os.Getenv("ANDROID_HOME")
	if sdk == "" {
		sdk = "/opt/homebrew/share/android-commandlinetools"
	}
	d := &droid{t: t, serial: serial, adb: filepath.Join(sdk, "platform-tools", "adb")}
	if d.shell("getprop", "ro.kernel.qemu") != "1" && d.shell("getprop", "ro.boot.qemu") != "1" {
		t.Fatalf("refusing: %s is not an emulator", serial)
	}
	shots := os.Getenv("MUSUBI_E2E_SHOTS")
	if shots == "" {
		shots = t.TempDir()
	}
	dist := filepath.Join("..", "..", "cmd", "musubi-gui", "frontend", "dist")
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("no built frontend: run make frontend first")
	}

	a := exploreDataset(t)
	a.SetFrontend(os.DirFS(dist))
	var logMu sync.Mutex
	var logs []string
	live, assets := a.phoneLiveFor(true)
	s, err := phone.New(phone.Config{Dir: filepath.Join(t.TempDir(), "phone"), RecorderData: t.TempDir(), MacName: "Test Mac",
		Live: live, Assets: assets,
		Agenda: func(context.Context, int) ([]phone.Event, error) { return []phone.Event{}, nil },
		Logf: func(f string, args ...any) {
			logMu.Lock()
			logs = append(logs, fmt.Sprintf(f, args...))
			logMu.Unlock()
			t.Logf(f, args...)
		}})
	if err != nil {
		t.Fatal(err)
	}
	logged := func() string { logMu.Lock(); defer logMu.Unlock(); return strings.Join(logs, "\n") }

	port := func() int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}()
	d.adbRun("forward", fmt.Sprintf("tcp:%d", port), "tcp:47616")
	t.Cleanup(func() { exec.Command(d.adb, "-s", serial, "forward", "--remove", fmt.Sprintf("tcp:%d", port)).Run() })
	addr := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))

	t.Log("setup: clear the app, open it")
	d.shell("pm", "clear", "com.musubi.phone")
	d.shell("pm", "grant", "com.musubi.phone", "android.permission.POST_NOTIFICATIONS")
	d.shell("am", "start", "-n", "com.musubi.phone/.MainActivity")
	time.Sleep(3 * time.Second)

	t.Log("pair")
	d.tap("Mac")
	d.tap("Pair with your Mac")
	d.waitText(`Waiting for your Mac`, 15*time.Second)
	var fp16 string
	for try := 0; try < 20 && fp16 == ""; try++ {
		if fp16, err = s.E2EFingerprint(addr); err != nil {
			time.Sleep(time.Second)
		}
	}
	if fp16 == "" {
		t.Fatalf("the phone never answered: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !s.E2EDial(ctx, fp16, addr, true, false) {
		t.Fatal("pairing not started")
	}
	var pr *phone.PairPrompt
	waitUntil(t, "the Mac's pairing prompt", 30*time.Second, func() bool { pr = s.PairPrompt(); return pr != nil })
	m := d.waitText(`\b(\d{3}) (\d{3})\b`, 15*time.Second)
	if m[1]+m[2] != pr.Code {
		t.Fatalf("codes differ: phone %s%s, Mac %s", m[1], m[2], pr.Code)
	}
	d.tap("Confirm")
	if err := s.AnswerPair(pr.ID, true); err != nil {
		t.Fatal(err)
	}
	s.E2EWait()
	if len(s.Devices()) != 1 {
		t.Fatalf("not paired: %s", logged())
	}
	t.Log("  ok: paired (codes matched)")

	t.Log("Explore: the Mac dials the phone in the foreground")
	if !s.E2EDial(ctx, fp16, addr, false, true) {
		t.Fatal("live dial not started")
	}
	waitUntil(t, "Explore open on the Mac", 30*time.Second, func() bool { return strings.Contains(logged(), "Explore open on") })
	d.tap("Explore")
	d.waitText(`(?i)\bTopics\b`, 60*time.Second)
	d.screenshot(filepath.Join(shots, "1-explore.png"))
	t.Log("  ok: the Explore tab shows Musubi's Explore screen from the Mac")

	t.Log("search")
	d.tap("Search")
	d.tapField()
	d.shell("input", "text", "pricing")
	d.shell("input", "keyevent", "66")
	d.waitText(`Invented pricing review`, 30*time.Second)
	d.screenshot(filepath.Join(shots, "2-search.png"))
	t.Log("  ok: search found the invented meeting")

	t.Log("a recording finishes while Explore is open: it syncs, and Explore comes back")
	rid := seedRecording(t, d)
	seeded := time.Now()
	waitUntil(t, "Explore ended for a sync", 20*time.Second, func() bool { return strings.Contains(logged(), "ended (the phone has something to sync)") })
	d.screenshot(filepath.Join(shots, "2b-syncing.png"))
	waitUntil(t, "the recording synced", 20*time.Second, func() bool { return strings.Contains(logged(), "recording "+rid+": ") })
	waitUntil(t, "Explore open again", 20*time.Second, func() bool { return strings.Count(logged(), "Explore open on") >= 2 })
	t.Logf("  ok: synced and Explore open again %.1f s after the recording appeared", time.Since(seeded).Seconds())
	if took := time.Since(seeded); took > 15*time.Second {
		t.Errorf("took %s", took)
	}
	d.waitText(`Invented pricing review`, 10*time.Second) // the page kept its search
	d.tapField()
	d.shell("input", "keyevent", "123") // end of the field
	d.shell("input", "text", "%stiers")
	d.shell("input", "keyevent", "66")
	d.waitText(`Invented pricing review`, 20*time.Second)
	d.screenshot(filepath.Join(shots, "2c-after-sync.png"))
	t.Log("  ok: the page kept its search and searches on the new connection")

	t.Log("open the passage")
	d.tapWhere("the pricing passage", func(n droidNode) bool { return strings.Contains(n.text, "Invented pricing review") })
	d.waitText(`passages visible`, 20*time.Second)
	d.screenshot(filepath.Join(shots, "3-source.png"))
	d.shell("input", "keyevent", "111") // Esc closes the drawer
	time.Sleep(time.Second)
	if d.hasText("passages visible") {
		d.tap("✕")
	}

	t.Log("ask")
	d.tap("ask")
	d.tapField()
	d.shell("input", "text", "What%sdid%swe%sdecide%son%spricing?")
	d.shell("input", "keyevent", "66")
	d.waitText(`(?i)\d+ sources|cited|Nothing in your knowledge base|no model`, 60*time.Second)
	d.screenshot(filepath.Join(shots, "4-ask.png"))
	t.Log("  ok: Ask answered (no model configured: its sources only)")

	t.Log("leaving the app ends Explore")
	d.shell("input", "keyevent", "3") // Home
	waitUntil(t, "Explore ended on the Mac", 30*time.Second, func() bool { return strings.Contains(logged(), "ended (the phone left)") })
	cancel()
	s.E2EWait()
	if l := logged(); strings.Contains(l, "pricing") || strings.Contains(l, "Invented") {
		t.Errorf("meeting text or a query reached the log:\n%s", l)
	}
	t.Logf("screenshots in %s", shots)
}

// seedRecording puts a finished recording (a few seconds of silent ADTS
// frames) into the app's storage, as the recorder leaves one, and returns
// its id.
func seedRecording(t *testing.T, d *droid) string {
	t.Helper()
	var chunk []byte
	for i := 0; i < 200; i++ {
		l := 7 + 10
		chunk = append(chunk, 0xFF, 0xF1, 0x4C, byte(0x40|l>>11), byte(l>>3), byte((l&7)<<5|0x1F), 0xFC)
		chunk = append(chunk, make([]byte, 10)...)
	}
	sum := sha256.Sum256(chunk)
	rid := "6d1f0c3a-2b4e-4c8f-9a7d-0e5b3c2a1f40"
	start := time.Now().Add(-10 * time.Minute)
	meta := fmt.Sprintf(`{"version":1,"id":%q,"start":%q,"end":%q,"device_name":"Test Phone","event":null,"chunks":[{"n":0,"name":"chunk-0000.aac","size":%d,"sha256":%q}]}`,
		rid, start.Format("2006-01-02T15:04:05.000-07:00"), start.Add(5*time.Minute).Format("2006-01-02T15:04:05.000-07:00"), len(chunk), hex.EncodeToString(sum[:]))
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600)
	os.WriteFile(filepath.Join(dir, "chunk-0000.aac"), chunk, 0o600)
	tmp := "/data/local/tmp/musubi-e2e-" + rid[:8]
	d.shell("mkdir", "-p", tmp)
	for _, f := range []string{"chunk-0000.aac", "meta.json"} {
		d.adbRun("push", filepath.Join(dir, f), tmp+"/"+f)
	}
	d.shell("chmod", "-R", "a+rX", tmp)
	// The chunk first, meta.json last: the app never sees a meta without its audio.
	d.shell("run-as", "com.musubi.phone", "sh", "-c",
		fmt.Sprintf("'mkdir -p files/recordings/%[1]s.tmp && cp %[2]s/chunk-0000.aac %[2]s/meta.json files/recordings/%[1]s.tmp/ && mv files/recordings/%[1]s.tmp files/recordings/%[1]s'", rid, tmp))
	d.shell("rm", "-rf", tmp)
	if !strings.Contains(d.shell("run-as", "com.musubi.phone", "ls", "files/recordings/"+rid), "meta.json") {
		t.Fatal("the recording wasn't seeded")
	}
	return rid
}

// exploreDataset is an app over a temporary store of invented meetings.
func exploreDataset(t *testing.T) *App {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Owner = config.Owner{Name: "Sam Owner", Email: "sam@corp.example", InternalDomains: []string{"corp.example"}}
	at := time.Now().Add(-48 * time.Hour)
	for i, m := range []struct{ title, a, b string }{
		{"Invented pricing review", "We agreed the pricing tiers stay as they are until March.", "The discount for the pilot stays at ten percent."},
		{"Invented hiring sync", "Two open roles for the platform team.", "Interviews continue next week."},
		{"Invented roadmap planning", "The roadmap puts search before the mobile work.", "Pricing comes up again in the spring."},
	} {
		it := item.Item{ID: fmt.Sprintf("tomoe:e2e-%d", i), Source: "tomoe", Ref: fmt.Sprintf("e2e-%d", i), Kind: item.KindMeeting, Title: m.title,
			OccurredAt: at.Add(time.Duration(i) * time.Hour),
			Segments:   []item.Segment{{Speaker: "Robin Vale", Start: 5, Text: m.a}, {Speaker: "Sam Owner", Start: 65, Text: m.b}}}
		if _, err := st.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceChunks(it.ID, "v", "h", []store.Chunk{
			{Seq: 0, SegFrom: 0, SegTo: 1, Start: 5, Text: "Robin Vale: " + m.a},
			{Seq: 1, SegFrom: 1, SegTo: 2, Start: 65, Text: "Sam Owner: " + m.b},
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceLabels(it.ID, "v", "h", sensitivity.Label{Level: sensitivity.Internal}, nil, []string{"sam@corp.example"}); err != nil {
			t.Fatal(err)
		}
	}
	return &App{ctx: context.Background(), eng: &engine.Engine{Cfg: cfg, Store: st}, emitFn: func(string, any) {}}
}

func waitUntil(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// droid drives the emulator through adb and uiautomator.
type droid struct {
	t      *testing.T
	serial string
	adb    string
}

func (d *droid) adbRun(args ...string) {
	d.t.Helper()
	if out, err := exec.Command(d.adb, append([]string{"-s", d.serial}, args...)...).CombinedOutput(); err != nil {
		d.t.Fatalf("adb %v: %v %s", args, err, out)
	}
}

func (d *droid) shell(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, d.adb, append([]string{"-s", d.serial, "shell"}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

type droidNode struct {
	text, desc, hint, class string
	x, y                    int
}

var (
	droidNodeRe   = regexp.MustCompile(`<node [^>]*>`)
	droidAttrRe   = regexp.MustCompile(` (text|content-desc|hint|class|bounds)="([^"]*)"`)
	droidBoundsRe = regexp.MustCompile(`\d+`)
)

func (d *droid) nodes() []droidNode {
	d.shell("rm", "-f", "/sdcard/ui.xml")
	d.shell("uiautomator", "dump", "/sdcard/ui.xml")
	xml, err := exec.Command(d.adb, "-s", d.serial, "exec-out", "cat", "/sdcard/ui.xml").Output()
	if err != nil {
		return nil
	}
	var out []droidNode
	for _, n := range droidNodeRe.FindAllString(string(xml), -1) {
		var u droidNode
		for _, a := range droidAttrRe.FindAllStringSubmatch(n, -1) {
			switch a[1] {
			case "text":
				u.text = a[2]
			case "content-desc":
				u.desc = a[2]
			case "hint":
				u.hint = a[2]
			case "class":
				u.class = a[2]
			case "bounds":
				if b := droidBoundsRe.FindAllString(a[2], -1); len(b) == 4 {
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

func (d *droid) screenText() string {
	var s []string
	for _, n := range d.nodes() {
		if n.text != "" {
			s = append(s, n.text)
		} else if n.desc != "" {
			s = append(s, n.desc)
		}
	}
	return strings.Join(s, "\n")
}

func (d *droid) hasText(label string) bool {
	for _, n := range d.nodes() {
		if strings.EqualFold(n.text, label) || strings.EqualFold(n.desc, label) {
			return true
		}
	}
	return false
}

func (d *droid) tapWhere(what string, match func(droidNode) bool) {
	d.t.Helper()
	end := time.Now().Add(15 * time.Second)
	for time.Now().Before(end) {
		for _, n := range d.nodes() {
			if match(n) {
				d.shell("input", "tap", strconv.Itoa(n.x), strconv.Itoa(n.y))
				time.Sleep(800 * time.Millisecond)
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	d.t.Fatalf("no %s on screen; it shows:\n%s", what, d.screenText())
}

func (d *droid) tap(label string) {
	d.t.Helper()
	d.tapWhere(fmt.Sprintf("%q", label), func(n droidNode) bool {
		return strings.EqualFold(n.text, label) || strings.EqualFold(n.desc, label)
	})
}

// tapField taps the page's (only) text field.
func (d *droid) tapField() {
	d.t.Helper()
	d.tapWhere("a text field", func(n droidNode) bool { return n.class == "android.widget.EditText" })
}

func (d *droid) waitText(pattern string, timeout time.Duration) []string {
	d.t.Helper()
	re := regexp.MustCompile(pattern)
	end := time.Now().Add(timeout)
	var s string
	for time.Now().Before(end) {
		s = d.screenText()
		if m := re.FindStringSubmatch(s); m != nil {
			return m
		}
		time.Sleep(700 * time.Millisecond)
	}
	d.t.Fatalf("the screen never showed %q; it shows:\n%s", pattern, s)
	return nil
}

func (d *droid) screenshot(path string) {
	d.t.Helper()
	png, err := exec.Command(d.adb, "-s", d.serial, "exec-out", "screencap", "-p").Output()
	if err != nil || len(png) == 0 {
		d.t.Errorf("screenshot: %v", err)
		return
	}
	if err := os.WriteFile(path, png, 0o600); err != nil {
		d.t.Error(err)
	}
}
