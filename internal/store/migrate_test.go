package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ledger(t *testing.T, path string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	done, err := appliedMigrations(db)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func ledgerComplete(t *testing.T, path string) {
	t.Helper()
	done := ledger(t, path)
	for _, m := range migrations {
		if !done[m.id] {
			t.Errorf("step %s not in the ledger", m.id)
		}
	}
}

func count(t *testing.T, path, q string) int {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A new database records every step without a copy; opened again, no
// step runs.
func TestLedgerOnNewDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, err := openAt(t, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Migrated) != len(migrations) || s.UpgradeBackup != "" {
		t.Errorf("migrated %v, backup %q", s.Migrated, s.UpgradeBackup)
	}
	s.Close()
	ledgerComplete(t, path)
	if s, err = openAt(t, path, Options{}); err != nil || len(s.Migrated) != 0 || s.UpgradeBackup != "" {
		t.Errorf("reopen: migrated %v, backup %q, err %v", s.Migrated, s.UpgradeBackup, err)
	}
}

// A database from before the ledger is copied, then every step is
// recorded without losing a row.
func TestLedgerOnDatabaseBeforeIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, _ := openAt(t, path, Options{})
	s.db.Exec(`INSERT INTO judgments(item_id, text, verdict, note, at) VALUES ('t:1', 'x', 'verified', '', 1)`)
	s.Close()
	exec(t, path, `DROP TABLE migrations`)
	s, err := openAt(t, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.UpgradeBackup == "" {
		t.Error("no copy before the ledger's first run")
	}
	if len(s.Migrated) != len(migrations) {
		t.Errorf("migrated %v", s.Migrated)
	}
	if b, a := s.MigratedCounts[0], s.MigratedCounts[1]; b.Change(a) != "" || b["judgments"] != 1 {
		t.Errorf("counts %v → %v", b, a)
	}
	s.Close()
	ledgerComplete(t, path)
}

// A step that would lose kept rows rolls back: the database is as it was,
// the step isn't recorded, and the error names the copy.
func TestLosingKeptRowsStopsTheUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, _ := openAt(t, path, Options{})
	s.db.Exec(`INSERT INTO judgments(item_id, text, verdict, note, at) VALUES ('t:1', 'x', 'verified', '', 1)`)
	s.Close()
	saved := migrations
	t.Cleanup(func() { migrations = saved })
	migrations = append(append([]migration{}, saved...),
		migration{id: "test-adds-column", run: func(q dbtx) error { return addColumns(q, "judgments", "extra TEXT NOT NULL DEFAULT ''") }},
		migration{id: "test-loses-judgments", afterSchema: true, run: func(q dbtx) error {
			_, err := q.Exec(`DELETE FROM judgments`)
			return err
		}})
	_, err := OpenWith(path, Options{})
	if err == nil || !strings.Contains(err.Error(), "judgments 1 → 0") || !strings.Contains(err.Error(), "pre-upgrade-") {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, path, `SELECT count(*) FROM judgments`); n != 1 {
		t.Errorf("judgments = %d after a refused upgrade", n)
	}
	if n := count(t, path, `SELECT count(*) FROM pragma_table_info('judgments') WHERE name = 'extra'`); n != 0 {
		t.Error("an earlier step of the refused upgrade stayed")
	}
	if done := ledger(t, path); done["test-adds-column"] || done["test-loses-judgments"] {
		t.Errorf("ledger %v", done)
	}
	// Without the bad step the same database upgrades.
	migrations = migrations[:len(migrations)-1]
	if _, err := openAt(t, path, Options{NoUpgradeBackup: true}); err != nil {
		t.Fatal(err)
	}
}

// A schema change empties the derived tables (rebuilt from raw) and keeps
// the kept ones; the count check allows it.
func TestSchemaStepKeepsKeptTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, _ := openAt(t, path, Options{})
	s.db.Exec(`INSERT INTO items(id, source, ref, raw_hash, kind, title, occurred_at, ended_at, thread, segments, participants, native, content_hash)
		VALUES ('t:1', 't', '1', '', 'email', 'x', 0, 0, '', '[]', '[]', '{}', '')`)
	s.db.Exec(`INSERT INTO corrections(item_id, statement, extracted, field, old, new, by, at) VALUES ('t:1', 1, 'x', 'text', 'x', 'y', 'owner', 1)`)
	s.Close()
	exec(t, path, `PRAGMA user_version = 3`)
	s, err := openAt(t, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if n := count(t, path, `SELECT count(*) FROM items`); n != 0 {
		t.Errorf("items = %d after a schema change", n)
	}
	if n := count(t, path, `SELECT count(*) FROM corrections`); n != 1 {
		t.Errorf("corrections = %d after a schema change", n)
	}
}

// A redo that loses kept rows gets them back and an error; one that
// doesn't removes its copy.
func TestKeptGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "musubi.db")
	s, _ := openAt(t, path, Options{})
	s.db.Exec(`INSERT INTO judgments(item_id, text, verdict, note, at) VALUES ('t:1', 'x', 'verified', '', 1), ('t:1', 'y', 'false', '', 2)`)
	s.db.Exec(`INSERT INTO owner_links(item_id, text, rel, by, at) VALUES ('t:1', 'x', 'done', 'owner', 1)`)
	s.db.Exec(`INSERT INTO learned_aliases(keep, variant, kind, status, at) VALUES ('A', 'a', 'spelling', 'proposed', 1)`)

	g, err := s.GuardKept()
	if err != nil {
		t.Fatal(err)
	}
	s.DropProposedAliases() // not the owner's: not held
	if after, err := g.Finish(); err != nil || after["judgments"] != 2 {
		t.Fatalf("clean run: %v, %v", after, err)
	}
	if _, err := os.Stat(g.path); !os.IsNotExist(err) {
		t.Error("the copy of a clean run stayed")
	}

	g, err = s.GuardKept()
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`DELETE FROM judgments WHERE text = 'y'`)
	s.db.Exec(`DELETE FROM owner_links`)
	after, err := g.Finish()
	if err == nil || !strings.Contains(err.Error(), "owner_links 1 → 0") {
		t.Fatalf("err = %v", err)
	}
	if after["judgments"] != 2 || after["owner_links"] != 1 {
		t.Errorf("after = %v", after)
	}
	var verdict string
	if err := s.db.QueryRow(`SELECT verdict FROM judgments WHERE text = 'y'`).Scan(&verdict); err != nil || verdict != "false" {
		t.Errorf("put back %q, %v", verdict, err)
	}
	if _, err := os.Stat(g.path); err != nil {
		t.Errorf("the copy wasn't kept: %v", err)
	}
}
