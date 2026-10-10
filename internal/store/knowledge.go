package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// Statement is one thing someone said or wrote, extracted from a passage.
type Statement struct {
	ID        int64     `json:"id"`
	ItemID    string    `json:"itemId"`
	ChunkID   int64     `json:"chunkId"`
	ChunkSeq  int       `json:"chunkSeq"`
	Line      int       `json:"line"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	Holder    string    `json:"holder"`
	HolderID  string    `json:"holderId,omitempty"`
	Certainty string    `json:"certainty"`
	SaidAt    time.Time `json:"saidAt"`
	When      string    `json:"when,omitempty"`
	About     []string  `json:"about,omitempty"`
	// Set on reads.
	ItemKind  string            `json:"itemKind,omitempty"`
	ItemTitle string            `json:"itemTitle,omitempty"`
	Label     sensitivity.Label `json:"label"`
	ClaimID   int64             `json:"claimId,omitempty"`
}

// --- Pipeline side (unscoped; stages only) ---

// ReplaceStatements swaps the statements extracted from an item's chunks.
// Their claim memberships go with them (cascade); the link step relinks.
func (s *Store) ReplaceStatements(itemID, version, hash string, sts []Statement) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM statements WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		for _, st := range sts {
			about, _ := json.Marshal(st.About)
			var holder any
			if st.HolderID != "" {
				holder = st.HolderID
			}
			if _, err := tx.Exec(`INSERT INTO statements(item_id, chunk_id, line, kind, text, holder, holder_id, certainty, said_at, about_when, about)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)`, itemID, st.ChunkID, st.Line, st.Kind, st.Text, st.Holder, holder, st.Certainty,
				st.SaidAt.Unix(), st.When, string(about)); err != nil {
				return err
			}
		}
		return stamp(tx, "extract", itemID, version, hash)
	})
}

// UnlinkedStatements returns statements not yet in a claim, oldest first:
// linking in the order things were said makes "supersedes" point forward.
func (s *Store) UnlinkedStatements() ([]Statement, error) {
	return s.statements(`WHERE NOT EXISTS (SELECT 1 FROM statement_claims sc WHERE sc.statement_id = st.id) ORDER BY st.said_at, st.id`)
}

func (s *Store) statements(where string, args ...any) ([]Statement, error) {
	rows, err := s.db.Query(`SELECT st.id, st.item_id, st.chunk_id, c.seq, st.line, st.kind, st.text, st.holder, coalesce(st.holder_id, ''),
		st.certainty, st.said_at, st.about_when, st.about, coalesce(sc.claim_id, 0)
		FROM statements st JOIN chunks c ON c.id = st.chunk_id LEFT JOIN statement_claims sc ON sc.statement_id = st.id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Statement
	for rows.Next() {
		var st Statement
		var said int64
		var about string
		if err := rows.Scan(&st.ID, &st.ItemID, &st.ChunkID, &st.ChunkSeq, &st.Line, &st.Kind, &st.Text, &st.Holder, &st.HolderID,
			&st.Certainty, &said, &st.When, &about, &st.ClaimID); err != nil {
			return nil, err
		}
		st.SaidAt = time.Unix(said, 0)
		json.Unmarshal([]byte(about), &st.About)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) PutStatementVec(id int64, model string, v []float32) error {
	b, err := sqlite_vec.SerializeFloat32(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO statement_vecs(statement_id, model, vec) VALUES (?,?,?)
		ON CONFLICT(statement_id) DO UPDATE SET model = excluded.model, vec = excluded.vec`, id, model, b)
	return err
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

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		u := uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24
		v[i] = float32frombits(u)
	}
	return v
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
}

// Claims loads every claim with its members, for linking and clustering.
func (s *Store) Claims() ([]ClaimRow, error) {
	rows, err := s.db.Query(`SELECT c.id, c.kind, c.centroid, sc.statement_id, st.text, st.holder, st.said_at
		FROM claims c JOIN statement_claims sc ON sc.claim_id = c.id JOIN statements st ON st.id = sc.statement_id
		ORDER BY c.id, st.said_at, st.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaimRow
	for rows.Next() {
		var id, sid, said int64
		var kind, text, holder string
		var cen []byte
		rows.Scan(&id, &kind, &cen, &sid, &text, &holder, &said)
		if len(out) == 0 || out[len(out)-1].ID != id {
			out = append(out, ClaimRow{ID: id, Kind: kind, Centroid: decodeVec(cen), Text: text, Holder: holder, SaidAt: time.Unix(said, 0)})
		}
		c := &out[len(out)-1]
		c.Members = append(c.Members, sid)
	}
	return out, rows.Err()
}

// NewClaim creates a claim holding one statement.
func (s *Store) NewClaim(st Statement, vec []float32) (int64, error) {
	var id int64
	err := s.tx(func(tx *sql.Tx) error {
		b, _ := sqlite_vec.SerializeFloat32(vec)
		res, err := tx.Exec(`INSERT INTO claims(kind, created_at, centroid) VALUES (?,?,?)`, st.Kind, st.SaidAt.Unix(), b)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
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
		b, _ := sqlite_vec.SerializeFloat32(centroid)
		_, err := tx.Exec(`UPDATE claims SET centroid = ? WHERE id = ?`, b, claimID)
		return err
	})
}

func (s *Store) RelateClaims(from, to int64, rel string, statementID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO claim_relations(from_claim, to_claim, rel, statement_id) VALUES (?,?,?,?)`, from, to, rel, statementID)
	return err
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
	rows, err := s.db.Query(`SELECT t.id, t.label, t.summary, t.label_level, t.centroid, coalesce(c.id, 0) FROM topics t LEFT JOIN claims c ON c.topic_id = t.id ORDER BY t.id`)
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
	A, B   int64
	Sim    float64
	Shared int
}

// ReplaceTopics writes the topic set: topics keep the ids given (stable
// across runs), claims point at their topic, and relations are rewritten.
func (s *Store) ReplaceTopics(ts []TopicRow, rels []TopicRelation) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE claims SET topic_id = NULL`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM topic_relations`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM topics`); err != nil {
			return err
		}
		for _, t := range ts {
			b, _ := sqlite_vec.SerializeFloat32(t.Centroid)
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
			if _, err := tx.Exec(`INSERT OR REPLACE INTO topic_relations(a, b, sim, shared) VALUES (?,?,?,?)`, r.A, r.B, r.Sim, r.Shared); err != nil {
				return err
			}
		}
		return nil
	})
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

