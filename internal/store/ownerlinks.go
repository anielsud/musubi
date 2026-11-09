package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// Owner links: the owner's word on to-dos (owner_links in schema.sql). A
// link relates two statements; a mark is about one. Both name statements
// by key, so they apply again after a re-extraction or a correction.

// OwnerLinkRels are the links (statement → other) and marks (statement
// alone) the owner can make; who says whose it is (the note: names,
// comma-separated; empty for not the owner's).
var OwnerLinkRels = map[string]bool{"same": true, "replaces": true, "part": true, "done": true, "dropped": true, "not": true, "due": true, "who": true}

// OwnerLink is one recorded link or mark.
type OwnerLink struct {
	ID        int64        `json:"id"`
	Statement StatementKey `json:"statement"`
	Rel       string       `json:"rel"`
	Other     StatementKey `json:"other"`
	Note      string       `json:"note,omitempty"`
	By        string       `json:"by"`
	At        time.Time    `json:"at"`
	Undone    bool         `json:"undone,omitempty"`
}

// LinkStatements records the owner's link from one statement to another
// (other 0 for a mark) and returns its id. A negative id names a meeting
// summary's to-do that no statement matches (-meeting_todos.id): it is
// keyed by its meeting and text as a statement is.
func (s *Store) LinkStatements(id int64, rel string, other int64, note, by string) (int64, error) {
	if !OwnerLinkRels[rel] {
		return 0, fmt.Errorf("unknown link %q", rel)
	}
	mark := rel == "done" || rel == "dropped" || rel == "not" || rel == "due" || rel == "who"
	if mark != (other == 0) || id == other {
		return 0, fmt.Errorf("%s needs %s", rel, map[bool]string{true: "no other statement", false: "another statement"}[mark])
	}
	ids := []int64{id}
	if other != 0 {
		ids = append(ids, other)
	}
	keys, err := s.toDoKeys(ids)
	if err != nil {
		return 0, err
	}
	if len(keys) != len(ids) {
		return 0, fmt.Errorf("no such statement")
	}
	var o StatementKey
	if other != 0 {
		o = keys[1]
	}
	res, err := s.db.Exec(`INSERT INTO owner_links(item_id, text, rel, other_item, other_text, note, by, at) VALUES (?,?,?,?,?,?,?,?)`,
		keys[0].Item, keys[0].Text, rel, o.Item, o.Text, note, by, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// toDoKeys keys statements (positive ids) and unmatched summary to-dos
// (negative ids); those gone are left out.
func (s *Store) toDoKeys(ids []int64) ([]StatementKey, error) {
	var out []StatementKey
	for _, id := range ids {
		if id > 0 {
			ks, err := s.StatementKeys([]int64{id})
			if err != nil {
				return nil, err
			}
			out = append(out, ks...)
			continue
		}
		var k StatementKey
		err := s.db.QueryRow(`SELECT item_id, text FROM meeting_todos WHERE id = ?`, -id).Scan(&k.Item, &k.Text)
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

// OwnerLinks lists the links in force, oldest first (a later mark on the
// same statement overrides an earlier one).
func (s *Store) OwnerLinks() ([]OwnerLink, error) {
	rows, err := s.db.Query(`SELECT id, item_id, text, rel, other_item, other_text, note, by, at FROM owner_links WHERE undone = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwnerLink
	for rows.Next() {
		var l OwnerLink
		var at int64
		if err := rows.Scan(&l.ID, &l.Statement.Item, &l.Statement.Text, &l.Rel, &l.Other.Item, &l.Other.Text, &l.Note, &l.By, &at); err != nil {
			return nil, err
		}
		l.At = time.Unix(at, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// UndoOwnerLink records that a link was undone.
func (s *Store) UndoOwnerLink(id int64) error {
	res, err := s.db.Exec(`UPDATE owner_links SET undone = 1 WHERE id = ? AND undone = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no link %d to undo", id)
	}
	return nil
}

// ThreadLabels returns every thread's name and how sensitive the
// statements it was written from are (the caller checks it against the
// scope).
func (s *Store) ThreadLabels() (map[int64]TopicInfo, error) {
	rows, err := s.db.Query(`SELECT id, label, label_level FROM topics`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]TopicInfo{}
	for rows.Next() {
		var t TopicInfo
		var lvl int
		if err := rows.Scan(&t.ID, &t.Label, &lvl); err != nil {
			return nil, err
		}
		t.LabelLevel = sensitivity.Level(lvl)
		out[t.ID] = t
	}
	return out, rows.Err()
}
