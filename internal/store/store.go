// Package store is Musubi's SQLite database: items, chunks, labels, vectors,
// entities and their links.
//
// Two kinds of access exist. Pipeline methods (pipe.go) read and write
// everything, because stages must process everything; they never return
// results to a person. Query methods (query.go) serve people and automatic
// uses, and every one takes a sensitivity.Scope that is applied in SQL.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	sqlite3 "github.com/mattn/go-sqlite3"

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
var kept = map[string]bool{"forgotten": true, "audit": true, "judgments": true, "corrections": true, "learned_aliases": true, "thread_edits": true, "owner_links": true}

type Store struct {
	// db is the one connection that writes (and, without a read pool,
	// reads). rdb serves the read paths (reads.go): a pool of read-only
	// connections, or db itself when Options.ReadConns is 1 or less.
	db  *sql.DB
	rdb *sql.DB
	// sdb, with a read pool, is a read-only connection that only ever asks
	// PRAGMA data_version, so KnowledgeSignature doesn't wait behind a
	// write; sgen counts its (re)connections. nil without a pool.
	sdb   *sql.DB
	sgen  atomic.Int64
	codes codeIndexes // nearest-neighbour prefilters (vecindex.go)
	// ReadWithRules applies the owner's spelling rules when statements are
	// read for showing or writing from (Corrector); set from
	// knowledge.apply_spellings.
	ReadWithRules bool
	// VectorEncoding is how vectors are written: "f32" (or "") or "f16"
	// (vecenc.go); set from store.vector_encoding. Both always read.
	VectorEncoding string
	// UpgradeBackup is the copy made before this open upgraded the
	// database ("" when nothing was upgraded).
	UpgradeBackup string
	// Migrated is the ledger steps this open applied (migrate.go), and
	// MigratedCounts the row counts before and after them.
	Migrated       []string
	MigratedCounts [2]Counts
	path           string
}

func init() { sqlite_vec.Auto() }

// Options tune the connections (config [store]). The zero value is the
// behaviour before they existed: one connection, SQLite's own page cache
// (2 MB) and no memory-mapped reads.
type Options struct {
	// CacheMB is each connection's page cache in MiB (0: SQLite's default).
	CacheMB int
	// MmapMB maps up to this much of the file into memory for reads, so a
	// page the OS has cached is read without a system call and a copy
	// (0: off, SQLite's default).
	MmapMB int
	// ReadConns opens that many read-only connections for the read paths
	// (reads.go); WAL lets them read while the single writer writes. 1 or
	// less: reads share the writer's connection, as before.
	ReadConns int
	// NoUpgradeBackup skips the copy made before an older database is
	// upgraded ([store] backup_before_upgrade = false). A newer database is
	// refused either way.
	NoUpgradeBackup bool
}

// Open opens or creates the database at path with the zero Options.
func Open(path string) (*Store, error) { return OpenWith(path, openOptions) }

// openOptions is what Open uses: the zero Options, except when this
// package's tests run again over a read pool (readpool_test.go).
var openOptions Options

// connector opens connections with a hook run on each (mmap_size, which
// the driver has no DSN parameter for). sqlite-vec is an auto extension,
// so it is loaded into every connection however it is opened.
type connector struct {
	dsn string
	drv *sqlite3.SQLiteDriver
}

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.drv.Open(c.dsn) }
func (c connector) Driver() driver.Driver                        { return c.drv }

func openDB(dsn string, o Options, onConnect func()) *sql.DB {
	if o.CacheMB > 0 {
		dsn += fmt.Sprintf("&_cache_size=%d", -o.CacheMB*1024) // negative: KiB
	}
	drv := &sqlite3.SQLiteDriver{}
	if o.MmapMB > 0 || onConnect != nil {
		drv.ConnectHook = func(c *sqlite3.SQLiteConn) error {
			if onConnect != nil {
				onConnect()
			}
			if o.MmapMB > 0 {
				if _, err := c.Exec(fmt.Sprintf("PRAGMA mmap_size = %d", int64(o.MmapMB)<<20), nil); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return sql.OpenDB(connector{dsn: dsn, drv: drv})
}

// OpenWith opens or creates the database at path.
func OpenWith(path string, o Options) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=10000&_synchronous=NORMAL"
	db := openDB(dsn, o, nil)
	// One writer at a time is SQLite's model; a single connection also keeps
	// transactions from interleaving between goroutines.
	db.SetMaxOpenConns(1)
	// Others' datasets are upgraded by builds they install: never drop or
	// misread a newer database, and copy an older one before changing it.
	upgrade, from, err := upgradeNeeded(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	var backup string
	if upgrade && !o.NoUpgradeBackup {
		if backup, err = backupBeforeUpgrade(db, path, from, time.Now()); err != nil {
			db.Close()
			return nil, err
		}
	}
	applied, before, after, err := migrateDB(db, backup)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db, rdb: db, path: path, ReadWithRules: true, UpgradeBackup: backup, Migrated: applied, MigratedCounts: [2]Counts{before, after}}
	if o.ReadConns > 1 {
		// Read-only twice over: the file is opened read-only (mode=ro) and
		// query_only refuses any write, so a read path routed here by
		// mistake fails instead of writing outside the single writer.
		ro := "file:" + path + "?mode=ro&_query_only=1&_busy_timeout=10000"
		s.rdb = openDB(ro, o, nil)
		s.rdb.SetMaxOpenConns(o.ReadConns)
		s.rdb.SetMaxIdleConns(o.ReadConns)
		s.sdb = openDB(ro, Options{}, func() { s.sgen.Add(1) })
		s.sdb.SetMaxOpenConns(1)
		s.sdb.SetMaxIdleConns(1)
		for _, d := range []*sql.DB{s.rdb, s.sdb} {
			if err := d.Ping(); err != nil {
				s.Close()
				return nil, fmt.Errorf("read connections: %w", err)
			}
		}
	}
	return s, nil
}

func (s *Store) Close() error {
	if s.rdb != nil && s.rdb != s.db {
		s.rdb.Close()
	}
	if s.sdb != nil {
		s.sdb.Close()
	}
	return s.db.Close()
}

// BackupTo writes a consistent copy of the database to path (which must not
// exist), while it stays open and in use.
func (s *Store) BackupTo(path string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
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
func addColumns(db dbtx, table string, defs ...string) error {
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
