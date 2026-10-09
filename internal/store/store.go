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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

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
		args = append(args, int(sensitivity.OpenLevel))
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
