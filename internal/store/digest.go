package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// Span is a stretch of a meeting: from one line of one passage to another.
type Span struct {
	FromChunk int64 `json:"fromChunk"`
	FromLine  int   `json:"fromLine"`
	ToChunk   int64 `json:"toChunk"`
	ToLine    int   `json:"toLine"`
}

// DigestSection is one subject of a meeting's summary.
type DigestSection struct {
	N        int      `json:"n"`
	Subject  string   `json:"subject"`
	Text     string   `json:"text"`
	People   []string `json:"people,omitempty"`
	Spans    []Span   `json:"spans,omitempty"`
	Grounded bool     `json:"grounded"`
	Issues   string   `json:"issues,omitempty"`
	// Own is the section's own sensitivity, worked out from the passages it
	// cites, on top of the item's.
	Own sensitivity.Label `json:"-"`
}

// DigestTodo is one thing said in a meeting would or should be done, as the
// whole-meeting read found it.
type DigestTodo struct {
	ID        int64      `json:"id"`
	N         int        `json:"n"`
	Text      string     `json:"text"`
	Owners    []string   `json:"owners,omitempty"`
	Requester string     `json:"requester,omitempty"`
	Agreed    string     `json:"agreed"` // agreed, asked, floated, declined
	DueText   string     `json:"dueText,omitempty"`
	DueDate   string     `json:"dueDate,omitempty"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	SaidAt    time.Time  `json:"saidAt"`
	// StatementID is the statement that says the same thing, 0 for none;
	// LinkScore how well it matches.
	StatementID int64   `json:"statementId,omitempty"`
	LinkScore   float64 `json:"linkScore,omitempty"`
	Grounded    bool    `json:"grounded"`
	Issues      string  `json:"issues,omitempty"`
	// ItemID and ItemTitle are set on reads.
	ItemID    string            `json:"itemId,omitempty"`
	ItemTitle string            `json:"itemTitle,omitempty"`
	Own       sensitivity.Label `json:"-"`
}

// Digest is a meeting's overview, sections and to-dos.
type Digest struct {
	ItemID    string          `json:"itemId"`
	ItemTitle string          `json:"itemTitle,omitempty"`
	Start     time.Time       `json:"start"`
	Version   string          `json:"version,omitempty"`
	Overview  string          `json:"overview"`
	MadeAt    time.Time       `json:"madeAt"`
	Sections  []DigestSection `json:"sections"`
	Todos     []DigestTodo    `json:"todos"`
	// OverviewOwn is the overview's own sensitivity (as sensitive as the
	// most sensitive section), on top of the item's.
	OverviewOwn sensitivity.Label `json:"-"`
}

// DigestLink says which statement a to-do matched.
type DigestLink struct {
	Todo      int64
	Statement int64
	Score     float64
}

// ReplaceDigest swaps an item's digest. version and hash stamp the stage;
// input is the hash of what it read, without the version or model.
func (s *Store) ReplaceDigest(itemID, version, hash, input string, d Digest) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, t := range []string{"meeting_todos", "meeting_sections", "meeting_digests"} {
			if _, err := tx.Exec(`DELETE FROM `+t+` WHERE item_id = ?`, itemID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO meeting_digests(item_id, version, overview, level, categories, made_at) VALUES (?,?,?,?,?,?)`,
			itemID, version, d.Overview, int(d.OverviewOwn.Level), int64(d.OverviewOwn.Categories), time.Now().Unix()); err != nil {
			return err
		}
		for _, x := range d.Sections {
			if _, err := tx.Exec(`INSERT INTO meeting_sections(item_id, n, subject, text, people, spans, level, categories, grounded, issues) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				itemID, x.N, x.Subject, x.Text, jsonOrEmpty(x.People, len(x.People) == 0), jsonOrEmpty(x.Spans, len(x.Spans) == 0),
				int(x.Own.Level), int64(x.Own.Categories), x.Grounded, x.Issues); err != nil {
				return err
			}
		}
		for _, x := range d.Todos {
			if _, err := tx.Exec(`INSERT INTO meeting_todos(item_id, n, text, owners, requester, agreed, due_text, due_date, evidence, said_at, statement_id, link_score, level, categories, grounded, issues)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				itemID, x.N, x.Text, jsonOrEmpty(x.Owners, len(x.Owners) == 0), x.Requester, x.Agreed, x.DueText, x.DueDate,
				jsonOrEmpty(x.Evidence, len(x.Evidence) == 0), x.SaidAt.Unix(), nullID(x.StatementID), x.LinkScore,
				int(x.Own.Level), int64(x.Own.Categories), x.Grounded, x.Issues); err != nil {
				return err
			}
		}
		if err := stamp(tx, "digest", itemID, version, hash); err != nil {
			return err
		}
		return stamp(tx, "digest-input", itemID, "in", input)
	})
}

