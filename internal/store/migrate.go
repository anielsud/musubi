package store

import (
	"context"
	"database/sql"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// The migration ledger: every one-off change an open makes to an existing
// database is a named step, run once, in order, and recorded in the
// migrations table with when and by which build. All pending steps run in
// one transaction with schema.sql, and row counts are compared before it
// commits: a step that would lose the owner's rows (or items or
// statements) rolls back and leaves the database as it was.
//
// A step must be idempotent: a database that already has its effect (all
// of them, before the ledger existed) records it without changing
// anything. Ids are names, never renamed; order is this list's. Add new
// steps at the end.
//
// Outside the ledger, on every open: schema.sql (CREATE ... IF NOT EXISTS:
// creates what is missing, changes nothing that exists) and the format
// record (what this database now is; upgrade.go).
var migrations = []migration{
	// A schema change drops every derived table; the next update refills
	// them from raw. The id carries the version, so a bump is a new step.
	{id: fmt.Sprintf("schema-%d", schemaVersion), dropsDerived: true, run: resetDerived, due: schemaChanged},
	// Kept tables aren't dropped by a schema change; their new columns
	// are added in place.
	{id: "judgments-anchor", run: func(q dbtx) error {
		return addColumns(q, "judgments", "chunk_hash TEXT NOT NULL DEFAULT ''", "line INTEGER NOT NULL DEFAULT 0")
	}},
	// Provenance on thread edges (docs/incremental-design.md).
	{id: "thread-items-provenance", run: func(q dbtx) error {
		return addColumns(q, "thread_items", "version TEXT NOT NULL DEFAULT ''", "added_in TEXT NOT NULL DEFAULT ''", "added_at INTEGER NOT NULL DEFAULT 0",
			"retired_in TEXT NOT NULL DEFAULT ''", "retired_at INTEGER NOT NULL DEFAULT 0", "retired_why TEXT NOT NULL DEFAULT ''")
	}},
	{id: "topics-since-review", run: func(q dbtx) error {
		return addColumns(q, "topics", "since_review INTEGER NOT NULL DEFAULT 0")
	}},
	// Corrections made before this column had their threads mended then.
	{id: "corrections-related", run: func(q dbtx) error {
		return addColumns(q, "corrections", "related INTEGER NOT NULL DEFAULT 1")
	}},
	{id: "claim-relations-provenance", run: func(q dbtx) error {
		return addColumns(q, "claim_relations", "version TEXT NOT NULL DEFAULT ''", "made_in TEXT NOT NULL DEFAULT ''", "made_at INTEGER NOT NULL DEFAULT 0")
	}},
	{id: "statements-worth-labels", run: func(q dbtx) error {
		return addColumns(q, "statements", "worth REAL", "worth_version TEXT NOT NULL DEFAULT ''", "search_text TEXT NOT NULL DEFAULT ''",
			"status TEXT NOT NULL DEFAULT ''", "who TEXT NOT NULL DEFAULT ''", "evidence TEXT NOT NULL DEFAULT ''",
			"level INTEGER NOT NULL DEFAULT 0", "categories INTEGER NOT NULL DEFAULT 0", "label_version TEXT NOT NULL DEFAULT ''")
	}},
	// Ran on every open until the ledger: the app now marks a replaced
	// proposal itself (MarkAliasReplaced), and run again later it could
	// take an owner's quick reject-then-add for a replacement.
	{id: "aliases-replaced", afterSchema: true, run: markReplacedAliases},
	{id: "claim-centroids-dedup", afterSchema: true, run: upgradeClaimCentroids},
}

type migration struct {
	id string
	// afterSchema steps need the tables schema.sql creates; the others
	// come first because schema.sql's indexes may need their columns.
	afterSchema bool
	// dropsDerived: the step may empty derived tables (never kept ones).
	dropsDerived bool
	run          func(dbtx) error
	// due, if set, makes a recorded step pending again: the schema step
	// runs whenever the schema version differs, as before the ledger.
	due func(dbtx) (bool, error)
}

// dbtx is a *sql.DB, or the migration's transaction.
type dbtx interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

// connTx runs on one connection, inside the BEGIN IMMEDIATE it issued.
type connTx struct{ c *sql.Conn }

func (t connTx) Exec(q string, a ...any) (sql.Result, error) {
	return t.c.ExecContext(context.Background(), q, a...)
}
func (t connTx) Query(q string, a ...any) (*sql.Rows, error) {
	return t.c.QueryContext(context.Background(), q, a...)
}
func (t connTx) QueryRow(q string, a ...any) *sql.Row {
	return t.c.QueryRowContext(context.Background(), q, a...)
}

const ledgerSchema = `CREATE TABLE IF NOT EXISTS migrations (
	id         TEXT PRIMARY KEY,
	applied_at INTEGER NOT NULL,
	build      TEXT NOT NULL
)`

// Build names this build in the ledger: the release version where main
// sets it, else the commit it was built from.
var Build = buildName()

func buildName() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}

// appliedMigrations is the ledger's ids; none when it doesn't exist yet.
func appliedMigrations(q dbtx) (map[string]bool, error) {
	done := map[string]bool{}
	var n int
	if err := q.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'migrations'`).Scan(&n); err != nil || n == 0 {
		return done, err
	}
	rows, err := q.Query(`SELECT id FROM migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		done[id] = true
	}
	return done, rows.Err()
}

func pendingMigrations(q dbtx) ([]migration, error) {
	done, err := appliedMigrations(q)
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, m := range migrations {
		again := false
		if done[m.id] && m.due != nil {
			if again, err = m.due(q); err != nil {
				return nil, err
			}
		}
		if !done[m.id] || again {
			out = append(out, m)
		}
	}
	return out, nil
}

