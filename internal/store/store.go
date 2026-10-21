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
const schemaVersion = 4

// kept survive a schema upgrade: they are not derived from raw. The
// owner's corrections, decided aliases and thread edits are among them:
// they are kept to the side and applied again to whatever is rebuilt.
var kept = map[string]bool{"forgotten": true, "audit": true, "judgments": true, "corrections": true, "learned_aliases": true, "thread_edits": true}

type Store struct {
	db    *sql.DB
	codes codeIndexes // nearest-neighbour prefilters (vecindex.go)
	// ReadWithRules applies the owner's spelling rules when statements are
	// read for showing or writing from (Corrector); set from
	// knowledge.apply_spellings.
	ReadWithRules bool
}

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
	// Provenance on thread edges (docs/incremental-design.md).
	if err := addColumns(db, "thread_items", "version TEXT NOT NULL DEFAULT ''", "added_in TEXT NOT NULL DEFAULT ''", "added_at INTEGER NOT NULL DEFAULT 0",
		"retired_in TEXT NOT NULL DEFAULT ''", "retired_at INTEGER NOT NULL DEFAULT 0", "retired_why TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumns(db, "topics", "since_review INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Corrections made before this column had their threads mended then.
	if err := addColumns(db, "corrections", "related INTEGER NOT NULL DEFAULT 1"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumns(db, "claim_relations", "version TEXT NOT NULL DEFAULT ''", "made_in TEXT NOT NULL DEFAULT ''", "made_at INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumns(db, "statements", "worth REAL", "worth_version TEXT NOT NULL DEFAULT ''", "search_text TEXT NOT NULL DEFAULT ''",
		"status TEXT NOT NULL DEFAULT ''", "who TEXT NOT NULL DEFAULT ''", "evidence TEXT NOT NULL DEFAULT ''",
		"level INTEGER NOT NULL DEFAULT 0", "categories INTEGER NOT NULL DEFAULT 0", "label_version TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := markReplacedAliases(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, ReadWithRules: true}, nil
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
	if err != nil {
		return err
	}
	return upgradeKept(db)
}

// upgradeKept adds columns to kept tables (which a version change doesn't
// drop) in place.
func upgradeKept(db *sql.DB) error {
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(judgments)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		cols[name] = true
	}
	rows.Close()
	if len(cols) == 0 {
		return nil // created fresh by the schema
	}
	for _, c := range []string{"chunk_hash TEXT NOT NULL DEFAULT ''", "line INTEGER NOT NULL DEFAULT 0"} {
		name := strings.Fields(c)[0]
		if !cols[name] {
			if _, err := db.Exec(`ALTER TABLE judgments ADD COLUMN ` + c); err != nil {
				return err
			}
		}
	}
	return nil
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
	return visExpr(sc, level, cats, itemCol)
}

// statementVis is visSQL for statements (alias st) read through
// chunkVisJoin: a statement is as sensitive as its passage, its item and
// its own label (a point's evidence can span passages, and a personal
// detail is labelled on the point, not the passage).
func statementVis(sc sensitivity.Scope) (string, []any) {
	return visExpr(sc, stLevel, stCats, "c.item_id")
}

const (
	stLevel = "max(il.level, coalesce(cl.level, 0), st.level)"
	stCats  = "(il.categories | coalesce(cl.categories, 0) | st.categories)"
)

func visExpr(sc sensitivity.Scope, level, cats, itemCol string) (string, []any) {
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

// addColumns adds missing columns to a table in place, without a schema
// version change. A version change drops every derived table, and with them
// the extracted statements, which at full scale cost hours to rebuild; a
// new column on them must not.
func addColumns(db *sql.DB, table string, defs ...string) error {
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		cols[name] = true
	}
	rows.Close()
	if len(cols) == 0 {
		return nil
	}
	for _, d := range defs {
		if name := strings.Fields(d)[0]; !cols[name] {
			// Another process opening the same database may add it first.
			if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + d); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	return nil
}