// RestampDigest re-stamps an item's digest without changing it, for when
// the way the stamp is worked out changes but what it read has not.
func (s *Store) RestampDigest(itemID, version, hash, input string) error {
	return s.tx(func(tx *sql.Tx) error {
		if err := stamp(tx, "digest", itemID, version, hash); err != nil {
			return err
		}
		return stamp(tx, "digest-input", itemID, "in", input)
	})
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// SetDigestLinks records which statement each of an item's to-dos matched
// (none for those not listed) and stamps the link step, which is redone,
// without a model, when the item's statements change.
func (s *Store) SetDigestLinks(itemID, hash string, links []DigestLink) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE meeting_todos SET statement_id = NULL, link_score = 0 WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		for _, l := range links {
			if _, err := tx.Exec(`UPDATE meeting_todos SET statement_id = ?, link_score = ? WHERE id = ? AND item_id = ?`,
				l.Statement, l.Score, l.Todo, itemID); err != nil {
				return err
			}
		}
		return stamp(tx, "digest-link", itemID, "l", hash)
	})
}

// ItemStatements returns an item's statements in the order they were said
// (pipeline side, unscoped).
func (s *Store) ItemStatements(itemID string) ([]Statement, error) {
	return s.statements(`WHERE st.item_id = ? ORDER BY st.line, st.id`, itemID)
}

// DigestTodos returns an item's to-dos, unscoped (pipeline side).
func (s *Store) DigestTodos(itemID string) ([]DigestTodo, error) {
	return s.scanDigestTodos(`WHERE x.item_id = ? ORDER BY x.n`, []any{itemID})
}

const digestTodoCols = `x.id, x.item_id, i.title, x.n, x.text, x.owners, x.requester, x.agreed, x.due_text, x.due_date, x.evidence, x.said_at,
	coalesce(x.statement_id, 0), x.link_score, x.level, x.categories, x.grounded, x.issues`

