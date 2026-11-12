package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/vindex"
)

// Value is a statement's typed value (a date, amount, number, percentage
// or duration): the words as said, and bounds normalised from them
// (knowledge.Normalize).
type Value struct {
	Type       string  `json:"type"`
	Raw        string  `json:"raw"`
	Lo         float64 `json:"lo"`
	Hi         float64 `json:"hi"`
	Normalized bool    `json:"normalized"`
}

// Statement is one thing someone said or wrote, extracted from a passage.
type Statement struct {
	ID        int64  `json:"id"`
	ItemID    string `json:"itemId"`
	ChunkID   int64  `json:"chunkId"`
	ChunkSeq  int    `json:"chunkSeq"`
	ChunkHash string `json:"-"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	// Original is the text as written when a spelling rule changed it for
	// reading (Corrector.Fix); empty otherwise.
	Original  string    `json:"original,omitempty"`
	Holder    string    `json:"holder"`
	HolderID  string    `json:"holderId,omitempty"`
	Certainty float64   `json:"certainty"`
	SaidAt    time.Time `json:"saidAt"`
	Value     Value     `json:"value"`
	// Quote is the source words the statement was taken from; Grounded is
	// false when the quote isn't in the source, or the statement's figures
	// or negation don't match it (Issues says which).
	Quote    string `json:"quote"`
	Grounded bool   `json:"grounded"`
	Issues   string `json:"issues,omitempty"`
	// Worth is the probability the statement is knowledge worth keeping
	// rather than small talk or garble; -1 when not scored.
	Worth float64 `json:"worth"`
	// SearchText is what search matches (see knowledge.SearchForm).
	SearchText string `json:"-"`
	// Points only: Status is how settled it is (decided, agreed, proposed,
	// open, answered, rejected, stated); Who are the people it belongs to;
	// Evidence is every line it rests on (Line and Quote are the first).
	Status   string     `json:"status,omitempty"`
	Who      []string   `json:"who,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	// Own is the statement's own sensitivity, on top of its passage's: a
	// point is as sensitive as every passage it rests on, and a personal
	// detail is marked on the point.
	Own sensitivity.Label `json:"-"`
	// Set on reads.
	ItemKind  string            `json:"itemKind,omitempty"`
	ItemTitle string            `json:"itemTitle,omitempty"`
	Label     sensitivity.Label `json:"label"`
	ClaimID   int64             `json:"claimId,omitempty"`
}

// Evidence is one source line a point rests on.
type Evidence struct {
	ChunkID int64  `json:"chunkId"`
	Line    int    `json:"line"` // in the chunk
	Quote   string `json:"quote"`
}

