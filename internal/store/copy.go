package store

import (
	"database/sql"
	"fmt"
	"net/url"
)

// Versions are the schema and format this build writes; a database of a
// higher one is refused (upgrade.go).
func Versions() (schema, format int) { return schemaVersion, formatVersion }

// fileDSN is a plain connection to a database file: none of OpenWith's
// migrations, so a copy or a look leaves the file as it is.
func fileDSN(path, params string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + params
}

// CopyFile writes a consistent copy of the database file at src to dst
// (which must not exist) without upgrading it: for a database no Store
// has open (a backup made before a restore).
func CopyFile(src, dst string) error {
	db, err := sql.Open("sqlite3", fileDSN(src, "_busy_timeout=10000"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`VACUUM INTO ?`, dst)
	return err
}

// FileInfo is what a database file holds, read without changing it: its
// schema and format, and the row counts a restore is judged by (tables
// missing from it are left out).
type FileInfo struct {
	Schema int              `json:"schema"`
	Format int              `json:"format"`
	Counts map[string]int64 `json:"counts,omitempty"`
}

// manifestTables, counted into a backup's manifest: the items read, what was learnt from them, and the
// owner's word.
var manifestTables = []string{"items", "statements", "thread_items", "corrections", "judgments", "learned_aliases", "thread_edits", "forgotten"}

// ReadFileInfo reads FileInfo from the database file at path, which no
// one is writing (a backup's copy).
func ReadFileInfo(path string) (FileInfo, error) {
	var fi FileInfo
	db, err := sql.Open("sqlite3", fileDSN(path, "mode=ro&immutable=1"))
	if err != nil {
		return fi, err
	}
	defer db.Close()
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&fi.Schema); err != nil {
		return fi, err
	}
	if fi.Format, err = storedFormat(db); err != nil {
		return fi, err
	}
	fi.Counts = map[string]int64{}
	for _, t := range manifestTables {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, t).Scan(&n); err != nil {
			return fi, err
		}
		if n == 0 {
			continue
		}
		var c int64
		// t is one of manifestTables, never input.
		if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM "%s"`, t)).Scan(&c); err != nil {
			return fi, err
		}
		fi.Counts[t] = c
	}
	return fi, nil
}
