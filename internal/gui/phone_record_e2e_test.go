//go:build musubi_e2e

package gui

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/phone"
	"github.com/anielsud/musubi/internal/phonedetails"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// TestPhoneRecordWhileExplore records with the app's own Record button
// while the app stays open with Explore live, adds a note, a person and a
// camera photo during the recording, and stops: the recording syncs while
// the app is open, Explore comes back, and the details reach the session
// folder. The Recording screen never goes idle, so uiautomator can't read
// it: its controls are tapped where they are on a 1080x2400 screen
// (a Pixel 6 AVD), and screenshots record what it showed.
//
//	MUSUBI_PHONE_E2E=1 MUSUBI_E2E_SERIAL=emulator-5640 go test -tags sqlite_fts5,musubi_e2e -run TestPhoneRecordWhileExplore -v -timeout 15m ./internal/gui/
func TestPhoneRecordWhileExplore(t *testing.T) {
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
	if size := d.shell("wm", "size"); !strings.Contains(size, "1080x2400") {
		t.Fatalf("taps are placed for a 1080x2400 screen; this one is %s", size)
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
	logged := func() string { logMu.Lock(); defer logMu.Unlock(); return strings.Join(logs, "\n") }
	recData := t.TempDir()
	// The recorder imports at once, into a session folder named after the
	// recording, as the real one does when no meeting is live.
	importer := func(_ context.Context, args control.ImportArgs) (control.ImportStatus, error) {
		// Like the recorder: only audio in its inbox becomes a session.
		if fi, err := os.Lstat(args.Path); err != nil || fi.Size() == 0 {
			return control.ImportStatus{}, fmt.Errorf("no audio to import")
		}
		if err := os.MkdirAll(filepath.Join(recData, "sessions", args.ID), 0o700); err != nil {
			return control.ImportStatus{}, err
		}
		return control.ImportStatus{Session: args.ID, State: control.ImportImported}, nil
	}
	live, assets := a.phoneLiveFor(true)
	s, err := phone.New(phone.Config{Dir: filepath.Join(t.TempDir(), "phone"), RecorderData: recData, MacName: "Test Mac",
		Live: live, Assets: assets, Details: true, Import: importer,
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

	t.Log("setup: clear the app, grant, open, pair")
	d.shell("pm", "clear", "com.musubi.phone")
	for _, p := range []string{"android.permission.RECORD_AUDIO", "android.permission.POST_NOTIFICATIONS"} {
		d.shell("pm", "grant", "com.musubi.phone", p)
	}
	d.shell("am", "start", "-n", "com.musubi.phone/.MainActivity")
	time.Sleep(3 * time.Second)
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

	t.Log("Explore live")
	if !s.E2EDial(ctx, fp16, addr, false, true) {
		t.Fatal("live dial not started")
	}
	waitUntil(t, "Explore open on the Mac", 30*time.Second, func() bool { return strings.Contains(logged(), "Explore open on") })
	d.tap("Explore")
	d.waitText(`(?i)\bTopics\b`, 60*time.Second)
	t.Log("  ok: Explore open")

	t.Log("record with the app's Record button; a note, a person and a camera photo during the recording")
	d.tap("Today")
	d.tap("Record (ad hoc)")
	time.Sleep(4 * time.Second)
	at := func(x, y int) { d.shell("input", "tap", strconv.Itoa(x), strconv.Itoa(y)); time.Sleep(900 * time.Millisecond) }
	// The bottom of the screen is a stable place: the positions below are
	// measured there, with no photo (notes, Take photo) and with one (Add
	// person).
	bottom := func() {
		for i := 0; i < 3; i++ {
			d.shell("input", "swipe", "540", "1800", "540", "300", "200")
		}
		time.Sleep(time.Second)
	}
	bottom()
	d.screenshot(filepath.Join(shots, "r1-recording.png"))
	at(540, 1644) // Notes
	d.shell("input", "text", "Invented%snote%staken%swhile%srecording")
	d.shell("input", "keyevent", "BACK") // the keyboard
	time.Sleep(time.Second)
	bottom()
	at(218, 2210) // Take photo
	d.tap("Shutter")
	d.tap("Done")
	time.Sleep(3 * time.Second)
	// The camera took the app out of the foreground, which ends Explore;
	// back in front, the phone advertises live=1 and the Mac dials it
	// again (here by hand: the emulator's advertisement isn't seen).
	waitUntil(t, "Explore ended when the camera opened", 30*time.Second, func() bool { return strings.Contains(logged(), "ended (the phone left)") })
	if !s.E2EDial(ctx, fp16, addr, false, true) {
		t.Fatal("live dial not started")
	}
	waitUntil(t, "Explore open again after the camera", 30*time.Second, func() bool { return strings.Count(logged(), "Explore open on") >= 2 })
	bottom()
	d.screenshot(filepath.Join(shots, "r2-photo.png"))
	at(540, 1755) // Add person
	at(540, 1219) // Type a name…
	at(540, 1198) // Name
	d.shell("input", "text", "Test%sAttendee")
	at(842, 999) // Add (the keyboard is up)
	time.Sleep(time.Second)
	d.screenshot(filepath.Join(shots, "r3-details.png"))
	rid := d.shell("run-as", "com.musubi.phone", "ls", "files/recordings")
	if len(rid) != 36 {
		t.Fatalf("recordings on the phone: %q", rid)
	}
	meta := d.shell("run-as", "com.musubi.phone", "cat", "files/recordings/"+rid+"/meta.json")
	if !strings.Contains(meta, `"end": null`) || !strings.Contains(meta, "photo-0001.jpg") || !strings.Contains(meta, "Test Attendee") {
		t.Fatalf("details during the recording: %s", meta)
	}
	t.Log("  ok: details saved while recording")

	t.Log("stop: the recording syncs with the app open, Explore comes back")
	d.shell("input", "swipe", "540", "500", "540", "2000", "300") // back to the top
	time.Sleep(time.Second)
	at(540, 1176) // Stop
	stopped := time.Now()
	d.tap("Explore")
	for i := 0; i < 20; i++ {
		d.screenshot(filepath.Join(shots, fmt.Sprintf("r4-after-stop-%02d.png", i)))
		if strings.Count(logged(), "Explore open on") >= 3 {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	waitUntil(t, "Explore ended for the sync", 60*time.Second, func() bool { return strings.Contains(logged(), "ended (the phone has something to sync)") })
	waitUntil(t, "the recording imported", 60*time.Second, func() bool { return strings.Contains(logged(), "recording "+rid+" imported as session") })
	waitUntil(t, "the details applied", 60*time.Second, func() bool { return strings.Contains(logged(), "recording "+rid+": details rev") })
	waitUntil(t, "Explore open again", 60*time.Second, func() bool { return strings.Count(logged(), "Explore open on") >= 3 })
	t.Logf("  ok: synced and Explore open again %.1f s after Stop", time.Since(stopped).Seconds())
	d.waitText(`(?i)\bTopics\b`, 30*time.Second)
	d.screenshot(filepath.Join(shots, "r5-explore-back.png"))

	f := phonedetails.Read(filepath.Join(recData, "sessions", rid))
	if f == nil || f.Notes.Text != "Invented note taken while recording" || len(f.Attendees) != 1 || f.Attendees[0].Name != "Test Attendee" || len(f.Photos) != 1 {
		t.Fatalf("details on the Mac: %+v", f)
	}
	if _, err := os.Stat(filepath.Join(recData, "sessions", rid, "photos", f.Photos[0].Name)); err != nil {
		t.Fatal(err)
	}
	t.Logf("  ok: rev %d on the Mac: the note, the person and %s (%d bytes)", f.Rev, f.Photos[0].Name, f.Photos[0].Size)
	d.shell("input", "keyevent", "3") // Home
	waitUntil(t, "Explore ended on the Mac", 30*time.Second, func() bool { return strings.Count(logged(), "ended (the phone left)") >= 2 })
	cancel()
	s.E2EWait()
	if l := logged(); strings.Contains(l, "Invented note") || strings.Contains(l, "Test Attendee") {
		t.Errorf("notes or names reached the log")
	}
	t.Logf("screenshots in %s", shots)
}
