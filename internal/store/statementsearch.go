package store

import (
	"math"
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// StatementHit is a statement found by meaning, with the passage it was
// taken from as its evidence (nil when the scope can't see the passage).
type StatementHit struct {
	Statement
	Sim     float64 `json:"sim"`
	Passage *Hit    `json:"passage,omitempty"`
}

// StatementsNear finds the statements nearest a query vector, visible in
// the scope, most similar first. The claim code index proposes the nearest
// claims (no statement vectors are held in memory); their visible
// statements are then scored exactly against their own vectors. Statements
// from the excluded items (the meeting in progress) are left out.
func (s *Store) StatementsNear(sc sensitivity.Scope, q []float32, model string, claims int, exclude []string) ([]StatementHit, error) {
	ix, err := s.ClaimIndex()
	if err != nil {
		return nil, err
	}
	sts, err := s.visibleStatements(sc, ix.Nearest(q, claims))
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, x := range exclude {
		skip[x] = true
	}
	var ids []int64
	byID := map[int64]Statement{}
	for _, st := range sts {
		if !skip[st.ItemID] {
			ids = append(ids, st.ID)
			byID[st.ID] = st
		}
	}
	vecs, err := s.statementVecsOf(ids, model)
	if err != nil {
		return nil, err
	}
	out := make([]StatementHit, 0, len(vecs))
	for id, v := range vecs {
		out = append(out, StatementHit{Statement: byID[id], Sim: cosine(q, v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sim > out[j].Sim })
	return out, nil
}

// WithPassages adds each hit's passage, read under the scope.
func (s *Store) WithPassages(sc sensitivity.Scope, hs []StatementHit) error {
	for i := range hs {
		h, err := s.hit(sc, hs[i].ChunkID)
		if err != nil {
			return err
		}
		hs[i].Passage = h
	}
	return nil
}

func (s *Store) statementVecsOf(ids []int64, model string) (map[int64][]float32, error) {
	out := map[int64][]float32{}
	for i := 0; i < len(ids); i += 500 {
		part := ids[i:min(i+500, len(ids))]
		args := make([]any, 0, len(part)+1)
		args = append(args, model)
		for _, id := range part {
			args = append(args, id)
		}
		rows, err := s.db.Query(`SELECT statement_id, vec FROM statement_vecs WHERE model = ? AND statement_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var b []byte
			if err := rows.Scan(&id, &b); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = decodeVec(b)
		}
		rows.Close()
	}
	return out, nil
}

func cosine(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		if i >= len(b) {
			break
		}
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return d / math.Sqrt(na*nb)
}
