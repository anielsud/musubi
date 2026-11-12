package store

import (
	"path/filepath"
	"testing"
)

func TestBackupTo(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dst := filepath.Join(dir, "copy.db")
	if err := s.BackupTo(dst); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := s.BackupTo(dst); err == nil {
		t.Error("overwrote an existing backup")
	}
}
