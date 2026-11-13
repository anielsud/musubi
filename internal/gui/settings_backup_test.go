package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/anielsud/musubi/internal/engine"
)

// A backup holds the database, the rest of the data folder, the
// recorder's folder and the config, and is listed for restoring.
func TestBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "raw", "ab"), 0o700)
	os.WriteFile(filepath.Join(dir, "raw", "ab", "x.json"), []byte("{}"), 0o600)
	os.MkdirAll(filepath.Join(dir, "inbox"), 0o700)
	rec := filepath.Join(dir, "rec")
	os.MkdirAll(filepath.Join(rec, "sessions", "s1"), 0o700)
	os.WriteFile(filepath.Join(rec, "sessions", "s1", "transcript.json"), []byte("{}"), 0o600)
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("data_dir = %q\n[embed]\nprovider = \"hash\"\n[llm]\nprovider = \"none\"\n[m365]\nenabled = false\n[recorder]\ndata = %q\n", dir, rec)), 0o600)
	e, err := engine.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	a := &App{eng: e, cfgPath: cfg} // no context: events need the app window
	out, err := a.Backup()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"manifest.json", "data/musubi.db", "data/raw/ab/x.json", "recorder/sessions/s1/transcript.json", "config/config.toml"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("backup lacks %s", f)
		}
	}
	if filepath.Dir(out) != filepath.Join(home, ".local", "share", "musubi", "backups") {
		t.Errorf("backup at %s", out)
	}
	st, _ := a.Storage()
	if st.LastBackup != filepath.Base(out) || st.Total == 0 {
		t.Errorf("storage: %+v", st)
	}
	bs, err := a.Backups()
	if err != nil || len(bs) != 1 || bs[0].Kind != "manual" || !bs[0].Recorder {
		t.Errorf("backups: %+v %v", bs, err)
	}
	if err := a.RevealPath("/etc/hosts"); err == nil {
		t.Error("revealed a path outside the dataset")
	}
}
