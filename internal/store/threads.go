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
		for _, q := range []string{`DELETE FROM thread_items`, `DELETE FROM thread_runs`, `DELETE FROM thread_status`, `DELETE FROM statement_relations`, `UPDATE claims SET topic_id = NULL`,
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

// Provenance says what made a thread change and when: the threading
// rules' version, the item being threaded, why an item stopped being
// current (reconcile, consolidate, cross, lapse), and the processing time.
// It is the base for invalidating locally and for "what was known when"
// (docs/incremental-design.md).
type Provenance struct {
	Version, During, Why string
	At                   time.Time
}

func prov(p []Provenance) Provenance {
	if len(p) > 0 {
		if p[0].At.IsZero() {
			p[0].At = time.Now()
		}
		return p[0]
	}
	return Provenance{At: time.Now()}
}

// AddToThread puts statements in a thread (current until reconciled) and
// raises the thread's label level to theirs: its name was written from them.
func (s *Store) AddToThread(topic int64, sids []int64, level sensitivity.Level, p ...Provenance) error {
	pv := prov(p)
	return s.tx(func(tx *sql.Tx) error {
		for _, id := range sids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_items(topic_id, statement_id, current, version, added_in, added_at) VALUES (?,?,1,?,?,?)`,
				topic, id, pv.Version, pv.During, pv.At.Unix()); err != nil {
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
// An item leaving the current set records when, during which item and
// why; one coming back clears that.
func (s *Store) SetThreadCurrent(topic int64, current []int64, p ...Provenance) error {
	pv := prov(p)
	keep := make(map[int64]bool, len(current))
	for _, id := range current {
		keep[id] = true
	}
	return s.tx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT statement_id, current FROM thread_items WHERE topic_id = ?`, topic)
		if err != nil {
			return err
		}
		type row struct {
			id  int64
			cur bool
		}
		var all []row
		for rows.Next() {
			var r row
			var c int
			rows.Scan(&r.id, &c)
			r.cur = c == 1
			all = append(all, r)
		}
		rows.Close()
		for _, r := range all {
			switch {
			case r.cur && !keep[r.id]:
				if _, err := tx.Exec(`UPDATE thread_items SET current = 0, retired_in = ?, retired_at = ?, retired_why = ? WHERE topic_id = ? AND statement_id = ?`,
					pv.During, pv.At.Unix(), pv.Why, topic, r.id); err != nil {
					return err
				}
			case !r.cur && keep[r.id]:
				if _, err := tx.Exec(`UPDATE thread_items SET current = 1, retired_in = '', retired_at = 0, retired_why = '' WHERE topic_id = ? AND statement_id = ?`, topic, r.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RelateStatements records that a later statement bears on an earlier one,
// as a relation between their claims.
func (s *Store) RelateStatements(later, earlier int64, rel string, p ...Provenance) error {
	pv := prov(p)
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO statement_relations(later, earlier, rel, version, made_in, made_at) VALUES (?,?,?,?,?,?)`,
		later, earlier, rel, pv.Version, pv.During, pv.At.Unix()); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO claim_relations(from_claim, to_claim, rel, statement_id, version, made_in, made_at)
		SELECT a.claim_id, b.claim_id, ?, ?, ?, ?, ? FROM statement_claims a, statement_claims b
		WHERE a.statement_id = ? AND b.statement_id = ? AND a.claim_id != b.claim_id`, rel, later, pv.Version, pv.During, pv.At.Unix(), later, earlier)
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

// TouchThread counts one more touch of a thread since its last review and
// returns the count.
func (s *Store) TouchThread(id int64) (int, error) {
	if _, err := s.db.Exec(`UPDATE topics SET since_review = since_review + 1 WHERE id = ?`, id); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRow(`SELECT since_review FROM topics WHERE id = ?`, id).Scan(&n)
	return n, err
}

// ReviewedThread records that a thread's current list was reviewed.
func (s *Store) ReviewedThread(id int64) error {
	_, err := s.db.Exec(`UPDATE topics SET since_review = 0 WHERE id = ?`, id)
	return err
}

// UnmarkThreaded records that an item is no longer threaded (it was taken
// out of its threads to be threaded again).
func (s *Store) UnmarkThreaded(itemID string) error {
	_, err := s.db.Exec(`DELETE FROM thread_runs WHERE item_id = ?`, itemID)
	return err
}

// ThreadedSince lists threaded items that happened after t, oldest first.
func (s *Store) ThreadedSince(t time.Time) ([]ItemTime, error) {
	rows, err := s.db.Query(`SELECT i.id, i.occurred_at FROM thread_runs r JOIN items i ON i.id = r.item_id WHERE i.occurred_at > ? ORDER BY i.occurred_at`, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemTime
	for rows.Next() {
		var x ItemTime
		var at int64
		if err := rows.Scan(&x.ID, &at); err != nil {
			return nil, err
		}
		x.At = time.Unix(at, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}

// ItemTime is an item and when it happened.
type ItemTime struct {
	ID string
	At time.Time
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
		if _, err := tx.Exec(`DELETE FROM thread_status WHERE topic_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM thread_items WHERE topic_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM topics WHERE id = ?`, id)
		return err
	})
}

// LatestThreaded is when the latest threaded item happened (zero if none).
func (s *Store) LatestThreaded() (time.Time, error) {
	var at sql.NullInt64
	err := s.db.QueryRow(`SELECT max(i.occurred_at) FROM thread_runs r JOIN items i ON i.id = r.item_id`).Scan(&at)
	if !at.Valid {
		return time.Time{}, err
	}
	return time.Unix(at.Int64, 0), err
}

// ThreadItemRow is a thread membership with its provenance.
type ThreadItemRow struct {
	Topic, Statement      int64
	Current               bool
	Version, AddedIn      string
	AddedAt               time.Time
	RetiredIn, RetiredWhy string
	RetiredAt             time.Time
}

// ThreadItemRows lists every membership with its provenance. Unscoped: for
// audits and the incremental engine, never shown.
func (s *Store) ThreadItemRows() ([]ThreadItemRow, error) {
	rows, err := s.db.Query(`SELECT topic_id, statement_id, current, version, added_in, added_at, retired_in, retired_at, retired_why FROM thread_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadItemRow
	for rows.Next() {
		var r ThreadItemRow
		var cur int
		var add, ret int64
		if err := rows.Scan(&r.Topic, &r.Statement, &cur, &r.Version, &r.AddedIn, &add, &r.RetiredIn, &ret, &r.RetiredWhy); err != nil {
			return nil, err
		}
		r.Current, r.AddedAt, r.RetiredAt = cur == 1, time.Unix(add, 0), time.Unix(ret, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RetiringRelations maps each statement to the statements that retire it
// (replaces, completes, answers or repeats it), from the claim relations.
// Unscoped: for deriving "current".
func (s *Store) RetiringRelations() (map[int64][]int64, error) {
	// Statement relations where threading wrote them; claim relations for
	// what was threaded before they existed.
	rows, err := s.db.Query(`SELECT earlier, later FROM statement_relations
		WHERE rel IN ('supersedes', 'completes', 'answers', 'repeats')
		UNION
		SELECT b.statement_id, r.statement_id FROM claim_relations r
		JOIN statement_claims b ON b.claim_id = r.to_claim
		WHERE r.rel IN ('supersedes', 'completes', 'answers', 'repeats') AND b.statement_id != r.statement_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var earlier, later int64
		rows.Scan(&earlier, &later)
		out[earlier] = append(out[earlier], later)
	}
	return out, rows.Err()
}

// StatementTimes returns each statement's time and kind.
func (s *Store) StatementTimes() (map[int64]time.Time, map[int64]string, error) {
	rows, err := s.db.Query(`SELECT id, said_at, kind FROM statements`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	at, kind := map[int64]time.Time{}, map[int64]string{}
	for rows.Next() {
		var id, t int64
		var k string
		rows.Scan(&id, &t, &k)
		at[id], kind[id] = time.Unix(t, 0), k
	}
	return at, kind, rows.Err()
}

// RemoveFromThreads takes statements out of the thread graph: their
// memberships, and every relation they are part of (as the later or the
// earlier). It returns how many statement relations went. Threads left
// without statements are kept (an insert may fill them again).
func (s *Store) RemoveFromThreads(sts []ItemStatement) (int, error) {
	n := 0
	err := s.tx(func(tx *sql.Tx) error {
		for _, x := range sts {
			if _, err := tx.Exec(`DELETE FROM thread_items WHERE statement_id = ?`, x.ID); err != nil {
				return err
			}
			res, err := tx.Exec(`DELETE FROM statement_relations WHERE later = ? OR earlier = ?`, x.ID, x.ID)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			n += int(k)
			if _, err := tx.Exec(`DELETE FROM claim_relations WHERE statement_id = ? OR to_claim IN (SELECT claim_id FROM statement_claims WHERE statement_id = ?)`, x.ID, x.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// ThreadsOfItem lists the threads holding an item's statements.
func (s *Store) ThreadsOfItem(itemID string) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT ti.topic_id FROM thread_items ti JOIN statements st ON st.id = ti.statement_id WHERE st.item_id = ?`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var t int64
		rows.Scan(&t)
		out[t] = true
	}
	return out, rows.Err()
}

// DeleteEmptyThreads removes those of the given threads that hold no
// statements.
func (s *Store) DeleteEmptyThreads(threads map[int64]bool) error {
	for t := range threads {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM thread_items WHERE topic_id = ?`, t).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if err := s.DeleteThread(t); err != nil {
				return err
			}
		}
	}
	return nil
}

// PendingCorrections lists the statements whose corrections haven't had
// the graph around them mended (a correction made while the model was
// unreachable), with who made them, oldest first.
func (s *Store) PendingCorrections() ([]PendingCorrection, error) {
	rows, err := s.db.Query(`SELECT c.statement, max(c.field = 'text'), min(c.by) FROM corrections c JOIN statements st ON st.id = c.statement
		WHERE c.related = 0 AND c.undone = 0 GROUP BY c.statement ORDER BY min(c.at)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingCorrection
	for rows.Next() {
		var p PendingCorrection
		if err := rows.Scan(&p.Statement, &p.Text, &p.By); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PendingCorrection is a statement with corrections still to mend around.
type PendingCorrection struct {
	Statement int64
	Text      bool // its text changed (re-embedded)
	By        string
}

// MarkCorrectionsRelated records that the graph around a statement's
// corrections was mended.
func (s *Store) MarkCorrectionsRelated(statement int64) error {
	_, err := s.db.Exec(`UPDATE corrections SET related = 1 WHERE statement = ? AND related = 0`, statement)
	return err
}

// CorrectStatement records the owner's correction of a statement (its
// text, and who it belongs to; empty text and nil who keep them) in
// corrections, shows it on the statement, and takes the statement out of
// the relations it was in: they were judged on the old text. The caller
// re-embeds it, judges it again, and derives "current".
func (s *Store) CorrectStatement(id int64, text string, who []string, by string) error {
	return s.tx(func(tx *sql.Tx) error {
		var item, oldText, oldWho string
		if err := tx.QueryRow(`SELECT item_id, text, who FROM statements WHERE id = ?`, id).Scan(&item, &oldText, &oldWho); err != nil {
			return err
		}
		// The text as extracted: the first correction's old text, if the
		// statement was corrected before.
		extracted := oldText
		tx.QueryRow(`SELECT old FROM corrections WHERE statement = ? AND field = 'text' AND undone = 0 ORDER BY id LIMIT 1`, id).Scan(&extracted)
		now := time.Now().Unix()
		if text != "" && text != oldText {
			if _, err := tx.Exec(`UPDATE statements SET text = ?, search_text = '' WHERE id = ?`, text, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO corrections(item_id, statement, extracted, field, old, new, by, at, related) VALUES (?,?,?,?,?,?,?,?,0)`,
				item, id, extracted, "text", oldText, text, by, now); err != nil {
				return err
			}
		}
		if who != nil {
			b, _ := json.Marshal(who)
			if string(b) != oldWho {
				if _, err := tx.Exec(`UPDATE statements SET who = ?, holder = ? WHERE id = ?`, string(b), strings.Join(who, ", "), id); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO corrections(item_id, statement, extracted, field, old, new, by, at, related) VALUES (?,?,?,?,?,?,?,?,0)`,
					item, id, extracted, "who", oldWho, string(b), by, now); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(`DELETE FROM statement_relations WHERE later = ? OR earlier = ?`, id, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM claim_relations WHERE statement_id = ? OR to_claim IN (SELECT claim_id FROM statement_claims WHERE statement_id = ?)`, id, id)
		return err
	})
}

// UndoCorrection puts a statement back as it was before a correction (the
// record stays, marked undone). The caller mends the graph as for a
// correction.
func (s *Store) UndoCorrection(id int64) (statement int64, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		var field, old string
		if err := tx.QueryRow(`SELECT statement, field, old FROM corrections WHERE id = ? AND undone = 0`, id).Scan(&statement, &field, &old); err != nil {
			return err
		}
		switch field {
		case "text":
			if _, err := tx.Exec(`UPDATE statements SET text = ?, search_text = '' WHERE id = ?`, old, statement); err != nil {
				return err
			}
		case "who":
			var who []string
			json.Unmarshal([]byte(old), &who)
			if _, err := tx.Exec(`UPDATE statements SET who = ?, holder = ? WHERE id = ?`, old, strings.Join(who, ", "), statement); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`UPDATE corrections SET undone = 1 WHERE id = ?`, id)
		return err
	})
	return statement, err
}

// reapplyCorrections applies an item's corrections to its statements again
// after they were replaced (re-extraction), finding each by the text it
// was extracted with. A correction whose statement is no longer extracted
// waits, kept.
func reapplyCorrections(tx *sql.Tx, itemID string) error {
	rows, err := tx.Query(`SELECT id, extracted, field, new FROM corrections WHERE item_id = ? AND undone = 0 ORDER BY id`, itemID)
	if err != nil {
		return err
	}
	type c struct {
		id                    int64
		extracted, field, new string
	}
	var cs []c
	for rows.Next() {
		var x c
		rows.Scan(&x.id, &x.extracted, &x.field, &x.new)
		cs = append(cs, x)
	}
	rows.Close()
	// Find every statement by its extracted text before changing any: a
	// statement's text correction would hide it from its who correction.
	sids := make([]int64, len(cs))
	for i, x := range cs {
		tx.QueryRow(`SELECT id FROM statements WHERE item_id = ? AND text = ? LIMIT 1`, itemID, x.extracted).Scan(&sids[i])
	}
	for i, x := range cs {
		sid := sids[i]
		if sid == 0 {
			continue
		}
		switch x.field {
		case "text":
			if _, err := tx.Exec(`UPDATE statements SET text = ?, search_text = '' WHERE id = ?`, x.new, sid); err != nil {
				return err
			}
		case "who":
			var who []string
			json.Unmarshal([]byte(x.new), &who)
			if _, err := tx.Exec(`UPDATE statements SET who = ?, holder = ? WHERE id = ?`, x.new, strings.Join(who, ", "), sid); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE corrections SET statement = ? WHERE id = ?`, sid, x.id); err != nil {
			return err
		}
	}
	return nil
}
