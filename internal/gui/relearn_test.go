package gui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The profiles are learnt again automatically when the learning rules
// change, so the app no longer offers "Learn from saved meetings" (the
// recorder's learn_saved op stays for tools); Explore → Voices notes a
// relearn instead.
func TestNoLearnFromSavedButton(t *testing.T) {
	src := filepath.Join("..", "..", "frontend", "src")
	var note bool
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")) {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		for _, bad := range []string{"Learn from saved meetings", "learnSaved", "LearnSaved"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s still has %q", path, bad)
			}
		}
		note = note || strings.Contains(s, "Voice profiles updated for new rules on")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !note {
		t.Error("no note on a relearn for new rules")
	}
}