// StatementLevels returns each statement's effective level (its passage's),
// for deciding how sensitive a topic label written from them is.
func (s *Store) StatementLevels() (map[int64]sensitivity.Level, error) {
	rows, err := s.db.Query(`SELECT st.id, max(il.level, coalesce(cl.level, 0)) FROM statements st JOIN chunks c ON c.id = st.chunk_id` + chunkVisJoin)
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
	var item, text string
	if err := s.db.QueryRow(`SELECT item_id, text FROM statements WHERE id = ?`, statementID).Scan(&item, &text); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO judgments(item_id, text, verdict, note, at) VALUES (?,?,?,?,?)`, item, text, verdict, note, time.Now().Unix())
	return err
}

// --- Scoped reads ---

// visibleStatements returns the statements of the given claims that are
// visible in scope, with their labels.
func (s *Store) visibleStatements(sc sensitivity.Scope, claimIDs []int64) ([]Statement, error) {
	if len(claimIDs) == 0 {
		return nil, nil
	}
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	var out []Statement
	for i := 0; i < len(claimIDs); i += 400 {
		part := claimIDs[i:min(i+400, len(claimIDs))]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, 0, len(part)+len(vargs))
		for _, id := range part {
			args = append(args, id)
		}
		args = append(args, vargs...)
		rows, err := s.db.Query(`SELECT st.id, st.item_id, st.chunk_id, c.seq, st.line, st.kind, st.text, st.holder, coalesce(st.holder_id, ''),
			st.certainty, st.said_at, st.about_when, st.about, sc.claim_id, i.kind, CASE WHEN c.title != '' THEN c.title ELSE i.title END,
			max(il.level, coalesce(cl.level, 0)), (il.categories | coalesce(cl.categories, 0))
			FROM statement_claims sc JOIN statements st ON st.id = sc.statement_id JOIN chunks c ON c.id = st.chunk_id
			JOIN items i ON i.id = st.item_id`+chunkVisJoin+`WHERE sc.claim_id IN (`+ph+`) AND `+vis+` ORDER BY st.said_at, st.id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var st Statement
			var said int64
			var about string
			var lvl int
			var cats int64
			if err := rows.Scan(&st.ID, &st.ItemID, &st.ChunkID, &st.ChunkSeq, &st.Line, &st.Kind, &st.Text, &st.Holder, &st.HolderID,
				&st.Certainty, &said, &st.When, &about, &st.ClaimID, &st.ItemKind, &st.ItemTitle, &lvl, &cats); err != nil {
				rows.Close()
				return nil, err
			}
			st.SaidAt = time.Unix(said, 0)
			json.Unmarshal([]byte(about), &st.About)
			st.Label = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
			out = append(out, st)
		}
		rows.Close()
	}
	return out, nil
}

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
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	ph := strings.TrimSuffix(strings.Repeat("?,", len(claimIDs)), ",")
	args := []any{}
	for _, id := range claimIDs {
		args = append(args, id)
	}
	for _, id := range claimIDs {
		args = append(args, id)
	}
	args = append(args, vargs...)
	rows, err := s.db.Query(`SELECT r.from_claim, r.to_claim, r.rel, r.statement_id FROM claim_relations r
		JOIN statements st ON st.id = r.statement_id JOIN chunks c ON c.id = st.chunk_id`+chunkVisJoin+`
		WHERE (r.from_claim IN (`+ph+`) OR r.to_claim IN (`+ph+`)) AND `+vis, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		var r Relation
		rows.Scan(&r.From, &r.To, &r.Rel, &r.StatementID)
		out = append(out, r)
	}
	return out, rows.Err()
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
	osts, err := s.visibleStatements(sc, append(other, ids...))
	if err != nil {
		return nil, err
	}
	byClaim := map[int64][]Statement{}
	for _, st := range osts {
		byClaim[st.ClaimID] = append(byClaim[st.ClaimID], st)
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
	// Kinds, topics and judgements.
	for _, e := range out {
		s.db.QueryRow(`SELECT kind, coalesce(topic_id, 0) FROM claims WHERE id = ?`, e.ClaimID).Scan(&e.Kind, &e.TopicID)
		for _, st := range e.Statements {
			var j Judgment
			var at int64
			err := s.db.QueryRow(`SELECT verdict, note, at FROM judgments WHERE item_id = ? AND text = ?`, st.ItemID, st.Text).Scan(&j.Verdict, &j.Note, &at)
			if err == nil {
				j.StatementID, j.At = st.ID, time.Unix(at, 0)
				e.Judgments = append(e.Judgments, j)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
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
	rows, err := s.db.Query(`SELECT id, label, summary, label_level, size FROM topics`)
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
	rows, err = s.db.Query(`SELECT a, b, sim, shared FROM topic_relations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r TopicRelation
		rows.Scan(&r.A, &r.B, &r.Sim, &r.Shared)
		if t := out[r.A]; t != nil {
			t.Related = append(t.Related, r)
		}
		if t := out[r.B]; t != nil {
			t.Related = append(t.Related, TopicRelation{A: r.B, B: r.A, Sim: r.Sim, Shared: r.Shared})
		}
	}
	for _, t := range out {
		sort.Slice(t.Related, func(i, j int) bool { return t.Related[i].Sim > t.Related[j].Sim })
	}
	return out, rows.Err()
}

// AllClaimIDs lists every claim (the caller scopes through Evidence).
func (s *Store) AllClaimIDs() ([]int64, error) { return s.ids(`SELECT id FROM claims`) }

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
		if len(t) < 3 {
			continue
		}
		conds = append(conds, `lower(st.text) LIKE '%' || ? || '%'`)
		args = append(args, t)
	}
	if len(conds) == 0 {
		return nil, nil
	}
	args = append(args, limit)
	return s.ids(`SELECT sc.claim_id FROM statements st JOIN statement_claims sc ON sc.statement_id = st.id
		WHERE `+strings.Join(conds, " AND ")+` GROUP BY sc.claim_id ORDER BY count(*) DESC LIMIT ?`, args...)
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
	s.db.QueryRow(`SELECT count(*) FROM statements`).Scan(&k.Statements)
	s.db.QueryRow(`SELECT count(*) FROM claims`).Scan(&k.Claims)
	s.db.QueryRow(`SELECT count(*) FROM topics`).Scan(&k.Topics)
	s.db.QueryRow(`SELECT count(*) FROM claim_relations`).Scan(&k.Relations)
	s.db.QueryRow(`SELECT count(*) FROM stage_runs WHERE stage = 'extract'`).Scan(&k.Extracted)
	err := s.db.QueryRow(`SELECT count(*) FROM items WHERE kind != 'event'`).Scan(&k.Extractable)
	return k, err
}
