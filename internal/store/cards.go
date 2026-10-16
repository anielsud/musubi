package store

import (
	"encoding/json"
	"strings"
	"time"
)

// CardRow is one linked statement as the card index sees it: which claim
// and topic it is in, where and when it was said, and who it belongs to.
type CardRow struct {
	StatementID int64
	ClaimID     int64
	TopicID     int64
	ItemID      string
	SaidAt      time.Time
	Text        string
	Holder      string
	Who         []string
}

// CardRows lists every linked statement for building the card index
// (trigger terms, topics discussed together). Like Claims, it is unscoped
// because it builds an index, not a view: nothing from it is shown. Cards
// are read through Evidence, which is scoped.
func (s *Store) CardRows() ([]CardRow, error) {
	rows, err := s.db.Query(`SELECT st.id, sc.claim_id, coalesce(c.topic_id, 0), st.item_id, st.said_at, st.text, st.holder, st.who
		FROM statements st JOIN statement_claims sc ON sc.statement_id = st.id JOIN claims c ON c.id = sc.claim_id
		ORDER BY st.item_id, st.said_at, st.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardRow
	for rows.Next() {
		var r CardRow
		var said int64
		var who string
		if err := rows.Scan(&r.StatementID, &r.ClaimID, &r.TopicID, &r.ItemID, &said, &r.Text, &r.Holder, &who); err != nil {
			return nil, err
		}
		r.SaidAt = time.Unix(said, 0)
		if who != "" {
			json.Unmarshal([]byte(who), &r.Who)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClaimCentroids loads every claim's centroid, for finding the claims
// nearest a query.
func (s *Store) ClaimCentroids() (map[int64][]float32, error) {
	rows, err := s.db.Query(`SELECT id, centroid FROM claims WHERE centroid IS NOT NULL`)
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

// PersonNameParts returns the lower-case words of every person's name the
// store knows (person entities' aliases and everyone heard speaking), so
// names are never mistaken for project terms.
func (s *Store) PersonNameParts() (map[string]bool, error) {
	out := map[string]bool{}
	add := func(q string, split func(string) []string) error {
		rows, err := s.db.Query(q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			rows.Scan(&a)
			for _, n := range split(a) {
				for _, w := range strings.Fields(strings.ToLower(n)) {
					out[strings.Trim(w, "().,")] = true
				}
			}
		}
		return rows.Err()
	}
	one := func(s string) []string { return []string{s} }
	if err := add(`SELECT a.alias FROM entity_aliases a JOIN entities e ON e.id = a.entity_id WHERE e.type = 'person'`, one); err != nil {
		return nil, err
	}
	if err := add(`SELECT DISTINCT speakers FROM chunks`, func(s string) []string { return strings.Split(s, ",") }); err != nil {
		return nil, err
	}
	return out, nil
}

// ClaimRelations lists every relation between claims, for benchmarks.
// Unscoped like Claims: it feeds tools, not views.
func (s *Store) ClaimRelations() ([]Relation, error) {
	rows, err := s.db.Query(`SELECT from_claim, to_claim, rel, statement_id FROM claim_relations`)
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
