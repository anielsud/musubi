package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Thread edits: the owner's merges and splits, kept to the side
// (thread_edits) and named by statement (item and text, as corrections
// find theirs), so they apply again after a rethread or a re-extraction.

// StatementKey names a statement across rebuilds: its item and its text as
// extracted (a corrected statement is found by its extracted text).
type StatementKey struct {
	Item string `json:"item"`
	Text string `json:"text"`
}

// ThreadEdit is one recorded merge or split.
type ThreadEdit struct {
	ID            int64
	Kind          string // merge, split
	Moved, Stay   []StatementKey
	Name, OldName string
	By            string
	At            time.Time
	Undone        bool
}

// StatementKeys returns the keys of statements by id.
func (s *Store) StatementKeys(ids []int64) ([]StatementKey, error) {
	var out []StatementKey
	for _, id := range ids {
		var k StatementKey
		// The extracted text when it was corrected, so the key outlives
		// the correction's re-application.
		err := s.db.QueryRow(`SELECT st.item_id, coalesce((SELECT c.extracted FROM corrections c WHERE c.statement = st.id AND c.field = 'text' ORDER BY c.id LIMIT 1), st.text)
			FROM statements st WHERE st.id = ?`, id).Scan(&k.Item, &k.Text)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// ResolveKeys finds the statements keys name now; those gone are left out.
func (s *Store) ResolveKeys(keys []StatementKey) ([]int64, error) {
	var out []int64
	for _, k := range keys {
		var id int64
		err := s.db.QueryRow(`SELECT id FROM statements WHERE item_id = ? AND text = ?
			UNION ALL SELECT st.id FROM corrections c JOIN statements st ON st.id = c.statement
			WHERE c.item_id = ? AND c.extracted = ? AND c.undone = 0 LIMIT 1`, k.Item, k.Text, k.Item, k.Text).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// RecordThreadEdit stores an edit and returns its id.
func (s *Store) RecordThreadEdit(e ThreadEdit) (int64, error) {
	moved, _ := json.Marshal(e.Moved)
	stay, _ := json.Marshal(e.Stay)
	res, err := s.db.Exec(`INSERT INTO thread_edits(kind, moved, stay, name, old_name, by, at) VALUES (?,?,?,?,?,?,?)`,
		e.Kind, string(moved), string(stay), e.Name, e.OldName, e.By, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ThreadEdits lists the edits, oldest first; undone ones only with all.
func (s *Store) ThreadEdits(all bool) ([]ThreadEdit, error) {
	q := `SELECT id, kind, moved, stay, name, old_name, by, at, undone FROM thread_edits`
	if !all {
		q += ` WHERE undone = 0`
	}
	rows, err := s.db.Query(q + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadEdit
	for rows.Next() {
		var e ThreadEdit
		var moved, stay string
		var at int64
		if err := rows.Scan(&e.ID, &e.Kind, &moved, &stay, &e.Name, &e.OldName, &e.By, &at, &e.Undone); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(moved), &e.Moved)
		json.Unmarshal([]byte(stay), &e.Stay)
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkThreadEditUndone records that an edit was undone.
func (s *Store) MarkThreadEditUndone(id int64) error {
	res, err := s.db.Exec(`UPDATE thread_edits SET undone = 1 WHERE id = ? AND undone = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no edit %d to undo", id)
	}
	return nil
}

// ThreadOf maps statements to the threads holding them (a statement is in
// one thread; the lowest id if in several).
func (s *Store) ThreadOf(ids []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	for _, id := range ids {
		var t int64
		err := s.db.QueryRow(`SELECT min(topic_id) FROM thread_items WHERE statement_id = ?`, id).Scan(&t)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if t != 0 {
			out[id] = t
		}
	}
	return out, nil
}

// MoveStatements moves statements' memberships from one thread to another
// (keeping whether each is current and where it came from), refreshes both
// sizes, and drops the status of each: both are written again.
func (s *Store) MoveStatements(from, to int64, ids []int64) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_items(topic_id, statement_id, current, version, added_in, added_at, retired_in, retired_at, retired_why)
				SELECT ?, statement_id, current, version, added_in, added_at, retired_in, retired_at, retired_why FROM thread_items WHERE topic_id = ? AND statement_id = ?`, to, from, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM thread_items WHERE topic_id = ? AND statement_id = ?`, from, id); err != nil {
				return err
			}
		}
		for _, t := range []int64{from, to} {
			if _, err := tx.Exec(`UPDATE topics SET size = (SELECT count(*) FROM thread_items WHERE topic_id = ?),
				label_level = coalesce((SELECT max(st.level) FROM thread_items ti JOIN statements st ON st.id = ti.statement_id WHERE ti.topic_id = ?), label_level)
				WHERE id = ?`, t, t, t); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM thread_status WHERE topic_id = ?`, t); err != nil {
				return err
			}
		}
		return nil
	})
}

// RenameThread sets a thread's name.
func (s *Store) RenameThread(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE topics SET label = ? WHERE id = ?`, name, id)
	return err
}

// ThreadStatementIDs lists a thread's statements.
func (s *Store) ThreadStatementIDs(id int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT statement_id FROM thread_items WHERE topic_id = ? ORDER BY statement_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var x int64
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ThreadName returns a thread's name and scope.
func (s *Store) ThreadName(id int64) (name, scope string, err error) {
	err = s.db.QueryRow(`SELECT label, summary FROM topics WHERE id = ?`, id).Scan(&name, &scope)
	return name, scope, err
}
