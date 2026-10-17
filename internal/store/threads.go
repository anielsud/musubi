package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// ThreadRow is a thread as the threader keeps it: a topics row with the
// statements that state its current position, oldest first.
type ThreadRow struct {
	ID         int64
	Name       string
	Scope      string
	LabelLevel sensitivity.Level
	Current    []int64
}

// Threads lists every thread with its current statements. Unscoped: it
// feeds the threader, which writes thread names from the statements it was
// given, and labels them as sensitive as those (label_level).
func (s *Store) Threads() ([]ThreadRow, error) {
	rows, err := s.db.Query(`SELECT t.id, t.label, t.summary, t.label_level, coalesce(ti.statement_id, 0)
		FROM topics t LEFT JOIN thread_items ti ON ti.topic_id = t.id AND ti.current = 1
		LEFT JOIN statements st ON st.id = ti.statement_id
		ORDER BY t.id, st.said_at, st.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadRow
	for rows.Next() {
		var r ThreadRow
		var lvl int
		var sid int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Scope, &lvl, &sid); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != r.ID {
			r.LabelLevel = sensitivity.Level(lvl)
			out = append(out, r)
		}
		if sid != 0 {
			out[len(out)-1].Current = append(out[len(out)-1].Current, sid)
		}
	}
	return out, rows.Err()
}

// ClearThreads removes every thread, its membership and the relations
// between claims, for rebuilding under new rules. In thread mode those
// relations come only from threading.
func (s *Store) ClearThreads() error {
	return s.tx(func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM thread_items`, `DELETE FROM thread_runs`, `UPDATE claims SET topic_id = NULL`,
			`DELETE FROM topic_relations`, `DELETE FROM topic_entities`, `DELETE FROM topic_events`, `DELETE FROM topics`, `DELETE FROM claim_relations`} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
}

// NewThread creates a thread and returns its id.
func (s *Store) NewThread(name, scope string, level sensitivity.Level) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO topics(label, summary, label_level, centroid, size) VALUES (?,?,?,NULL,0)`, name, scope, int(level))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddToThread puts statements in a thread (current until reconciled) and
// raises the thread's label level to theirs: its name was written from them.
func (s *Store) AddToThread(topic int64, sids []int64, level sensitivity.Level) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, id := range sids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_items(topic_id, statement_id, current) VALUES (?,?,1)`, topic, id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`UPDATE topics SET label_level = max(label_level, ?),
			size = (SELECT count(*) FROM thread_items WHERE topic_id = ?) WHERE id = ?`, int(level), topic, topic)
		return err
	})
}

// SetThreadCurrent marks which of a thread's statements state its current
// position.
func (s *Store) SetThreadCurrent(topic int64, current []int64) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE thread_items SET current = 0 WHERE topic_id = ?`, topic); err != nil {
			return err
		}
		for _, id := range current {
			if _, err := tx.Exec(`UPDATE thread_items SET current = 1 WHERE topic_id = ? AND statement_id = ?`, topic, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// RelateStatements records that a later statement bears on an earlier one,
// as a relation between their claims.
func (s *Store) RelateStatements(later, earlier int64, rel string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO claim_relations(from_claim, to_claim, rel, statement_id)
		SELECT a.claim_id, b.claim_id, ?, ? FROM statement_claims a, statement_claims b
		WHERE a.statement_id = ? AND b.statement_id = ? AND a.claim_id != b.claim_id`, rel, later, later, earlier)
	return err
}

// PlaceClaimsInThreads sets each claim's topic to the first thread its
// first statement joined, for views that show one topic per claim.
func (s *Store) PlaceClaimsInThreads() error {
	_, err := s.db.Exec(`UPDATE claims SET topic_id = (
		SELECT ti.topic_id FROM statement_claims sc JOIN thread_items ti ON ti.statement_id = sc.statement_id
		JOIN statements st ON st.id = sc.statement_id
		WHERE sc.claim_id = claims.id ORDER BY st.said_at, st.id, ti.rowid LIMIT 1)`)
	return err
}

// ThreadedItems returns the rules stamp each threaded item was done under.
func (s *Store) ThreadedItems() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT item_id, stamp FROM thread_runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		rows.Scan(&id, &st)
		out[id] = st
	}
	return out, rows.Err()
}