// Counts are row counts of the tables an upgrade or a redo must not lose
// (items, statements, thread items and the kept tables); a table that
// doesn't exist is absent. Aliases count the decided ones: proposals are
// the corpus's, dropped and made again.
type Counts map[string]int64

var countedTables = []string{"items", "statements", "thread_items", "forgotten", "audit", "judgments", "corrections", "learned_aliases", "thread_edits", "owner_links"}

func countRows(q dbtx) (Counts, error) {
	out := Counts{}
	for _, t := range countedTables {
		var n int64
		if err := q.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, t).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		where := ""
		if t == "learned_aliases" {
			where = ` WHERE status != 'proposed'`
		}
		if err := q.QueryRow(`SELECT count(*) FROM "` + t + `"` + where).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

// Counts are the database's row counts now.
func (s *Store) Counts() (Counts, error) { return countRows(s.db) }

func (c Counts) String() string {
	var parts []string
	for _, t := range countedTables {
		if n, ok := c[t]; ok {
			parts = append(parts, fmt.Sprintf("%s %d", t, n))
		}
	}
	return strings.Join(parts, ", ")
}

// Change says how each count moved, "table before → after", for the
// tables that changed ("" when none did).
func (c Counts) Change(after Counts) string {
	var parts []string
	for _, t := range countedTables {
		if c[t] != after[t] {
			parts = append(parts, fmt.Sprintf("%s %d → %d", t, c[t], after[t]))
		}
	}
	return strings.Join(parts, ", ")
}

// lost lists the counted tables that went down, kept ones always, the
// derived ones (items, statements) unless derived is true: a schema
// reset empties them on purpose. thread_items is compared, not held.
func (c Counts) lost(after Counts, derived bool) []string {
	var out []string
	for _, t := range countedTables {
		if after[t] >= c[t] {
			continue
		}
		if kept[t] || (!derived && (t == "items" || t == "statements")) {
			out = append(out, fmt.Sprintf("%s %d → %d", t, c[t], after[t]))
		}
	}
	return out
}

// migrateDB brings the database up to this build: pending ledger steps,
// schema.sql and the format record, in one transaction that rolls back if
// a count check fails. backup names the copy made before, for the error.
// It returns the steps applied and the counts before and after.
func migrateDB(db *sql.DB, backup string) (applied []string, before, after Counts, err error) {
	if _, err := db.Exec(ledgerSchema); err != nil {
		return nil, nil, nil, err
	}
	pending, err := pendingMigrations(db)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(pending) == 0 {
		// Nothing to upgrade: create what is missing, as every open did.
		if _, err := db.Exec(schema); err != nil {
			return nil, nil, nil, fmt.Errorf("schema: %w", err)
		}
		return nil, nil, nil, recordFormat(db)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer conn.Close()
	// Dropping a table with foreign keys on deletes its rows first (and
	// cascades); a pragma that can't change inside a transaction.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return nil, nil, nil, err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	// IMMEDIATE: another process opening the same database waits here and
	// then finds the steps applied.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, nil, nil, err
	}
	q := connTx{conn}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	if pending, err = pendingMigrations(q); err != nil {
		return nil, nil, nil, err
	}
	if before, err = countRows(q); err != nil {
		return nil, nil, nil, err
	}
	now := time.Now().Unix()
	derived := false
	runStep := func(m migration) error {
		if err := m.run(q); err != nil {
			return fmt.Errorf("migration %s: %w", m.id, err)
		}
		if _, err := q.Exec(`INSERT OR REPLACE INTO migrations(id, applied_at, build) VALUES (?, ?, ?)`, m.id, now, Build); err != nil {
			return err
		}
		applied = append(applied, m.id)
		derived = derived || m.dropsDerived
		return nil
	}
	for _, m := range pending {
		if !m.afterSchema {
			if err := runStep(m); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if _, err := q.Exec(schema); err != nil {
		return nil, nil, nil, fmt.Errorf("schema: %w", err)
	}
	for _, m := range pending {
		if m.afterSchema {
			if err := runStep(m); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if err := recordFormat(q); err != nil {
		return nil, nil, nil, err
	}
	if after, err = countRows(q); err != nil {
		return nil, nil, nil, err
	}
	if lost := before.lost(after, derived); len(lost) > 0 {
		where := "no copy was made ([store] backup_before_upgrade = false)"
		if backup != "" {
			where = "the copy made before it is " + backup
		}
		return nil, nil, nil, fmt.Errorf("upgrade stopped, the database is unchanged: it would have lost rows (%s; steps %s); %s",
			strings.Join(lost, ", "), strings.Join(applied, ", "), where)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, nil, nil, err
	}
	committed = true
	return applied, before, after, nil
}

func schemaChanged(q dbtx) (bool, error) {
	var v int
	err := q.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v != schemaVersion, err
}

// resetDerived drops every derived table when the schema version changed
// (or was never set on an existing database); kept tables and the ledger
// stay.
func resetDerived(q dbtx) error {
	if changed, err := schemaChanged(q); err != nil || !changed {
		return err
	}
	rows, err := q.Query(`SELECT type, name FROM sqlite_master WHERE type IN ('table', 'trigger') AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'chunks_fts_%'`)
	if err != nil {
		return err
	}
	var drop [][2]string
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			rows.Close()
			return err
		}
		if !kept[name] && name != "migrations" {
			drop = append(drop, [2]string{typ, name})
		}
	}
	rows.Close()
	for _, d := range drop {
		if _, err := q.Exec(fmt.Sprintf(`DROP %s IF EXISTS "%s"`, strings.ToUpper(d[0]), d[1])); err != nil {
			return err
		}
	}
	_, err = q.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}
