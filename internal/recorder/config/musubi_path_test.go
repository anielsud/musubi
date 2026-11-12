package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The app's dataset config, once named, is what the recorder reads,
// restarts included; $MUSUBI_CONFIG still wins.
func TestResolveMusubiPath(t *testing.T) {
	t.Setenv("MUSUBI_CONFIG", "")
	data := t.TempDir()
	if got := ResolveMusubiPath(data); got != MusubiPath() {
		t.Errorf("nothing stored: %s", got)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte("[recorder]\nof_record = \"musubi\"\n"), 0o600)
	if err := StoreMusubiPath(data, cfg); err != nil {
		t.Fatal(err)
	}
	if got := ResolveMusubiPath(data); got != cfg {
		t.Errorf("stored: %s, want %s", got, cfg)
	}
	t.Setenv("MUSUBI_CONFIG", "/elsewhere/config.toml")
	if got := ResolveMusubiPath(data); got != "/elsewhere/config.toml" {
		t.Errorf("env: %s", got)
	}
	for _, bad := range []string{"config.toml", filepath.Join(t.TempDir(), "missing.toml"), filepath.Dir(cfg), "/etc/passwd"} {
		if err := StoreMusubiPath(data, bad); err == nil {
			t.Errorf("stored %q", bad)
		}
	}
}
