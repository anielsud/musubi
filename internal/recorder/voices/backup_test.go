package voices

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func copies(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(dir + ".bak-*")
	return m
}

// A copy is made only when the folder changed since the last one, and
// only the newest KeepBackups are kept.
func TestBackupKeepsNewest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "voices")
	if got, err := Backup(dir, time.Now()); got != "" || err != nil {
		t.Fatalf("no folder: %q, %v", got, err)
	}
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for i := range KeepBackups + 2 {
		writeFile(t, filepath.Join(dir, "m.json"), []byte{byte('a' + i)})
		writeFile(t, filepath.Join(dir, "m.json.tmp"), []byte("half"))
		if _, err := Backup(dir, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got := copies(t, dir)
	if len(got) != KeepBackups || filepath.Base(got[0]) != "voices.bak-20261112-090200" {
		t.Fatalf("copies %v", got)
	}
	if _, err := os.Stat(filepath.Join(got[0], "m.json.tmp")); err == nil {
		t.Error("copied a half-written save")
	}
	last, err := Backup(dir, at.Add(time.Hour))
	if err != nil || last != got[len(got)-1] || len(copies(t, dir)) != KeepBackups {
		t.Errorf("unchanged folder copied again: %q, %v", last, err)
	}
}

// A file a newer Musubi wrote isn't opened, and is left as it was.
func TestOpenRefusesNewerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voices", "m.json")
	body := []byte(`{"version":2,"profiles":[],"shape":"new"}`)
	writeFile(t, path, body)
	if _, err := Open(path, sameFold, DefaultParams()); !errors.Is(err, ErrNewer) {
		t.Fatalf("Open: %v, want ErrNewer", err)
	}
	if err := CheckFile(path); !errors.Is(err, ErrNewer) {
		t.Errorf("CheckFile: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(body) {
		t.Error("file changed")
	}
}

// When a newer Musubi rewrites the file under an open store, the store
// stops writing it and naming voices from it.
func TestStoreStopsOnNewerFile(t *testing.T) {
	s, path := open(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.Learn("m1", at, []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.1))}}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":2,"profiles":[]}`)
	writeFile(t, path, body)
	future := time.Now().Add(time.Hour)
	os.Chtimes(path, future, future)
	if got := s.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0.1))}, nil); len(got) != 0 {
		t.Errorf("named from a newer file: %v", got)
	}
	if _, err := s.Learn("m2", at, []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.1))}}); !errors.Is(err, ErrNewer) {
		t.Errorf("Learn: %v, want ErrNewer", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(body) {
		t.Error("newer file overwritten")
	}
}

// A file converted on load (a meeting listed twice) is copied before the
// first save rewrites it; with backups off it isn't; one that needed no
// conversion isn't either.
func TestConversionCopiedFirst(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h := Heard{Session: "m1", At: at, Source: FromTile, Voice: voiceOf(12, unit(0, 1, 0.1))}
	old, err := json.Marshal(map[string]any{"version": 1, "profiles": []*Profile{{ID: "p1", Name: "Ilse Novak", Meetings: []Heard{h, h}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false} {
		dir := filepath.Join(t.TempDir(), "voices")
		path := filepath.Join(dir, "m.json")
		writeFile(t, path, old)
		s, err := Open(path, sameFold, DefaultParams())
		if err != nil {
			t.Fatal(err)
		}
		s.SetBackups(on)
		if err := s.Confirm("p1"); err != nil {
			t.Fatal(err)
		}
		got := copies(t, dir)
		if !on {
			if len(got) != 0 {
				t.Errorf("backups off: copied %v", got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("copies %v, want one", got)
		}
		if b, _ := os.ReadFile(filepath.Join(got[0], "m.json")); string(b) != string(old) {
			t.Error("the copy isn't the file as it was")
		}
		if err := s.Confirm("p1"); err != nil {
			t.Fatal(err)
		}
		if n := len(copies(t, dir)); n != 1 {
			t.Errorf("%d copies after a second save, want 1", n)
		}
	}
	s, path := open(t)
	if _, err := s.Learn("m1", at, []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.1))}}); err != nil {
		t.Fatal(err)
	}
	if got := copies(t, filepath.Dir(path)); len(got) != 0 {
		t.Errorf("copied with nothing converted: %v", got)
	}
}
