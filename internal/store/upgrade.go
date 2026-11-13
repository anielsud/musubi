package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// formatVersion is what is stored, beyond the tables: one-way changes an
// older build would misread (1, 2026-11-12: claim centroids stored once,
// vectors as float16). Bump it with any such change, and with every
// schemaVersion bump. A build refuses a database of a newer schema or
// format instead of dropping or misreading it, and backs a database up
// before upgrading it (docs/upgrades.md).
const formatVersion = 1

const metaFormat = "format"

// ErrNewer is a database written by a newer Musubi than this one.
var ErrNewer = errors.New("this database was written by a newer Musubi; open it with that version or later (or restore a backup made before the upgrade)")

// keepUpgradeBackups is how many pre-upgrade copies are kept per data
// folder; older ones are removed once a new one is made.
const keepUpgradeBackups = 3

// upgradeNeeded reports whether opening this database will change what
// it holds: an existing database of an older schema or format. A newer
// one is ErrNewer. from names the old version, for the backup's name.
func upgradeNeeded(db *sql.DB) (need bool, from string, err error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return false, "", err
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return false, "", err
	}
	if tables == 0 {
		return false, "", nil // new
	}
	if v > schemaVersion {
		return false, "", fmt.Errorf("%w (schema %d; this build reads up to %d)", ErrNewer, v, schemaVersion)
	}
	f, err := storedFormat(db)
	if err != nil {
		return false, "", err
	}
	if f > formatVersion {
		return false, "", fmt.Errorf("%w (format %d; this build reads up to %d)", ErrNewer, f, formatVersion)
	}
	return v != schemaVersion || f < formatVersion, fmt.Sprintf("s%d-f%d", v, f), nil
}

// storedFormat is the format a database was last opened at; 0 before
// formats were recorded (or with no store_meta yet).
func storedFormat(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`).Scan(&n); err != nil || n == 0 {
		return 0, err
	}
	var s string
	err := db.QueryRow(`SELECT value FROM store_meta WHERE key = ?`, metaFormat).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

func recordFormat(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO store_meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, metaFormat, strconv.Itoa(formatVersion))
	return err
}

// backupBeforeUpgrade copies the database, as it is, to
// backups/pre-upgrade-<from>-<time>.db beside it, and keeps the newest
// few. Failing to copy stops the upgrade: nothing has been changed yet.
func backupBeforeUpgrade(db *sql.DB, path, from string, now time.Time) (string, error) {
	dir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "pre-upgrade-"+from+"-"+now.Format("20060102-150405")+".db")
	if _, err := db.Exec(`VACUUM INTO ?`, out); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("backing up %s before upgrading it: %w (free some space, or set [store] backup_before_upgrade = false to upgrade without a copy)", path, err)
	}
	old, _ := filepath.Glob(filepath.Join(dir, "pre-upgrade-*.db"))
	sort.Slice(old, func(i, j int) bool { return backupTime(old[i]) < backupTime(old[j]) })
	for len(old) > keepUpgradeBackups {
		os.Remove(old[0])
		old = old[1:]
	}
	return out, nil
}

// backupTime is a pre-upgrade copy's time, as written in its name.
func backupTime(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".db")
	parts := strings.Split(name, "-")
	if len(parts) < 2 {
		return name
	}
	return parts[len(parts)-2] + parts[len(parts)-1]
}