// MarkThreaded records that an item's statements have been threaded.
func (s *Store) MarkThreaded(itemID, stamp string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO thread_runs(item_id, stamp) VALUES (?,?)`, itemID, stamp)
	return err
}

// ThreadMember is a statement's place in a thread.
type ThreadMember struct {
	Topic, Statement, Claim int64
	Current                 bool
	SaidAt                  time.Time
}

// ThreadMembers lists every thread membership, for the card index.
// Unscoped like CardRows: an index, never shown.
func (s *Store) ThreadMembers() ([]ThreadMember, error) {
	rows, err := s.db.Query(`SELECT ti.topic_id, ti.statement_id, sc.claim_id, ti.current, st.said_at
		FROM thread_items ti JOIN statement_claims sc ON sc.statement_id = ti.statement_id
		JOIN statements st ON st.id = ti.statement_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadMember
	for rows.Next() {
		var m ThreadMember
		var cur int
		var said int64
		rows.Scan(&m.Topic, &m.Statement, &m.Claim, &cur, &said)
		m.Current, m.SaidAt = cur == 1, time.Unix(said, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ItemStatement is a linked statement as the threader shows it.
type ItemStatement struct {
	ID         int64
	Kind       string
	Status     string
	Who        []string
	Holder     string
	Text       string
	SaidAt     time.Time
	Level      sensitivity.Level
	Categories sensitivity.Categories
}

// LinkedItemStatements returns an item's linked statements in the order
// they were said, with their own sensitivity combined with their passage's.
func (s *Store) LinkedItemStatements(itemID string) ([]ItemStatement, error) {
	rows, err := s.db.Query(`SELECT st.id, st.kind, st.status, st.who, st.holder, st.text, st.said_at,
		max(st.level, coalesce(l.level, 0), coalesce(il.level, 0)), st.categories | coalesce(l.categories, 0) | coalesce(il.categories, 0)
		FROM statements st JOIN statement_claims sc ON sc.statement_id = st.id
		JOIN chunks c ON c.id = st.chunk_id
		LEFT JOIN labels l ON l.item_id = st.item_id AND l.chunk_seq = c.seq
		LEFT JOIN labels il ON il.item_id = st.item_id AND il.chunk_seq = -1
		WHERE st.item_id = ? ORDER BY st.said_at, st.id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemStatement
	for rows.Next() {
		var x ItemStatement
		var who string
		var said int64
		var lvl, cats int
		if err := rows.Scan(&x.ID, &x.Kind, &x.Status, &who, &x.Holder, &x.Text, &said, &lvl, &cats); err != nil {
			return nil, err
		}
		x.SaidAt, x.Level, x.Categories = time.Unix(said, 0), sensitivity.Level(lvl), sensitivity.Categories(cats)
		if who != "" {
			json.Unmarshal([]byte(who), &x.Who)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ThreadLinks is what LinkThreads derives for threads: centroids, sizes,
// entities and relations, which threading itself doesn't write.
type ThreadLinks struct {
	Centroids map[int64][]float32
	Sizes     map[int64]int
	Entities  map[int64]map[string]int
	Relations []TopicRelation
}

// SetThreadLinks replaces the threads' centroids, entities and relations.
func (s *Store) SetThreadLinks(l ThreadLinks) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM topic_relations`, `DELETE FROM topic_entities`} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		for id, n := range l.Sizes {
			var b []byte
			if c := l.Centroids[id]; c != nil {
				b, _ = sqlite_vec.SerializeFloat32(c)
			}
			if _, err := tx.Exec(`UPDATE topics SET centroid = ?, size = ? WHERE id = ?`, b, n, id); err != nil {
				return err
			}
		}
		for _, r := range l.Relations {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO topic_relations(a, b, sim, shared, entities) VALUES (?,?,?,?,?)`,
				r.A, r.B, r.Sim, r.Shared, strings.Join(r.Entities, ",")); err != nil {
				return err
			}
		}
		for tid, es := range l.Entities {
			for e, n := range es {
				if _, err := tx.Exec(`INSERT INTO topic_entities(topic_id, entity_id, claims) VALUES (?,?,?)`, tid, e, n); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// DeleteThread removes a thread that holds no statements yet (one merged
// into another before its statements were stored).
func (s *Store) DeleteThread(id int64) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM thread_items WHERE topic_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM topics WHERE id = ?`, id)
		return err
	})
}
