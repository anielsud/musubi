package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
)

func TestFirstRunStandsAloneWithoutTomoe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "musubi", "config.toml")
	wrote, err := firstRun(path, filepath.Join(dir, "no-tomoe"), time.Now())
	if err != nil || !wrote {
		t.Fatalf("wrote %v, err %v", wrote, err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.MusubiRecords() || !c.TomoeOff() || c.Recorder.Since.IsZero() {
		t.Errorf("recorder = %+v, want Musubi's of record, Tomoe off, since set", c.Recorder)
	}
	// An existing config is never touched.
	if wrote, _ := firstRun(path, filepath.Join(dir, "no-tomoe"), time.Now()); wrote {
		t.Error("rewrote an existing config")
	}
}

func TestFirstRunKeepsDefaultsWithTomoe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if wrote, err := firstRun(path, dir, time.Now()); err != nil || wrote {
		t.Fatalf("wrote %v, err %v", wrote, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a config was written though Tomoe is installed")
	}
}

func TestWriteRecorderAgent(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "agent.template")
	if err := os.WriteFile(tmpl, []byte("<string>__EXECUTABLE__</string><string>__DATA__</string>"), 0o600); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(dir, "LaunchAgents", "x.plist")
	data := filepath.Join(dir, "rec")
	if err := writeRecorderAgent(plist, "/Applications/Musubi.app/rec", tmpl, data); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(plist)
	if want := "<string>/Applications/Musubi.app/rec</string><string>" + data + "</string>"; string(b) != want {
		t.Errorf("plist = %s, want %s", b, want)
	}
	if _, err := os.Stat(filepath.Join(data, "logs")); err != nil {
		t.Error("no logs folder")
	}
	if err := writeRecorderAgent(plist, "/x", tmpl, filepath.Join(dir, "a&b")); err == nil || !strings.Contains(err.Error(), "&") {
		t.Errorf("a path with & was accepted: %v", err)
	}
}
