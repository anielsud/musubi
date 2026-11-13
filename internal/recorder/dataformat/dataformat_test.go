package dataformat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A folder from before formats has its voices copied, then the format
// recorded; the next start changes nothing.
func TestPrepareOlderFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "voices", "m.json"), `{"version":1,"profiles":[]}`)
	write(t, filepath.Join(dir, "sessions", "s1", "session.json"), `{"id":"s1"}`)
	from, err := Prepare(dir, true, now)
	if err != nil || from != 0 {
		t.Fatalf("from %d, %v", from, err)
	}
	if v, err := Read(dir); v != Version || err != nil {
		t.Fatalf("format %d, %v", v, err)
	}
	if got := read(t, filepath.Join(dir, "voices.bak-20261112-150000", "m.json")); got != `{"version":1,"profiles":[]}` {
		t.Errorf("copy holds %q", got)
	}
	if from, err := Prepare(dir, true, now.Add(time.Hour)); err != nil || from != Version {
		t.Fatalf("again: from %d, %v", from, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "voices.bak-*")); len(m) != 1 {
		t.Errorf("copies %v, want one", m)
	}
}

// With backups off nothing is copied; a folder with no voices needs none.
func TestPrepareWithoutBackups(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "voices", "m.json"), `{}`)
	if _, err := Prepare(dir, false, now); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "voices.bak-*")); len(m) != 0 {
		t.Errorf("copied with backups off: %v", m)
	}
	empty := filepath.Join(t.TempDir(), "new")
	if _, err := Prepare(empty, true, now); err != nil {
		t.Fatal(err)
	}
	if v, _ := Read(empty); v != Version {
		t.Errorf("new folder format %d", v)
	}
}

// A newer folder, or one whose format can't be read, is refused and left
// exactly as it was.
func TestPrepareRefusesNewer(t *testing.T) {
	for _, format := range []string{"99\n", "two"} {
		dir := t.TempDir()
		write(t, filepath.Join(dir, File), format)
		write(t, filepath.Join(dir, "voices", "m.json"), `{"version":7}`)
		if _, err := Prepare(dir, true, now); !errors.Is(err, ErrNewer) {
			t.Fatalf("%q: %v, want ErrNewer", format, err)
		}
		if err := Check(dir); !errors.Is(err, ErrNewer) {
			t.Errorf("%q: Check %v", format, err)
		}
		if read(t, filepath.Join(dir, File)) != format {
			t.Errorf("%q: format rewritten", format)
		}
		if m, _ := filepath.Glob(filepath.Join(dir, "voices.bak-*")); len(m) != 0 {
			t.Errorf("%q: copied %v", format, m)
		}
	}
	if err := Check(t.TempDir()); err != nil {
		t.Errorf("no format: %v", err)
	}
}
