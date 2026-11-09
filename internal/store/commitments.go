package store

import (
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// Commitment is something someone said would or should happen, with the
// thread it is on and the later statements that relate to it (done,
// replaced, repeated), all read under the scope.
type Commitment struct {
	Statement
	Topic int64    `json:"topic"`
	Later []Follow `json:"later,omitempty"`
	// From a meeting digest (DigestCommitments): the due date the model
	// worked out, as 2006-01-02 (empty when none was said), who asked for
	// it, and the digest to-do's id (the statement it matched, if any, is
	// the embedded Statement; ID is negative when none did).
	DueDate   string `json:"dueDate,omitempty"`
	Requester string `json:"requester,omitempty"`
	Digest    int64  `json:"digest,omitempty"`
}

// Follow is a later statement and how it relates to an earlier one
// (completes, supersedes, repeats, answers, pending, contradicts). Owner
// is set when the owner said it (a mark or a link): their word over the
// threader's.
type Follow struct {
	Rel   string `json:"rel"`
	Owner bool   `json:"owner,omitempty"`
	Statement
}

var commitmentCols = `st.id, st.item_id, st.chunk_id, c.seq, st.line, st.kind, st.text, st.holder, st.said_at, st.quote,
	i.kind, ` + displayTitle("i") + `, ` + stLevel + `, ` + stCats + `, st.status, st.who, st.evidence`

func scanCommitmentCols(sc interface{ Scan(...any) error }, st *Statement, extra ...any) error {
	var said int64
	var lvl int
	var cats int64
	var who, ev string
	dst := append([]any{&st.ID, &st.ItemID, &st.ChunkID, &st.ChunkSeq, &st.Line, &st.Kind, &st.Text, &st.Holder, &said, &st.Quote,
		&st.ItemKind, &st.ItemTitle, &lvl, &cats, &st.Status, &who, &ev}, extra...)
	if err := sc.Scan(dst...); err != nil {
		return err
	}
	st.SaidAt = time.Unix(said, 0)
	st.setPoint(who, ev, 0, 0)
	st.Label = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
	return nil
}

// Commitments lists the commitment statements the scope may see, newest
// first; with proposals, also positions and decisions still proposed
// ("we should…"), which the extractors keep apart from commitments.
func (s *Store) Commitments(sc sensitivity.Scope, proposals bool) ([]Commitment, error) {
	return s.loadCommitments(sc, `(st.kind = 'commitment' OR (? AND st.status = 'proposed' AND st.kind IN ('position', 'decision', 'opinion')))`, []any{proposals})
}

// loadCommitments reads the statements cond selects (cond may use the
// aliases st, c and i, and takes args), with the later statements that
// relate to each, under the scope.
func (s *Store) loadCommitments(sc sensitivity.Scope, cond string, condArgs []any) ([]Commitment, error) {
	vis, vargs := statementVis(sc)
	args := append(append([]any{}, condArgs...), vargs...)
	rows, err := s.db.Query(`SELECT `+commitmentCols+`,
		coalesce((SELECT t.topic_id FROM thread_items t WHERE t.statement_id = st.id ORDER BY t.current DESC, t.added_at DESC LIMIT 1), 0)
		FROM statements st JOIN chunks c ON c.id = st.chunk_id JOIN items i ON i.id = st.item_id`+chunkVisJoin+`
		WHERE `+cond+` AND `+vis+`
		ORDER BY st.said_at DESC, st.id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Commitment
	for rows.Next() {
		var c Commitment
		if err := scanCommitmentCols(rows, &c.Statement, &c.Topic); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	at := make(map[int64]int, len(out))
	ids := make([]int64, len(out))
	for i, c := range out {
		at[c.ID], ids[i] = i, c.ID
	}
	// The later statements, each visible in the scope on its own: a
	// commitment's outcome said somewhere the scope can't see stays unseen.
	seen := map[[2]int64]string{}
	for i := 0; i < len(ids); i += 400 {
		part := ids[i:min(i+400, len(ids))]
		a := make([]any, 0, len(part)+len(vargs))
		for _, id := range part {
			a = append(a, id)
		}
		a = append(a, vargs...)
		rows, err := s.db.Query(`SELECT `+commitmentCols+`, r.earlier, r.rel
			FROM statement_relations r JOIN statements st ON st.id = r.later JOIN chunks c ON c.id = st.chunk_id
			JOIN items i ON i.id = st.item_id`+chunkVisJoin+`
			WHERE r.earlier IN (`+strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+`) AND `+vis+`
			ORDER BY st.said_at, st.id`, a...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var f Follow
			var earlier int64
			if err := scanCommitmentCols(rows, &f.Statement, &earlier, &f.Rel); err != nil {
				rows.Close()
				return nil, err
			}
			// Relations are kept per threading version: one of each.
			k := [2]int64{earlier, f.ID}
			if seen[k] == f.Rel {
				continue
			}
			seen[k] = f.Rel
			out[at[earlier]].Later = append(out[at[earlier]].Later, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	// Read with the owner's spelling rules; what was written is kept.
	fix, err := s.Corrector()
	if err != nil {
		return nil, err
	}
	for i := range out {
		fix.Fix(&out[i].Statement)
		for j := range out[i].Later {
			fix.Fix(&out[i].Later[j].Statement)
		}
	}
	return out, nil
}
