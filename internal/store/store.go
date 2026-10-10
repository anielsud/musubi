// Package store is Musubi's SQLite database: items, chunks, labels, vectors,
// entities and their links.
//
// Two kinds of access exist. Pipeline methods (pipe.go) read and write
// everything, because stages must process everything; they never return
// results to a person. Query methods (query.go) serve people and automatic
// uses, and every one takes a sensitivity.Scope that is applied in SQL.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/anielsud/musubi/internal/sensitivity"
)

//go:embed schema.sql
var schema string

// schemaVersion changes when schema.sql changes incompatibly. Everything
// except the forget list and the audit log is rebuilt from the raw store, so
// an upgrade drops the derived tables and the next update refills them.
const schemaVersion = 2

// kept survive a schema upgrade: they are not derived from raw.
var kept = map[string]bool{"forgotten": true, "audit": true, "judgments": true}

type Store struct{ db *sql.DB }

func init() { sqlite_vec.Auto() }

// Open opens or creates the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=10000&_synchronous=NORMAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// One writer at a time is SQLite's model; a single connection also keeps
	// transactions from interleaving between goroutines.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v == schemaVersion {
		return nil
	}
	rows, err := db.Query(`SELECT type, name FROM sqlite_master WHERE type IN ('table', 'trigger') AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'chunks_fts_%'`)
	if err != nil {
		return err
	}
	var drop [][2]string
	for rows.Next() {
		var typ, name string
		rows.Scan(&typ, &name)
		if !kept[name] {
			drop = append(drop, [2]string{typ, name})
		}
	}
	rows.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	for _, d := range drop {
		if _, err := db.Exec(fmt.Sprintf(`DROP %s IF EXISTS "%s"`, strings.ToUpper(d[0]), d[1])); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// visArgs builds the SQL that limits rows to scope. il and cl are the
// aliases of the item-level and chunk-level labels rows; cl may be "" for
// item-level checks. The SQL mirrors sensitivity.Visible.
func visSQL(sc sensitivity.Scope, il, cl, itemCol string) (string, []any) {
	level := il + ".level"
	cats := il + ".categories"
	if cl != "" {
		level = fmt.Sprintf("max(%s.level, coalesce(%s.level, 0))", il, cl)
		cats = fmt.Sprintf("(%s.categories | coalesce(%s.categories, 0))", il, cl)
	}
	denied := int64(sensitivity.AllCategories &^ sc.Allowed)
	q := fmt.Sprintf("(%s <= ? AND (%s & ?) = 0", level, cats)
	args := []any{int(sc.MaxLevel), denied}
	if n := len(sc.Audience); n > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", n), ",")
		q += fmt.Sprintf(" AND ((%s <= ? AND %s = 0) OR (SELECT count(*) FROM item_audience aud WHERE aud.item_id = %s AND aud.principal IN (%s)) = ?)",
			level, cats, itemCol, ph)
		args = append(args, int(sc.Open))
		for _, a := range sc.Audience {
			args = append(args, a)
		}
		args = append(args, n)
	}
	return q + ")", args
}

// chunkVisJoin is the join every chunk-level query uses with visSQL(sc, "il", "cl", "c.item_id").
const chunkVisJoin = ` JOIN labels il ON il.item_id = c.item_id AND il.chunk_seq = -1
 LEFT JOIN labels cl ON cl.item_id = c.item_id AND cl.chunk_seq = c.seq `
