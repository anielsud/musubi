package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openAt(t *testing.T, path string, o Options) (*Store, error) {
	t.Helper()
	s, err := OpenWith(path, o)
	if err == nil {
		t.Cleanup(func() { s.Close() })
	}
	return s, err
}

// exec runs SQL on the file directly, as another build would have left it.
func exec(t *testing.T, path, q string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	b, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "pre-upgrade-*.db"))
	return b
}

func TestNewDatabaseNeedsNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, err := openAt(t, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.UpgradeBackup != "" || len(backups(t, path)) != 0 {
		t.Errorf("a new database was backed up: %q", s.UpgradeBackup)
	}
	s.Close()
	// Opening it again at the same format changes nothing either.
	if s, err = openAt(t, path, Options{}); err != nil || s.UpgradeBackup != "" {
		t.Errorf("reopen: backup %q, err %v", s.UpgradeBackup, err)
	}
}

func TestOlderFormatIsBackedUpFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, err := openAt(t, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// As a build before formats were recorded left it.
	exec(t, path, `DELETE FROM store_meta WHERE key = 'format'`)
	s, err = openAt(t, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.UpgradeBackup == "" || !strings.Contains(filepath.Base(s.UpgradeBackup), "pre-upgrade-s4-f0-") {
		t.Fatalf("backup = %q", s.UpgradeBackup)
	}
	if _, err := os.Stat(s.UpgradeBackup); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, _ = openAt(t, path, Options{}); s.UpgradeBackup != "" {
		t.Error("backed up again once upgraded")
	}
}

func TestNoUpgradeBackupSkipsTheCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, _ := openAt(t, path, Options{})
	s.Close()
	exec(t, path, `DELETE FROM store_meta WHERE key = 'format'`)
	if s, err := openAt(t, path, Options{NoUpgradeBackup: true}); err != nil || s.UpgradeBackup != "" || len(backups(t, path)) != 0 {
		t.Errorf("backup %v, err %v", backups(t, path), err)
	}
}

func TestNewerDatabaseIsRefusedUntouched(t *testing.T) {
	for name, q := range map[string]string{
		"format": `UPDATE store_meta SET value = '99' WHERE key = 'format'`,
		"schema": `PRAGMA user_version = 99`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "musubi.db")
			s, _ := openAt(t, path, Options{})
			if _, err := s.db.Exec(`INSERT INTO store_meta(key, value) VALUES ('marker', 'kept')`); err != nil {
				t.Fatal(err)
			}
			s.Close()
			exec(t, path, q)
			if _, err := OpenWith(path, Options{}); !errors.Is(err, ErrNewer) {
				t.Fatalf("err = %v, want ErrNewer", err)
			}
			// Nothing was dropped: an older schemaVersion check used to
			// drop every derived table here.
			db, _ := sql.Open("sqlite3", "file:"+path)
			defer db.Close()
			var v string
			if err := db.QueryRow(`SELECT value FROM store_meta WHERE key = 'marker'`).Scan(&v); err != nil || v != "kept" {
				t.Errorf("marker = %q, %v", v, err)
			}
		})
	}
}

func TestUpgradeBackupsArePruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, _ := openAt(t, path, Options{})
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := backupBeforeUpgrade(s.db, path, "s4-f0", at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got := backups(t, path)
	if len(got) != keepUpgradeBackups {
		t.Fatalf("kept %d: %v", len(got), got)
	}
	for _, b := range got {
		if strings.Contains(b, "-000000.db") || strings.Contains(b, "-010000.db") {
			t.Errorf("an oldest copy was kept: %s", b)
		}
	}
}
