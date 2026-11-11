package recording

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anielsud/musubi/internal/recorder/voices"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCopyVoicesTakesTomoesVoiceFilesOnly(t *testing.T) {
	tomoe, rec := t.TempDir(), t.TempDir()
	profiles := voices.Path(tomoe, "model-a")
	owner := voices.OwnerPath(tomoe, "model-a")
	writeFile(t, profiles, `{"version":1,"profiles":[]}`)
	writeFile(t, owner, `{"n":0}`)
	writeFile(t, profiles+".bak-20260101", "old")
	writeFile(t, profiles+".tmp", "half")

	if err := CopyVoices(tomoe, rec); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, voices.Path(rec, "model-a")); got != `{"version":1,"profiles":[]}` {
		t.Errorf("profiles copy = %q", got)
	}
	if got := readFile(t, voices.OwnerPath(rec, "model-a")); got != `{"n":0}` {
		t.Errorf("owner voice copy = %q", got)
	}
	ents, _ := os.ReadDir(filepath.Join(rec, "voices"))
	if len(ents) != 2 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("recorder's voices = %v, want only the two .json files", names)
	}
}

// Tomoe's copy wins over what the recorder learnt, an unchanged file is
// not rewritten, and Tomoe's folder is never written.
func TestCopyVoicesTomoeWinsAndIsReadOnly(t *testing.T) {
	tomoe, rec := t.TempDir(), t.TempDir()
	src := voices.Path(tomoe, "model-a")
	dst := voices.Path(rec, "model-a")
	writeFile(t, src, "tomoe's")
	writeFile(t, dst, "recorder's")
	if err := os.Chmod(filepath.Dir(src), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(src), 0o700) })

	if err := CopyVoices(tomoe, rec); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dst); got != "tomoe's" {
		t.Errorf("after copy the recorder has %q, want Tomoe's", got)
	}
	before, _ := os.Stat(dst)
	if err := CopyVoices(tomoe, rec); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(dst); !after.ModTime().Equal(before.ModTime()) {
		t.Error("an unchanged voice file was rewritten")
	}
}

func TestCopyVoicesWithoutTomoeVoices(t *testing.T) {
	if err := CopyVoices(t.TempDir(), t.TempDir()); err != nil {
		t.Errorf("no voices folder: %v, want nil", err)
	}
	dir := t.TempDir()
	writeFile(t, voices.Path(dir, "model-a"), "x")
	if err := CopyVoices(dir, dir); err != nil {
		t.Errorf("same folder both sides: %v, want nil", err)
	}
	if got := readFile(t, voices.Path(dir, "model-a")); got != "x" {
		t.Errorf("same folder both sides changed the file to %q", got)
	}
}

// After a copy, the owner voice is read again from the recorder's copy.
func TestForgetOwnerVoicesRereadsTheCopy(t *testing.T) {
	newTestRecorder(t) // for a data folder of its own
	first := ownerFor("model-b")
	forgetOwnerVoices()
	if ownerFor("model-b") == first {
		t.Error("ownerFor returned the voice loaded before the copy")
	}
}
