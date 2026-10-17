package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// ThreadStatement is a thread's statement as a status is written from it:
// the statement, whether it is current, the item it came from, and its
// effective label (its own, its passage's and its item's).
type ThreadStatement struct {
	ItemStatement
	Current   bool
	ItemID    string
	ItemTitle string
}

// ThreadStatements lists a thread's statements in the order they were
// said. Unscoped: it feeds the status writer, which labels what it writes
// as sensitive as what it read.
func (s *Store) ThreadStatements(topic int64) ([]ThreadStatement, error) {
	rows, err := s.db.Query(`SELECT st.id, st.kind, st.status, st.who, st.holder, st.text, st.said_at,
		max(st.level, coalesce(l.level, 0), coalesce(il.level, 0)), st.categories | coalesce(l.categories, 0) | coalesce(il.categories, 0),
		ti.current, i.id, i.title
		FROM thread_items ti JOIN statements st ON st.id = ti.statement_id
		JOIN items i ON i.id = st.item_id
		JOIN chunks c ON c.id = st.chunk_id
		LEFT JOIN labels l ON l.item_id = st.item_id AND l.chunk_seq = c.seq
		LEFT JOIN labels il ON il.item_id = st.item_id AND il.chunk_seq = -1
		WHERE ti.topic_id = ? ORDER BY st.said_at, st.id`, topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadStatement
	for rows.Next() {
		var x ThreadStatement
		var who string
		var said int64
		var lvl, cats, cur int
		if err := rows.Scan(&x.ID, &x.Kind, &x.Status, &who, &x.Holder, &x.Text, &said, &lvl, &cats, &cur, &x.ItemID, &x.ItemTitle); err != nil {
			return nil, err
		}
		x.SaidAt, x.Level, x.Categories, x.Current = time.Unix(said, 0), sensitivity.Level(lvl), sensitivity.Categories(cats), cur == 1
		if who != "" {
			json.Unmarshal([]byte(who), &x.Who)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ThreadStatusRow is a stored status.
type ThreadStatusRow struct {
	Topic      int64
	Variant    string
	Version    string
	InputHash  string
	Body       string
	Level      sensitivity.Level
	Categories sensitivity.Categories
	AsOf       time.Time
}

// ThreadStatusStamps returns each stored status's version and input hash,
// by topic and variant, to decide which to write again.
func (s *Store) ThreadStatusStamps() (map[int64]map[string]string, error) {
	rows, err := s.db.Query(`SELECT topic_id, variant, version || '/' || input_hash FROM thread_status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]string{}
	for rows.Next() {
		var t int64
		var v, h string
		rows.Scan(&t, &v, &h)
		if out[t] == nil {
			out[t] = map[string]string{}
		}
		out[t][v] = h
	}
	return out, rows.Err()
}

// SetThreadStatus stores a status; an empty body removes the variant.
func (s *Store) SetThreadStatus(r ThreadStatusRow) error {
	if r.Body == "" {
		_, err := s.db.Exec(`DELETE FROM thread_status WHERE topic_id = ? AND variant = ?`, r.Topic, r.Variant)
		return err
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO thread_status(topic_id, variant, version, input_hash, body, level, categories, as_of)
		VALUES (?,?,?,?,?,?,?,?)`, r.Topic, r.Variant, r.Version, r.InputHash, r.Body, int(r.Level), int(r.Categories), r.AsOf.Unix())
	return err
}

// ThreadStatuses returns, for each thread, the fullest status scope sc may
// see: "full" when its label is visible, else "open" when that is, else
// none.
func (s *Store) ThreadStatuses(sc sensitivity.Scope) (map[int64]ThreadStatusRow, error) {
	rows, err := s.db.Query(`SELECT topic_id, variant, version, input_hash, body, level, categories, as_of FROM thread_status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ThreadStatusRow{}
	for rows.Next() {
		var r ThreadStatusRow
		var lvl, cats int
		var at int64
		if err := rows.Scan(&r.Topic, &r.Variant, &r.Version, &r.InputHash, &r.Body, &lvl, &cats, &at); err != nil {
			return nil, err
		}
		r.Level, r.Categories, r.AsOf = sensitivity.Level(lvl), sensitivity.Categories(cats), time.Unix(at, 0)
		// A status mixes items from many meetings, so an audience scope sees
		// it only when every item was open to anyone.
		if !sensitivity.Visible(sensitivity.Label{Level: r.Level, Categories: r.Categories}, nil, sc) {
			continue
		}
		if old, ok := out[r.Topic]; ok && old.Variant == "full" {
			continue
		}
		out[r.Topic] = r
	}
	return out, rows.Err()
}

// ClearThreadStatus removes every status (statuses are rewritten after a
// rethread).
func (s *Store) ClearThreadStatus() error {
	return s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM thread_status`)
		return err
	})
}

// ThreadIndexRow is what finding a thread for a question needs: its name,
// scope line and centroid. Labels are checked by the caller against
// LabelLevel.
type ThreadIndexRow struct {
	ID         int64
	Name       string
	Scope      string
	LabelLevel sensitivity.Level
	Centroid   []float32
	Size       int
}

// ThreadIndex lists every thread that holds statements.
func (s *Store) ThreadIndex() ([]ThreadIndexRow, error) {
	rows, err := s.db.Query(`SELECT t.id, t.label, t.summary, t.label_level, t.centroid, t.size FROM topics t
		WHERE EXISTS (SELECT 1 FROM thread_items ti WHERE ti.topic_id = t.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadIndexRow
	for rows.Next() {
		var r ThreadIndexRow
		var lvl int
		var c []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Scope, &lvl, &c, &r.Size); err != nil {
			return nil, err
		}
		r.LabelLevel, r.Centroid = sensitivity.Level(lvl), decodeVec(c)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ThreadMembership maps each statement to its threads (current or not).
// Unscoped: an index; reads go through VisibleThreadItems.
func (s *Store) ThreadMembership() (map[int64][]int64, error) {
	rows, err := s.db.Query(`SELECT statement_id, topic_id FROM thread_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var sid, tid int64
		rows.Scan(&sid, &tid)
		out[sid] = append(out[sid], tid)
	}
	return out, rows.Err()
}

// ThreadItem is a thread's statement as a scope sees it.
type ThreadItem struct {
	Statement
	Topic   int64 `json:"topic"`
	Current bool  `json:"current"`
}

// VisibleThreadItems returns the statements of the given threads that sc
// may see, in the order they were said.
func (s *Store) VisibleThreadItems(sc sensitivity.Scope, topics []int64) (map[int64][]ThreadItem, error) {
	out := map[int64][]ThreadItem{}
	if len(topics) == 0 {
		return out, nil
	}
	vis, vargs := statementVis(sc)
	args := make([]any, 0, len(topics)+len(vargs))
	for _, t := range topics {
		args = append(args, t)
	}
	args = append(args, vargs...)
	rows, err := s.db.Query(`SELECT ti.topic_id, ti.current, st.id, st.item_id, st.chunk_id, c.seq, st.line, st.kind, st.text, st.holder,
		st.said_at, st.quote, coalesce(sc.claim_id, 0), i.kind, CASE WHEN c.title != '' THEN c.title ELSE i.title END,
		`+stLevel+`, `+stCats+`, st.status, st.who, st.evidence
		FROM thread_items ti JOIN statements st ON st.id = ti.statement_id JOIN chunks c ON c.id = st.chunk_id
		JOIN items i ON i.id = st.item_id LEFT JOIN statement_claims sc ON sc.statement_id = st.id`+chunkVisJoin+`
		WHERE ti.topic_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(topics)), ",")+`) AND `+vis+` ORDER BY st.said_at, st.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x ThreadItem
		var said int64
		var lvl, cur int
		var cats int64
		var who, ev string
		if err := rows.Scan(&x.Topic, &cur, &x.ID, &x.ItemID, &x.ChunkID, &x.ChunkSeq, &x.Line, &x.Kind, &x.Text, &x.Holder,
			&said, &x.Quote, &x.ClaimID, &x.ItemKind, &x.ItemTitle, &lvl, &cats, &x.Status, &who, &ev); err != nil {
			return nil, err
		}
		x.SaidAt, x.Current = time.Unix(said, 0), cur == 1
		x.setPoint(who, ev, 0, 0)
		x.Label = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
		out[x.Topic] = append(out[x.Topic], x)
	}
	return out, rows.Err()
}
