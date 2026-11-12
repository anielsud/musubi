package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Without a [recorder] table, Musubi reads Tomoe's folder and socket
// exactly as before.
func TestRecorderDefaultsKeepTomoe(t *testing.T) {
	c := DefaultConfig()
	if c.Recorder.OfRecord != "tomoe" || c.MusubiRecords() {
		t.Fatalf("of_record = %q", c.Recorder.OfRecord)
	}
	if c.SessionsDir() != c.TomoeSessions || c.LiveSocket() != c.Live.TomoeSocket {
		t.Errorf("sessions %q socket %q, want Tomoe's", c.SessionsDir(), c.LiveSocket())
	}
	if !c.SessionsSince().IsZero() {
		t.Errorf("since = %v, want none", c.SessionsSince())
	}
	if d := c.SessionDirs(); len(d) != 1 || d[0] != c.TomoeSessions {
		t.Errorf("session dirs = %v", d)
	}
	if filepath.Base(c.Recorder.Data) != "recorder" || filepath.Base(c.RecorderControlSocket()) != "control.sock" {
		t.Errorf("recorder data %q control %q", c.Recorder.Data, c.RecorderControlSocket())
	}
}

func TestRecorderOfRecordSwitchesPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `tomoe_sessions = "` + dir + `/tomoe"
[recorder]
of_record = "musubi"
data = "` + dir + `/rec"
since = 2026-11-11T09:30:00Z
[recorder.meeting]
mic_voices = false
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.MusubiRecords() {
		t.Fatal("not of record")
	}
	if got, want := c.SessionsDir(), filepath.Join(dir, "rec", "sessions"); got != want {
		t.Errorf("sessions = %q, want %q", got, want)
	}
	if got, want := c.LiveSocket(), filepath.Join(dir, "rec", "live.sock"); got != want {
		t.Errorf("socket = %q, want %q", got, want)
	}
	if want := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC); !c.SessionsSince().Equal(want) {
		t.Errorf("since = %v", c.SessionsSince())
	}
	if v, ok := c.Recorder.Meeting["mic_voices"].(bool); !ok || v {
		t.Errorf("meeting override = %#v", c.Recorder.Meeting)
	}
	// A meeting Tomoe recorded before the switch keeps its files in
	// Tomoe's folder; a new one is in the recorder's.
	for _, s := range []string{"tomoe/aaaa", "rec/sessions/bbbb"} {
		os.MkdirAll(filepath.Join(dir, s), 0o700)
		os.WriteFile(filepath.Join(dir, s, "session.json"), []byte("{}"), 0o600)
	}
	if got := c.SessionDir("aaaa"); got != filepath.Join(dir, "tomoe", "aaaa") {
		t.Errorf("old session in %q", got)
	}
	if got := c.SessionDir("bbbb"); got != filepath.Join(dir, "rec", "sessions", "bbbb") {
		t.Errorf("new session in %q", got)
	}
	if got := c.SessionDir("cccc"); got != "" {
		t.Errorf("missing session in %q", got)
	}
}

// The settings writer can set since, and what it writes loads back.
func TestEditWritesSince(t *testing.T) {
	at := time.Date(2026, 11, 11, 14, 5, 0, 0, time.FixedZone("", -5*3600))
	out, err := EditText("[recorder]\nof_record = \"tomoe\"\n", []Edit{
		{Section: "recorder", Key: "of_record", Value: "musubi"},
		{Section: "recorder", Key: "since", Value: at},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte(out), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !c.MusubiRecords() || !c.Recorder.Since.Equal(at) {
		t.Errorf("loaded %q %v from\n%s", c.Recorder.OfRecord, c.Recorder.Since, out)
	}
}
