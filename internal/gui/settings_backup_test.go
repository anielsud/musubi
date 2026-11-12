package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/anielsud/musubi/internal/engine"
)

// A backup holds the database, the raw store, the inbox and the config.
func TestBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "raw", "ab"), 0o700)
	os.WriteFile(filepath.Join(dir, "raw", "ab", "x.json"), []byte("{}"), 0o600)
	os.MkdirAll(filepath.Join(dir, "inbox"), 0o700)
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("data_dir = %q\n[embed]\nprovider = \"hash\"\n[llm]\nprovider = \"none\"\n[m365]\nenabled = false\n", dir)), 0o600)
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
	for _, f := range []string{"musubi.db", "raw/ab/x.json", "config.toml"} {
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
	if err := a.RevealPath("/etc/hosts"); err == nil {
		t.Error("revealed a path outside the dataset")
	}
}
