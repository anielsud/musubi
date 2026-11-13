package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// fakeHome points the XDG data folder at a temporary one, so Tomoe's
// folder in these tests is never the real one.
func fakeHome(t *testing.T) (tomoe string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	t.Cleanup(func() { config.SetDataDir("") })
	return filepath.Join(base, "tomoe")
}

// What run writes -- its data folder, sessions, voice files and live
// socket -- is all outside Tomoe's data folder.
func TestRunDataFolderIsNotTomoes(t *testing.T) {
	tomoe := fakeHome(t)
	dir, err := useDataDir("")
	if err != nil {
		t.Fatal(err)
	}
	if dir != defaultDataDir() {
		t.Errorf("data folder %q, want the default %q", dir, defaultDataDir())
	}
	for what, path := range map[string]string{
		"data folder": config.DataDir(),
		"sessions":    config.SessionDir(),
		"live socket": livefeed.SocketPath(),
		"profiles":    voices.Path(config.DataDir(), "m"),
		"owner voice": voices.OwnerPath(config.DataDir(), "m"),
	} {
		if within(realPath(path), realPath(tomoe)) {
			t.Errorf("%s %s is inside Tomoe's folder %s", what, path, tomoe)
		}
	}
	if got := config.ModelDir(); got != filepath.Join(tomoe, "models") {
		t.Errorf("models from %q, want Tomoe's", got)
	}
}

func TestDataFolderRefusesTomoes(t *testing.T) {
	tomoe := fakeHome(t)
	if err := os.MkdirAll(filepath.Join(tomoe, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(tomoe, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		tomoe,
		tomoe + "/",
		filepath.Join(tomoe, "sessions"),
		filepath.Join(tomoe, "not-yet", "made"),
		filepath.Join(tomoe, "..", "tomoe"),
		link,
		filepath.Join(link, "recorder"),
	} {
		if _, err := useDataDir(dir); err == nil {
			t.Errorf("useDataDir(%q) accepted Tomoe's folder", dir)
		}
	}
	if config.DataDir() != tomoe {
		t.Errorf("a refused folder still moved DataDir to %q", config.DataDir())
	}
	// A sibling whose name only starts like Tomoe's is fine.
	if _, err := useDataDir(tomoe + "-recorder"); err != nil {
		t.Errorf("useDataDir(sibling) = %v", err)
	}
}