func jsonOrEmpty(v any, empty bool) string {
	if empty {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// --- Pipeline side (unscoped; stages only) ---

// ReplaceStatements swaps the statements extracted from an item's chunks.
// Their claim memberships go with them (cascade); the link step relinks.
func (s *Store) ReplaceStatements(itemID, version, hash string, sts []Statement) error {
	return s.ReplaceStatementsFrom(itemID, version, hash, "", sts)
}

// ReplaceStatementsFrom is ReplaceStatements recording, apart from the
// stage's stamp, a hash of the input alone (input, when set: the passages,
// labels and facts it read, without the prompt version or model). With
// both, a later run tells a changed input, which is always re-extracted,
// from a new version, which is redone only when asked
// (knowledge.updates). The item is also queued for threading again: its
// old statements, and so its thread memberships, are gone.
func (s *Store) ReplaceStatementsFrom(itemID, version, hash, input string, sts []Statement) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM statements WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		// An item threaded before goes back into the threads in its place
		// even when newer items are threaded (WasThreaded): its statements
		// changed, it isn't a backlog.
		var runs int
		if err := tx.QueryRow(`SELECT count(*) FROM thread_runs WHERE item_id = ?`, itemID).Scan(&runs); err != nil {
			return err
		}
		if runs > 0 {
			if err := stamp(tx, "was-threaded", itemID, "1", ""); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM thread_runs WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		if input != "" {
			if err := stamp(tx, "extract-input", itemID, "in", input); err != nil {
				return err
			}
		}
		for _, st := range sts {
			var holder any
			if st.HolderID != "" {
				holder = st.HolderID
			}
			v := st.Value
			if v.Type == "" {
				v.Type = "none"
			}
			if _, err := tx.Exec(`INSERT INTO statements(item_id, chunk_id, line, kind, text, holder, holder_id, certainty, said_at,
				value_type, value_raw, value_lo, value_hi, value_ok, quote, grounded, issues, search_text,
				status, who, evidence, level, categories) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				itemID, st.ChunkID, st.Line, st.Kind, st.Text, st.Holder, holder, st.Certainty, st.SaidAt.Unix(),
				v.Type, v.Raw, v.Lo, v.Hi, v.Normalized, st.Quote, st.Grounded, st.Issues, st.SearchText,
				st.Status, jsonOrEmpty(st.Who, len(st.Who) == 0), jsonOrEmpty(st.Evidence, len(st.Evidence) == 0),
				int(st.Own.Level), int64(st.Own.Categories)); err != nil {
				return err
			}
		}
		// The owner's corrections outlive re-extraction.
		if err := reapplyCorrections(tx, itemID); err != nil {
			return err
		}
		return stamp(tx, "extract", itemID, version, hash)
	})
}

// StatementsToScore returns statements without a worth score from this
// version of the scorer.
func (s *Store) StatementsToScore(version string) ([]Statement, error) {
	return s.statements(`WHERE st.worth_version != ? ORDER BY st.id`, version)
}

// SetWorth records a statement's worth score.
func (s *Store) SetWorth(id int64, p float64, version string) error {
	_, err := s.db.Exec(`UPDATE statements SET worth = ?, worth_version = ? WHERE id = ?`, p, version, id)
	return err
}

// AllStatements returns every statement (pipeline side).
func (s *Store) AllStatements() ([]Statement, error) {
	return s.statements(`ORDER BY st.id`)
}

// UnlinkedStatements returns statements not yet in a claim, oldest first:
// linking in the order things were said makes "supersedes" point forward.
func (s *Store) UnlinkedStatements() ([]Statement, error) {
	return s.statements(`WHERE NOT EXISTS (SELECT 1 FROM statement_claims sc WHERE sc.statement_id = st.id) ORDER BY st.said_at, st.id`)
}

func (s *Store) statements(where string, args ...any) ([]Statement, error) {
	rows, err := s.db.Query(`SELECT st.id, st.item_id, st.chunk_id, c.seq, c.text_hash, st.line, st.kind, st.text, st.holder, coalesce(st.holder_id, ''),
		st.certainty, st.said_at, st.value_type, st.value_raw, st.value_lo, st.value_hi, st.value_ok, st.quote, st.grounded, st.issues, coalesce(sc.claim_id, 0), coalesce(st.worth, -1), st.search_text,
		st.status, st.who, st.evidence, st.level, st.categories
		FROM statements st JOIN chunks c ON c.id = st.chunk_id LEFT JOIN statement_claims sc ON sc.statement_id = st.id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Statement
	for rows.Next() {
		var st Statement
		var said int64
		var who, ev string
		var lvl int
		var cats int64
		if err := rows.Scan(&st.ID, &st.ItemID, &st.ChunkID, &st.ChunkSeq, &st.ChunkHash, &st.Line, &st.Kind, &st.Text, &st.Holder, &st.HolderID,
			&st.Certainty, &said, &st.Value.Type, &st.Value.Raw, &st.Value.Lo, &st.Value.Hi, &st.Value.Normalized,
			&st.Quote, &st.Grounded, &st.Issues, &st.ClaimID, &st.Worth, &st.SearchText,
			&st.Status, &who, &ev, &lvl, &cats); err != nil {
			return nil, err
		}
		st.SaidAt = time.Unix(said, 0)
		st.setPoint(who, ev, lvl, cats)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) PutStatementVec(id int64, model string, v []float32) error {
	b := s.encodeVec(v)
	return s.tx(func(tx *sql.Tx) error {
		// A claim of this one statement reads its centroid from the
		// statement's vector: keep the old vector on the claim, as when
		// centroids were always stored.
		if _, err := tx.Exec(`UPDATE claims SET centroid = (SELECT vec FROM statement_vecs WHERE statement_id = ?1)
			WHERE centroid IS NULL AND id IN (SELECT claim_id FROM statement_claims WHERE statement_id = ?1)
			AND (SELECT vec FROM statement_vecs WHERE statement_id = ?1) IS NOT ?2`, id, b); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO statement_vecs(statement_id, model, vec) VALUES (?,?,?)
		ON CONFLICT(statement_id) DO UPDATE SET model = excluded.model, vec = excluded.vec`, id, model, b)
		return err
	})
}

// StatementVecs loads statement vectors for one model.
func (s *Store) StatementVecs(model string) (map[int64][]float32, error) {
	rows, err := s.db.Query(`SELECT statement_id, vec FROM statement_vecs WHERE model = ?`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]float32{}
	for rows.Next() {
		var id int64
		var b []byte
		rows.Scan(&id, &b)
		out[id] = decodeVec(b)
	}
	return out, rows.Err()
}

// ClaimRow is a claim as the linker keeps it.
type ClaimRow struct {
	ID       int64
	Kind     string
	Centroid []float32
	Members  []int64 // statement ids
	Text     string  // its first statement's text, for the judge
	Holder   string
	SaidAt   time.Time
	Value    Value
	// Items are the items its statements come from; Who the people its
	// statements belong to (points and imports only). The linker's rules
	// use them: a same-meeting "newer version" is usually a refinement.
	Items map[string]bool
	Who   []string
}

// Claims loads every claim with its members, for linking and clustering.
func (s *Store) Claims() ([]ClaimRow, error) {
	rows, err := s.db.Query(`SELECT c.id, c.kind, ` + claimCentroid + `, sc.statement_id, st.text, st.holder, st.said_at,
		st.value_type, st.value_raw, st.value_lo, st.value_hi, st.value_ok, st.item_id, st.who
		FROM claims c JOIN statement_claims sc ON sc.claim_id = c.id JOIN statements st ON st.id = sc.statement_id
		ORDER BY c.id, st.said_at, st.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaimRow
	for rows.Next() {
		var id, sid, said int64
		var kind, text, holder, itemID, who string
		var cen []byte
		var v Value
		rows.Scan(&id, &kind, &cen, &sid, &text, &holder, &said, &v.Type, &v.Raw, &v.Lo, &v.Hi, &v.Normalized, &itemID, &who)
		if len(out) == 0 || out[len(out)-1].ID != id {
			out = append(out, ClaimRow{ID: id, Kind: kind, Centroid: decodeVec(cen), Text: text, Holder: holder, SaidAt: time.Unix(said, 0), Value: v, Items: map[string]bool{}})
		}
		c := &out[len(out)-1]
		c.Members = append(c.Members, sid)
		c.Items[itemID] = true
		if who != "" {
			var ws []string
			if json.Unmarshal([]byte(who), &ws) == nil {
				c.Who = append(c.Who, ws...)
			}
		}
	}
	return out, rows.Err()
}

// NewClaim creates a claim holding one statement. Its centroid is stored
// only when vec isn't the statement's stored vector; otherwise it is read
// from statement_vecs (claimCentroid).
func (s *Store) NewClaim(st Statement, vec []float32) (int64, error) {
	var id int64
	err := s.tx(func(tx *sql.Tx) error {
		b := s.encodeVec(vec)
		var same int
		if b != nil {
			if err := tx.QueryRow(`SELECT count(*) FROM statement_vecs WHERE statement_id = ? AND vec = ?`, st.ID, b).Scan(&same); err != nil {
				return err
			}
		}
		var cen any = b
		if same > 0 {
			cen = nil
		}
		res, err := tx.Exec(`INSERT INTO claims(kind, created_at, centroid) VALUES (?,?,?)`, st.Kind, st.SaidAt.Unix(), cen)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		if len(vec) > 0 {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO claim_codes(claim_id, code) VALUES (?,?)`, id, vindex.Code(vec)); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`INSERT INTO statement_claims(statement_id, claim_id, sim, how) VALUES (?,?,1,'new')`, st.ID, id)
		return err
	})
	return id, err
}

// JoinClaim adds a statement to a claim and updates its centroid.
func (s *Store) JoinClaim(statementID, claimID int64, sim float64, how string, centroid []float32) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO statement_claims(statement_id, claim_id, sim, how) VALUES (?,?,?,?)`, statementID, claimID, sim, how); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE claims SET centroid = ? WHERE id = ?`, s.encodeVec(centroid), claimID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO claim_codes(claim_id, code) VALUES (?,?)`, claimID, vindex.Code(centroid))
		return err
	})
}

func (s *Store) RelateClaims(from, to int64, rel string, statementID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO claim_relations(from_claim, to_claim, rel, statement_id) VALUES (?,?,?,?)`, from, to, rel, statementID)
	return err
}

// ClearClaims removes every claim and its memberships and relations, so
// linking starts over from the stored statements. The owner's judgements
// are anchored to source lines, not claims, and survive.
func (s *Store) ClearClaims() error {
	return s.tx(func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM claim_relations`, `DELETE FROM statement_claims`, `DELETE FROM claims`} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
}

// DropEmptyClaims removes claims whose statements are all gone.
func (s *Store) DropEmptyClaims() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM claims WHERE NOT EXISTS (SELECT 1 FROM statement_claims sc WHERE sc.claim_id = claims.id)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TopicRow is a topic as the clustering step writes it.
type TopicRow struct {
	ID         int64
	Label      string
	Summary    string
	LabelLevel sensitivity.Level
	Centroid   []float32
	Claims     []int64
}

func (s *Store) Topics() ([]TopicRow, error) {
	rows, err := s.rdb.Query(`SELECT t.id, t.label, t.summary, t.label_level, t.centroid, coalesce(c.id, 0) FROM topics t LEFT JOIN claims c ON c.topic_id = t.id ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicRow
	for rows.Next() {
		var id, cid int64
		var label, summary string
		var lvl int
		var cen []byte
		rows.Scan(&id, &label, &summary, &lvl, &cen, &cid)
		if len(out) == 0 || out[len(out)-1].ID != id {
			out = append(out, TopicRow{ID: id, Label: label, Summary: summary, LabelLevel: sensitivity.Level(lvl), Centroid: decodeVec(cen)})
		}
		if cid != 0 {
			out[len(out)-1].Claims = append(out[len(out)-1].Claims, cid)
		}
	}
	return out, rows.Err()
}

// TopicRelation links two topics.
type TopicRelation struct {
	A, B     int64
	Sim      float64
	Shared   int      // items discussing both
	Entities []string // organisations and projects both involve
}

// TopicEvent is a change between rebuilds.
type TopicEvent struct {
	At      time.Time `json:"at"`
	TopicID int64     `json:"topicId"`
	Event   string    `json:"event"`
	Other   int64     `json:"other,omitempty"`
	Detail  string    `json:"detail"`
}

// ReplaceTopics writes the topic set: topics keep the ids given (stable
// across runs), claims point at their topic, relations and entities are
// rewritten, and events are appended.
func (s *Store) ReplaceTopics(ts []TopicRow, rels []TopicRelation, ents map[int64]map[string]int, events []TopicEvent) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE claims SET topic_id = NULL`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM topic_relations`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM topic_entities`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM topics`); err != nil {
			return err
		}
		for _, t := range ts {
			b := s.encodeVec(t.Centroid)
			if _, err := tx.Exec(`INSERT INTO topics(id, label, summary, label_level, centroid, size) VALUES (?,?,?,?,?,?)`,
				t.ID, t.Label, t.Summary, int(t.LabelLevel), b, len(t.Claims)); err != nil {
				return err
			}
			for _, c := range t.Claims {
				if _, err := tx.Exec(`UPDATE claims SET topic_id = ? WHERE id = ?`, t.ID, c); err != nil {
					return err
				}
			}
		}
		for _, r := range rels {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO topic_relations(a, b, sim, shared, entities) VALUES (?,?,?,?,?)`,
				r.A, r.B, r.Sim, r.Shared, strings.Join(r.Entities, ",")); err != nil {
				return err
			}
		}
		for tid, es := range ents {
			for e, n := range es {
				if _, err := tx.Exec(`INSERT INTO topic_entities(topic_id, entity_id, claims) VALUES (?,?,?)`, tid, e, n); err != nil {
					return err
				}
			}
		}
		for _, e := range events {
			if _, err := tx.Exec(`INSERT INTO topic_events(at, topic_id, event, other, detail) VALUES (?,?,?,?,?)`,
				e.At.Unix(), e.TopicID, e.Event, e.Other, e.Detail); err != nil {
				return err
			}
		}
		return nil
	})
}

// TopicEntities returns a topic's entities with how many of its claims
// involve each, most first.
func (s *Store) TopicEntities(topicID int64) ([]EntityCount, error) {
	rows, err := s.rdb.Query(`SELECT te.entity_id, coalesce(e.name, te.entity_id), coalesce(e.type, ''), coalesce(e.level, 0), te.claims
		FROM topic_entities te LEFT JOIN entities e ON e.id = te.entity_id WHERE te.topic_id = ? ORDER BY te.claims DESC`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityCount
	for rows.Next() {
		var e EntityCount
		var lvl int
		rows.Scan(&e.ID, &e.Name, &e.Type, &lvl, &e.Claims)
		e.Level = sensitivity.Level(lvl)
		out = append(out, e)
	}
	return out, rows.Err()
}

type EntityCount struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Level  sensitivity.Level `json:"-"`
	Claims int               `json:"claims"`
}

// TopicEvents returns a topic's history, newest first.
func (s *Store) TopicEvents(topicID int64) ([]TopicEvent, error) {
	rows, err := s.rdb.Query(`SELECT at, topic_id, event, other, detail FROM topic_events WHERE topic_id = ? ORDER BY at DESC, rowid DESC LIMIT 50`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicEvent
	for rows.Next() {
		var e TopicEvent
		var at int64
		rows.Scan(&at, &e.TopicID, &e.Event, &e.Other, &e.Detail)
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClaimItems maps each claim to the items its statements come from, for
// topic relations ("discussed together").
func (s *Store) ClaimItems() (map[int64][]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT sc.claim_id, st.item_id FROM statement_claims sc JOIN statements st ON st.id = sc.statement_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var c int64
		var it string
		rows.Scan(&c, &it)
		out[c] = append(out[c], it)
	}
	return out, rows.Err()
}

// StatementLevels returns each statement's effective level (its passage's
// and its own), for deciding how sensitive a topic label written from them
// is.
func (s *Store) StatementLevels() (map[int64]sensitivity.Level, error) {
	rows, err := s.db.Query(`SELECT st.id, ` + stLevel + ` FROM statements st JOIN chunks c ON c.id = st.chunk_id` + chunkVisJoin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]sensitivity.Level{}
	for rows.Next() {
		var id int64
		var l int
		rows.Scan(&id, &l)
		out[id] = sensitivity.Level(l)
	}
	return out, rows.Err()
}

// StatementsByID loads statements (pipeline side).
func (s *Store) StatementsByID(ids []int64) (map[int64]Statement, error) {
	out := map[int64]Statement{}
	for i := 0; i < len(ids); i += 500 {
		part := ids[i:min(i+500, len(ids))]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, len(part))
		for k, id := range part {
			args[k] = id
		}
		sts, err := s.statements(`WHERE st.id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, st := range sts {
			out[st.ID] = st
		}
	}
	return out, nil
}

// --- Judgements (the owner's) ---

// Judge records the owner's verdict on the claim a statement belongs to.
func (s *Store) Judge(statementID int64, verdict, note string) error {
	switch verdict {
	case "verified", "disputed", "false":
	case "":
		var item, text string
		if err := s.db.QueryRow(`SELECT item_id, text FROM statements WHERE id = ?`, statementID).Scan(&item, &text); err != nil {
			return err
		}
		_, err := s.db.Exec(`DELETE FROM judgments WHERE item_id = ? AND text = ?`, item, text)
		return err
	default:
		return fmt.Errorf("verdict must be verified, disputed or false")
	}
	var item, text, hash string
	var line int
	if err := s.db.QueryRow(`SELECT st.item_id, st.text, c.text_hash, st.line FROM statements st JOIN chunks c ON c.id = st.chunk_id WHERE st.id = ?`,
		statementID).Scan(&item, &text, &hash, &line); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO judgments(item_id, text, verdict, note, at, chunk_hash, line) VALUES (?,?,?,?,?,?,?)`,
		item, text, verdict, note, time.Now().Unix(), hash, line)
	return err
}

// --- Scoped reads ---

// visibleStatements returns the statements of the given claims that are
// visible in scope, with their labels.
func (s *Store) visibleStatements(sc sensitivity.Scope, claimIDs []int64) ([]Statement, error) {
	if len(claimIDs) == 0 {
		return nil, nil
	}
	vis, vargs := statementVis(sc)
	var out []Statement
	// Asked for most claims (the topic views): one pass over the table,
	// filtered here, beats thousands of IN-list queries.
	var keep map[int64]bool
	if len(claimIDs) > wholeTableAt {
		keep = make(map[int64]bool, len(claimIDs))
		for _, id := range claimIDs {
			keep[id] = true
		}
	}
	step := 400
	if keep != nil {
		step = len(claimIDs)
	}
	for i := 0; i < len(claimIDs); i += step {
		part := claimIDs[i:min(i+step, len(claimIDs))]
		in := `sc.claim_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(part)), ",") + `) AND `
		args := make([]any, 0, len(part)+len(vargs))
		if keep != nil {
			in = ""
		} else {
			for _, id := range part {
				args = append(args, id)
			}
		}
		args = append(args, vargs...)
		rows, err := s.rdb.Query(`SELECT st.id, st.item_id, st.chunk_id, c.seq, c.text_hash, st.line, st.kind, st.text, st.holder, coalesce(st.holder_id, ''),
			st.certainty, st.said_at, st.value_type, st.value_raw, st.value_lo, st.value_hi, st.value_ok, st.quote, st.grounded, st.issues,
			sc.claim_id, i.kind, `+displayTitle("i")+`,
			`+stLevel+`, `+stCats+`, st.status, st.who, st.evidence
			FROM statement_claims sc JOIN statements st ON st.id = sc.statement_id JOIN chunks c ON c.id = st.chunk_id
			JOIN items i ON i.id = st.item_id`+chunkVisJoin+`WHERE `+in+vis+` ORDER BY st.said_at, st.id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var st Statement
			var said int64
			var lvl int
			var cats int64
			var who, ev string
			if err := rows.Scan(&st.ID, &st.ItemID, &st.ChunkID, &st.ChunkSeq, &st.ChunkHash, &st.Line, &st.Kind, &st.Text, &st.Holder, &st.HolderID,
				&st.Certainty, &said, &st.Value.Type, &st.Value.Raw, &st.Value.Lo, &st.Value.Hi, &st.Value.Normalized,
				&st.Quote, &st.Grounded, &st.Issues, &st.ClaimID, &st.ItemKind, &st.ItemTitle, &lvl, &cats, &st.Status, &who, &ev); err != nil {
				rows.Close()
				return nil, err
			}
			if keep != nil && !keep[st.ClaimID] {
				continue
			}
			st.SaidAt = time.Unix(said, 0)
			st.setPoint(who, ev, 0, 0)
			st.Label = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
			out = append(out, st)
		}
		rows.Close()
	}
	return out, nil
}

// wholeTableAt: asked for more claims than this, the evidence loaders read
// their tables in one pass and filter in Go.
const wholeTableAt = 20000

// Relation is a claim relation as read in a scope.
type Relation struct {
	From, To    int64
	Rel         string
	StatementID int64
}

// visibleRelations returns relations touching the given claims whose
// establishing statement is visible in scope.
func (s *Store) visibleRelations(sc sensitivity.Scope, claimIDs []int64) ([]Relation, error) {
	if len(claimIDs) == 0 {
		return nil, nil
	}
	vis, vargs := statementVis(sc)
	// In batches (SQLite caps a statement's variables); a relation between
	// claims of two batches comes back twice.
	var out []Relation
	seen := map[Relation]bool{}
	var keep map[int64]bool
	step := 400
	if len(claimIDs) > wholeTableAt {
		keep = make(map[int64]bool, len(claimIDs))
		for _, id := range claimIDs {
			keep[id] = true
		}
		step = len(claimIDs)
	}
	for i := 0; i < len(claimIDs); i += step {
		part := claimIDs[i:min(i+step, len(claimIDs))]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		where := `(r.from_claim IN (` + ph + `) OR r.to_claim IN (` + ph + `)) AND `
		args := make([]any, 0, 2*len(part)+len(vargs))
		if keep != nil {
			where = ""
		} else {
			for _, id := range part {
				args = append(args, id)
			}
			for _, id := range part {
				args = append(args, id)
			}
		}
		args = append(args, vargs...)
		rows, err := s.rdb.Query(`SELECT r.from_claim, r.to_claim, r.rel, r.statement_id FROM claim_relations r
			JOIN statements st ON st.id = r.statement_id JOIN chunks c ON c.id = st.chunk_id`+chunkVisJoin+`
			WHERE `+where+vis, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r Relation
			rows.Scan(&r.From, &r.To, &r.Rel, &r.StatementID)
			if keep != nil && !keep[r.From] && !keep[r.To] {
				continue
			}
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ClaimEvidence is everything belief is computed from, for one claim, as
// visible in a scope.
type ClaimEvidence struct {
	ClaimID    int64
	Kind       string
	TopicID    int64
	Statements []Statement
	Relations  []Relation
	// Statements of related claims (contradicting, superseding...), keyed
	// by claim id.
	Related   map[int64][]Statement
	Judgments []Judgment
}

type Judgment struct {
	StatementID int64     `json:"statementId"`
	Verdict     string    `json:"verdict"`
	Note        string    `json:"note"`
	At          time.Time `json:"at"`
}

// Evidence loads what scope may see of the given claims. A claim with no
// visible statement is absent from the result: it does not exist in scope.
func (s *Store) Evidence(sc sensitivity.Scope, claimIDs []int64) (map[int64]*ClaimEvidence, error) {
	sts, err := s.visibleStatements(sc, claimIDs)
	if err != nil {
		return nil, err
	}
	out := map[int64]*ClaimEvidence{}
	for _, st := range sts {
		e := out[st.ClaimID]
		if e == nil {
			e = &ClaimEvidence{ClaimID: st.ClaimID, Related: map[int64][]Statement{}}
			out[st.ClaimID] = e
		}
		e.Statements = append(e.Statements, st)
	}
	ids := make([]int64, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	rels, err := s.visibleRelations(sc, ids)
	if err != nil {
		return nil, err
	}
	var other []int64
	seen := map[int64]bool{}
	for _, r := range rels {
		for _, id := range []int64{r.From, r.To} {
			if e := out[id]; e != nil {
				e.Relations = append(e.Relations, r)
			}
		}
		for _, id := range []int64{r.From, r.To} {
			if out[id] == nil && !seen[id] {
				seen[id] = true
				other = append(other, id)
			}
		}
	}
	// Related claims' statements, also scoped; ones invisible in scope are
	// dropped along with the relation's effect.
	// Claims already loaded aren't read again.
	osts, err := s.visibleStatements(sc, other)
	if err != nil {
		return nil, err
	}
	byClaim := map[int64][]Statement{}
	for _, st := range osts {
		byClaim[st.ClaimID] = append(byClaim[st.ClaimID], st)
	}
	for id, e := range out {
		byClaim[id] = e.Statements
	}
	for _, e := range out {
		for _, r := range e.Relations {
			o := r.To
			if o == e.ClaimID {
				o = r.From
			}
			e.Related[o] = byClaim[o]
		}
	}
	// Kinds, topics and judgements, in batches rather than a query per
	// claim and per statement.
	itemSet := map[string]bool{}
	for _, e := range out {
		for _, st := range e.Statements {
			itemSet[st.ItemID] = true
		}
	}
	if err := s.claimKinds(ids, out); err != nil {
		return nil, err
	}
	judged, err := s.judgmentsFor(itemSet)
	if err != nil {
		return nil, err
	}
	for _, e := range out {
		for _, st := range e.Statements {
			// Anchored by source line first, wording second; the latest wins.
			var best *storedJudgment
			for i, j := range judged[st.ItemID] {
				if (j.chunkHash != "" && j.chunkHash == st.ChunkHash && j.line == st.Line) || j.text == st.Text {
					if best == nil || j.at > best.at {
						best = &judged[st.ItemID][i]
					}
				}
			}
			if best != nil {
				e.Judgments = append(e.Judgments, Judgment{StatementID: st.ID, Verdict: best.verdict, Note: best.note, At: time.Unix(best.at, 0)})
			}
		}
	}
	return out, nil
}

// claimKinds fills in the kind and topic of the given claims.
func (s *Store) claimKinds(ids []int64, out map[int64]*ClaimEvidence) error {
	step := 500
	if len(ids) > wholeTableAt {
		step = len(ids)
	}
	for i := 0; i < len(ids); i += step {
		part := ids[i:min(i+step, len(ids))]
		args := make([]any, len(part))
		for k, id := range part {
			args[k] = id
		}
		q := `SELECT id, kind, coalesce(topic_id, 0) FROM claims WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(part)), ",") + `)`
		if step > 500 {
			q, args = `SELECT id, kind, coalesce(topic_id, 0) FROM claims`, nil
		}
		rows, err := s.rdb.Query(q, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var kind string
			var topic int64
			if err := rows.Scan(&id, &kind, &topic); err != nil {
				rows.Close()
				return err
			}
			if e := out[id]; e != nil {
				e.Kind, e.TopicID = kind, topic
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

type storedJudgment struct {
	text, verdict, note, chunkHash string
	line                           int
	at                             int64
}

// judgmentsFor loads the owner's verdicts on statements of these items.
func (s *Store) judgmentsFor(items map[string]bool) (map[string][]storedJudgment, error) {
	out := map[string][]storedJudgment{}
	var ids []any
	for id := range items {
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i += 500 {
		part := ids[i:min(i+500, len(ids))]
		rows, err := s.rdb.Query(`SELECT item_id, text, verdict, note, at, chunk_hash, line FROM judgments WHERE item_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+`)`, part...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var item string
			var j storedJudgment
			if err := rows.Scan(&item, &j.text, &j.verdict, &j.note, &j.at, &j.chunkHash, &j.line); err != nil {
				rows.Close()
				return nil, err
			}
			out[item] = append(out[item], j)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TopicClaims lists a topic's claim ids.
func (s *Store) TopicClaims(topicID int64) ([]int64, error) {
	return s.ids(`SELECT id FROM claims WHERE topic_id = ?`, topicID)
}

// TopicInfo is a topic's stored row and relations (labels are scope-checked
// by the caller against LabelLevel).
type TopicInfo struct {
	ID         int64
	Label      string
	Summary    string
	LabelLevel sensitivity.Level
	Size       int
	Related    []TopicRelation
}

func (s *Store) TopicInfos() (map[int64]*TopicInfo, error) {
	rows, err := s.rdb.Query(`SELECT id, label, summary, label_level, size FROM topics`)
	if err != nil {
		return nil, err
	}
	out := map[int64]*TopicInfo{}
	for rows.Next() {
		t := &TopicInfo{}
		var lvl int
		rows.Scan(&t.ID, &t.Label, &t.Summary, &lvl, &t.Size)
		t.LabelLevel = sensitivity.Level(lvl)
		out[t.ID] = t
	}
	rows.Close()
	rows, err = s.rdb.Query(`SELECT a, b, sim, shared, entities FROM topic_relations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r TopicRelation
		var ents string
		rows.Scan(&r.A, &r.B, &r.Sim, &r.Shared, &ents)
		if ents != "" {
			r.Entities = strings.Split(ents, ",")
		}
		if t := out[r.A]; t != nil {
			t.Related = append(t.Related, r)
		}
		if t := out[r.B]; t != nil {
			t.Related = append(t.Related, TopicRelation{A: r.B, B: r.A, Sim: r.Sim, Shared: r.Shared, Entities: r.Entities})
		}
	}
	for _, t := range out {
		sort.Slice(t.Related, func(i, j int) bool { return t.Related[i].Sim > t.Related[j].Sim })
	}
	return out, rows.Err()
}

// AllClaimIDs lists every claim (the caller scopes through Evidence).
func (s *Store) AllClaimIDs() ([]int64, error) { return s.ids(`SELECT id FROM claims`) }

func (st *Statement) setPoint(who, ev string, lvl int, cats int64) {
	if who != "" {
		json.Unmarshal([]byte(who), &st.Who)
	}
	if ev != "" {
		json.Unmarshal([]byte(ev), &st.Evidence)
	}
	st.Own = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
}

// SearchStatements finds claims whose statements match text (keyword), for
// looking claims up; results are scoped by Evidence afterwards.
func (s *Store) SearchStatements(text string, limit int) ([]int64, error) {
	terms := strings.Fields(strings.ToLower(text))
	if len(terms) == 0 {
		return nil, nil
	}
	var conds []string
	var args []any
	for _, t := range terms {
		if len(t) < 3 && !isDigits(t) {
			continue
		}
		conds = append(conds, `lower(CASE st.search_text WHEN '' THEN st.text ELSE st.search_text END) LIKE '%' || ? || '%'`)
		args = append(args, t)
	}
	if len(conds) == 0 {
		return nil, nil
	}
	args = append(args, limit)
	return s.ids(`SELECT sc.claim_id FROM statements st JOIN statement_claims sc ON sc.statement_id = st.id
		WHERE `+strings.Join(conds, " AND ")+` GROUP BY sc.claim_id ORDER BY count(*) DESC LIMIT ?`, args...)
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// StatementsWithoutSearchText lists statements extracted before search
// text existed; filling it in needs no model.
func (s *Store) StatementsWithoutSearchText() ([]Statement, error) {
	return s.statements(`WHERE st.search_text = '' ORDER BY st.id`)
}

// SetSearchText fills in a statement's search text.
func (s *Store) SetSearchText(id int64, text string) error {
	_, err := s.db.Exec(`UPDATE statements SET search_text = ? WHERE id = ?`, text, id)
	return err
}

// StatementsWithoutVec lists statements with no vector for model.
func (s *Store) StatementsWithoutVec(model string) ([]Statement, error) {
	return s.statements(`WHERE NOT EXISTS (SELECT 1 FROM statement_vecs v WHERE v.statement_id = st.id AND v.model = ?) ORDER BY st.id`, model)
}

// ClaimSignature summarises the claim set (ids and sizes), so topics are
// rebuilt only when claims changed.
func (s *Store) ClaimSignature() (string, error) {
	rows, err := s.db.Query(`SELECT claim_id, count(*) FROM statement_claims GROUP BY claim_id ORDER BY claim_id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, n int64
		rows.Scan(&id, &n)
		fmt.Fprintf(&b, "%d:%d,", id, n)
	}
	return b.String(), rows.Err()
}

// KnowledgeCounts are totals for the status view (owner only; no content).
type KnowledgeCounts struct {
	Statements  int `json:"statements"`
	Claims      int `json:"claims"`
	Topics      int `json:"topics"`
	Relations   int `json:"relations"`
	Extracted   int `json:"extracted"` // items with statements extracted
	Extractable int `json:"extractable"`
}

func (s *Store) KnowledgeCounts() (KnowledgeCounts, error) {
	var k KnowledgeCounts
	s.rdb.QueryRow(`SELECT count(*) FROM statements`).Scan(&k.Statements)
	s.rdb.QueryRow(`SELECT count(*) FROM claims`).Scan(&k.Claims)
	s.rdb.QueryRow(`SELECT count(*) FROM topics`).Scan(&k.Topics)
	s.rdb.QueryRow(`SELECT count(*) FROM claim_relations`).Scan(&k.Relations)
	s.rdb.QueryRow(`SELECT count(*) FROM stage_runs WHERE stage = 'extract'`).Scan(&k.Extracted)
	err := s.rdb.QueryRow(`SELECT count(*) FROM items WHERE kind != 'event'`).Scan(&k.Extractable)
	return k, err
}

// KnowledgeSignature changes whenever the database changes: data_version
// moves when another process commits (musubi knowledge, an update) and
// total_changes counts this connection's own writes (the store keeps one
// connection). Constant time, unlike counting rows: the views it guards
// must not pay for the check as the corpus grows. Any write invalidates,
// which is conservative.
//
// With a read pool the writer's connection may be held by a long write,
// so the signature is asked of a connection of its own that never writes:
// its data_version moves with every commit, this process's included. The
// count of its connections is part of it, since a reconnected one starts
// counting again.
func (s *Store) KnowledgeSignature() (string, error) {
	var dv, own int64
	if s.sdb != nil {
		err := s.sdb.QueryRow(`SELECT data_version FROM pragma_data_version`).Scan(&dv)
		return fmt.Sprint("r", s.sgen.Load(), ".", dv), err
	}
	err := s.db.QueryRow(`SELECT (SELECT data_version FROM pragma_data_version), total_changes()`).Scan(&dv, &own)
	return fmt.Sprint(dv, ".", own), err
}

// StatementsToLabel returns statements this version of the sensitivity
// labeller hasn't checked.
func (s *Store) StatementsToLabel(version string) ([]Statement, error) {
	return s.statements(`WHERE st.label_version != ? ORDER BY st.id`, version)
}

// StatementsUnlabelled lists statements no labeller has checked.
func (s *Store) StatementsUnlabelled() ([]Statement, error) {
	return s.statements(`WHERE st.label_version = '' ORDER BY st.id`)
}

// LabelStatement raises a statement's own label (never lowers it: a
// labeller only adds what it recognises) and records the labeller's
// version.
func (s *Store) LabelStatement(id int64, l sensitivity.Label, version string) error {
	_, err := s.db.Exec(`UPDATE statements SET level = max(level, ?), categories = categories | ?, label_version = ? WHERE id = ?`,
		int(l.Level), int64(l.Categories), version, id)
	return err
}

// WasThreaded reports whether an item was threaded before its statements
// were last replaced.
func (s *Store) WasThreaded(itemID string) bool {
	v, _ := s.StageStamp("was-threaded", itemID)
	return v != ""
}

// KeepStatements records an item's statements as current under version
// and hash (and input) without extracting again: the passages they came
// from have the same text (see SetExtractText).
func (s *Store) KeepStatements(itemID, version, hash, input string) error {
	return s.tx(func(tx *sql.Tx) error {
		if input != "" {
			if err := stamp(tx, "extract-input", itemID, "in", input); err != nil {
				return err
			}
		}
		return stamp(tx, "extract", itemID, version, hash)
	})
}

// SetExtractText records the key of the passage text, and of how it was
// extracted, that an item's statements came from.
func (s *Store) SetExtractText(itemID, key string) error {
	return s.tx(func(tx *sql.Tx) error { return stamp(tx, "extract-text", itemID, "t", key) })
}