func (s *Store) scanDigestTodos(where string, args []any) ([]DigestTodo, error) {
	rows, err := s.db.Query(`SELECT `+digestTodoCols+` FROM meeting_todos x JOIN items i ON i.id = x.item_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DigestTodo
	for rows.Next() {
		var t DigestTodo
		var owners, ev string
		var said int64
		var lvl int
		var cats int64
		if err := rows.Scan(&t.ID, &t.ItemID, &t.ItemTitle, &t.N, &t.Text, &owners, &t.Requester, &t.Agreed, &t.DueText, &t.DueDate, &ev, &said,
			&t.StatementID, &t.LinkScore, &lvl, &cats, &t.Grounded, &t.Issues); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(owners), &t.Owners)
		json.Unmarshal([]byte(ev), &t.Evidence)
		t.SaidAt = time.Unix(said, 0)
		t.Own = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
		out = append(out, t)
	}
	return out, rows.Err()
}

// digestVis is the read rule for digest rows (alias x): as sensitive as the
// row's own label and the item's, failing closed like statements do.
func digestVis(sc sensitivity.Scope) (string, []any) {
	return visExpr(sc, "max(il.level, x.level)", "(il.categories | x.categories)", "x.item_id")
}

const digestVisJoin = ` JOIN labels il ON il.item_id = x.item_id AND il.chunk_seq = -1 `

// MeetingDigest returns what the scope may see of a meeting's digest: nil
// when the meeting has none, or the scope can't see even its overview.
// Sections and to-dos the scope can't see are left out, and names follow
// the owner's spelling rules.
func (s *Store) MeetingDigest(sc sensitivity.Scope, itemID string) (*Digest, error) {
	vis, vargs := digestVis(sc)
	d := &Digest{ItemID: itemID}
	var made, start int64
	err := s.db.QueryRow(`SELECT i.title, i.occurred_at, x.version, x.overview, x.made_at FROM meeting_digests x JOIN items i ON i.id = x.item_id`+digestVisJoin+
		`WHERE x.item_id = ? AND `+vis, append([]any{itemID}, vargs...)...).Scan(&d.ItemTitle, &start, &d.Version, &d.Overview, &made)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.MadeAt, d.Start = time.Unix(made, 0), time.Unix(start, 0)
	rows, err := s.db.Query(`SELECT x.n, x.subject, x.text, x.people, x.spans, x.grounded, x.issues FROM meeting_sections x`+digestVisJoin+
		`WHERE x.item_id = ? AND `+vis+` ORDER BY x.n`, append([]any{itemID}, vargs...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x DigestSection
		var people, spans string
		if err := rows.Scan(&x.N, &x.Subject, &x.Text, &people, &spans, &x.Grounded, &x.Issues); err != nil {
			rows.Close()
			return nil, err
		}
		json.Unmarshal([]byte(people), &x.People)
		json.Unmarshal([]byte(spans), &x.Spans)
		d.Sections = append(d.Sections, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d.Todos, err = s.scanDigestTodos(digestVisJoin+`WHERE x.item_id = ? AND `+vis+` ORDER BY x.n`, append([]any{itemID}, vargs...))
	if err != nil {
		return nil, err
	}
	fix, err := s.Corrector()
	if err != nil {
		return nil, err
	}
	d.Overview = fix.Text(0, d.Overview)
	for i := range d.Sections {
		d.Sections[i].Subject = fix.Text(0, d.Sections[i].Subject)
		d.Sections[i].Text = fix.Text(0, d.Sections[i].Text)
	}
	for i := range d.Todos {
		d.Todos[i].Text = fix.Text(0, d.Todos[i].Text)
	}
	return d, nil
}

// DigestedItems lists the meetings the scope can see a digest of, newest
// first, with their overviews: the meeting list.
type DigestedItem struct {
	ItemID   string    `json:"itemId"`
	Title    string    `json:"title"`
	Start    time.Time `json:"start"`
	Overview string    `json:"overview"`
	Sections int       `json:"sections"`
	Todos    int       `json:"todos"`
}

// DigestedItems returns up to limit digested meetings the scope can see.
func (s *Store) DigestedItems(sc sensitivity.Scope, limit int) ([]DigestedItem, error) {
	vis, vargs := digestVis(sc)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Read the spelling rules first: the store has one connection, so no
	// other query may run while rows are open.
	fix, err := s.Corrector()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT x.item_id, i.title, i.occurred_at, x.overview,
		(SELECT count(*) FROM meeting_sections WHERE item_id = x.item_id), (SELECT count(*) FROM meeting_todos WHERE item_id = x.item_id)
		FROM meeting_digests x JOIN items i ON i.id = x.item_id`+digestVisJoin+`WHERE `+vis+` ORDER BY i.occurred_at DESC LIMIT ?`,
		append(vargs, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DigestedItem
	for rows.Next() {
		var x DigestedItem
		var at int64
		if err := rows.Scan(&x.ItemID, &x.Title, &at, &x.Overview, &x.Sections, &x.Todos); err != nil {
			return nil, err
		}
		x.Start, x.Overview = time.Unix(at, 0), fix.Text(0, x.Overview)
		out = append(out, x)
	}
	return out, rows.Err()
}

// MeetingTopic is a thread a meeting's statements are in, and how many of
// the meeting's.
type MeetingTopic struct {
	ID         int64  `json:"id"`
	Label      string `json:"label"`
	LabelLevel int    `json:"-"`
	Statements int    `json:"statements"`
	Size       int    `json:"size"`
}

// MeetingTopics returns the threads holding at least min of a meeting's
// current statements the scope may see, most first. Derived on read from
// the statements' memberships, so it follows rethreading, merges and splits.
func (s *Store) MeetingTopics(sc sensitivity.Scope, itemID string, min int) ([]MeetingTopic, error) {
	vis, vargs := statementVis(sc)
	if min < 1 {
		min = 1
	}
	rows, err := s.db.Query(`SELECT t.id, t.label, t.label_level, t.size, count(*) n
		FROM thread_items ti JOIN statements st ON st.id = ti.statement_id JOIN chunks c ON c.id = st.chunk_id`+chunkVisJoin+`
		JOIN topics t ON t.id = ti.topic_id
		WHERE st.item_id = ? AND ti.current = 1 AND `+vis+`
		GROUP BY t.id HAVING n >= ? ORDER BY n DESC, t.id`, append(append([]any{itemID}, vargs...), min)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingTopic
	for rows.Next() {
		var t MeetingTopic
		if err := rows.Scan(&t.ID, &t.Label, &t.LabelLevel, &t.Size, &t.Statements); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DigestCommitments returns the to-dos of the meetings' digests the scope
// can see, as commitments for the to-do view. A to-do that matched a
// statement is that statement (its id, thread and the later statements
// relating to it, so its state is read as before) with the to-do's own
// words, owners and date over it; one that matched none stands alone with a
// negative id. A to-do only floated is listed with proposals; one declined
// in the meeting is closed.
func (s *Store) DigestCommitments(sc sensitivity.Scope, proposals bool) ([]Commitment, error) {
	vis, vargs := digestVis(sc)
	todos, err := s.scanDigestTodos(digestVisJoin+`WHERE (x.agreed != 'floated' OR ?) AND `+vis+` ORDER BY x.said_at DESC, x.id`, append([]any{proposals}, vargs...))
	if err != nil {
		return nil, err
	}
	var ids []any
	for _, t := range todos {
		if t.StatementID > 0 {
			ids = append(ids, t.StatementID)
		}
	}
	base := map[int64]Commitment{}
	for i := 0; i < len(ids); i += 400 {
		part := ids[i:min(i+400, len(ids))]
		cs, err := s.loadCommitments(sc, `st.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+`)`, part)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			base[c.ID] = c
		}
	}
	fix, err := s.Corrector()
	if err != nil {
		return nil, err
	}
	used := map[int64]bool{}
	out := make([]Commitment, 0, len(todos))
	for _, t := range todos {
		var c Commitment
		// A statement that two to-dos matched is shown for the first only.
		if b, ok := base[t.StatementID]; ok && !used[t.StatementID] {
			used[t.StatementID] = true
			c = b
		} else {
			c.ID = -t.ID
			c.ItemKind = "meeting"
		}
		c.ItemID, c.ItemTitle, c.Digest = t.ItemID, t.ItemTitle, t.ID
		c.Kind, c.Text, c.Original, c.SaidAt = "commitment", t.Text, "", t.SaidAt
		c.Who = nil
		for _, o := range t.Owners {
			if !strings.EqualFold(o, "unclear") {
				c.Who = append(c.Who, o)
			}
		}
		c.Holder = ""
		if len(c.Who) > 0 {
			c.Holder = c.Who[0]
		}
		c.DueDate, c.Requester = t.DueDate, t.Requester
		c.Status = map[string]string{"agreed": "agreed", "floated": "proposed"}[t.Agreed]
		if c.Status == "" {
			c.Status = "stated"
		}
		c.Grounded, c.Issues = t.Grounded, t.Issues
		if len(t.Evidence) > 0 {
			e := t.Evidence[0]
			c.Quote, c.Evidence = e.Quote, t.Evidence
			if c.ChunkID == 0 {
				c.ChunkID, c.Line = e.ChunkID, e.Line
			}
		}
		if t.Agreed == "declined" {
			c.Later = append(c.Later, Follow{Rel: "supersedes", Statement: Statement{ItemID: t.ItemID, Text: "Turned down or dropped in the meeting.", SaidAt: t.SaidAt}})
		}
		fix.Fix(&c.Statement)
		out = append(out, c)
	}
	return out, nil
}

// String is for logs.
func (d Digest) String() string {
	return fmt.Sprintf("%s: %d sections, %d to-dos", d.ItemID, len(d.Sections), len(d.Todos))
}
