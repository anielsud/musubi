package main

import (
	"os"
	"path/filepath"
	"testing"
)

// cp -c clones a folder (here on the temp volume, APFS on a Mac) with its
// files and their times.
func TestCloneCopyFolder(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	write(t, filepath.Join(src, "a", "b.txt"), "b")
	dst := filepath.Join(t.TempDir(), "dst")
	if err := cloneCopy(src, dst); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dst, "a", "b.txt")) != "b" {
		t.Error("not copied")
	}
	s, _ := os.Stat(filepath.Join(src, "a", "b.txt"))
	d, _ := os.Stat(filepath.Join(dst, "a", "b.txt"))
	if !s.ModTime().Equal(d.ModTime()) {
		t.Errorf("time %v, want %v", d.ModTime(), s.ModTime())
	}
}
