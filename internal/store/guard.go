package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// KeptGuard holds a copy of the kept tables (the owner's word) taken
// before a long run that rewrites derived data (a redo, a rethread), which
// can't be one transaction. Finish compares the counts and puts back the
// rows a kept table lost.
type KeptGuard struct {
	s      *Store
	path   string
	Before Counts
}

// GuardKept copies the kept tables beside the database and counts rows.
func (s *Store) GuardKept() (*KeptGuard, error) {
	g := &KeptGuard{s: s, path: s.path + ".kept-" + time.Now().Format("20060102-150405")}
	var err error
	if g.Before, err = s.Counts(); err != nil {
		return nil, err
	}
	os.Remove(g.path)
	err = g.attached(func(q connTx) error {
		for _, t := range keptTables(g.Before) {
			// rowid too: rows are put back as they were, once.
			if _, err := q.Exec(`CREATE TABLE kg."` + t + `" AS SELECT rowid AS kg_rowid, * FROM main."` + t + `"`); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		os.Remove(g.path)
		return nil, fmt.Errorf("copying the kept tables: %w", err)
	}
	return g, nil
}

// keptTables are the kept tables this database has, in a fixed order.
func keptTables(c Counts) []string {
	var out []string
	for _, t := range countedTables {
		if _, ok := c[t]; ok && kept[t] {
			out = append(out, t)
		}
	}
	return out
}

func (g *KeptGuard) attached(f func(connTx) error) error {
	ctx := context.Background()
	conn, err := g.s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS kg`, g.path); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `DETACH DATABASE kg`)
	return f(connTx{conn})
}

// Finish counts again. If a kept table lost rows, they are put back from
// the copy, the copy is kept, and the error says what happened; otherwise
// the copy is removed. after is the counts once Finish is done.
func (g *KeptGuard) Finish() (after Counts, err error) {
	if after, err = g.s.Counts(); err != nil {
		return nil, err
	}
	lost := g.Before.lost(after, true)
	if len(lost) == 0 {
		os.Remove(g.path)
		return after, nil
	}
	err = g.attached(func(q connTx) error {
		if _, err := q.Exec(`BEGIN IMMEDIATE`); err != nil {
			return err
		}
		for _, t := range keptTables(g.Before) {
			if after[t] >= g.Before[t] {
				continue
			}
			cols, err := columns(q, "main", t)
			if err != nil {
				q.Exec(`ROLLBACK`)
				return err
			}
			// Proposed aliases aren't counted: the corpus's, not the owner's.
			where := ""
			if t == "learned_aliases" {
				where = ` WHERE status != 'proposed'`
			}
			list := `"` + strings.Join(cols, `", "`) + `"`
			if _, err := q.Exec(`INSERT OR IGNORE INTO main."` + t + `"(rowid, ` + list + `) SELECT kg_rowid, ` + list + ` FROM kg."` + t + `"` + where); err != nil {
				q.Exec(`ROLLBACK`)
				return err
			}
		}
		_, err := q.Exec(`COMMIT`)
		return err
	})
	if err != nil {
		return after, fmt.Errorf("kept tables lost rows (%s), and putting them back failed: %w; they are in %s", strings.Join(lost, ", "), err, g.path)
	}
	if after, err = g.s.Counts(); err != nil {
		return nil, err
	}
	return after, fmt.Errorf("kept tables lost rows during the run (%s); they were put back from the copy taken before it (%s). If you removed some yourself meanwhile (a cleared judgment), remove them again", strings.Join(lost, ", "), g.path)
}

func columns(q dbtx, schemaName, table string) ([]string, error) {
	rows, err := q.Query(`SELECT name FROM pragma_table_info(?, ?)`, table, schemaName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
