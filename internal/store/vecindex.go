package store

import (
	"database/sql"
	"sync"

	"github.com/anielsud/musubi/internal/vindex"
)

// Code indexes (internal/vindex) for passages, per embedding model, and for
// claims: loaded once into memory (96 bytes a vector), reloaded after the
// database changes. Unscoped like CardRows: an index that only proposes
// candidates, which every caller then reads through a scoped query.
type codeIndexes struct {
	mu   sync.Mutex
	byID map[string]*loadedIndex
}

type loadedIndex struct {
	sig string
	ix  *vindex.Index
}

// exactBelow: at fewer vectors than this, an exact scan is cheap and the
// prefilter isn't used.
const exactBelow = 2000

func (s *Store) codeIndex(key string, load func() (*vindex.Index, error)) (*vindex.Index, error) {
	sig, err := s.KnowledgeSignature()
	if err != nil {
		return nil, err
	}
	s.codes.mu.Lock()
	defer s.codes.mu.Unlock()
	if s.codes.byID == nil {
		s.codes.byID = map[string]*loadedIndex{}
	}
	if l := s.codes.byID[key]; l != nil && l.sig == sig {
		return l.ix, nil
	}
	ix, err := load()
	if err != nil {
		return nil, err
	}
	// Loading may have written missing codes; the signature after it is
	// the one this index matches.
	if sig, err = s.KnowledgeSignature(); err != nil {
		return nil, err
	}
	s.codes.byID[key] = &loadedIndex{sig, ix}
	return ix, nil
}

// passageIndex returns the code index of the passages embedded with model.
func (s *Store) passageIndex(model string) (*vindex.Index, error) {
	return s.codeIndex("chunk:"+model, func() (*vindex.Index, error) {
		if err := s.backfillCodes(`SELECT v.chunk_id, v.vec FROM chunk_vecs v LEFT JOIN chunk_codes k ON k.chunk_id = v.chunk_id AND k.model = v.model
			WHERE v.model = ? AND k.chunk_id IS NULL`, `INSERT OR REPLACE INTO chunk_codes(chunk_id, model, code) VALUES (?, ?, ?)`, model); err != nil {
			return nil, err
		}
		return s.loadCodes(`SELECT chunk_id, code FROM chunk_codes WHERE model = ?`, model)
	})
}

// ClaimIndex returns the code index of the claims' centroids.
func (s *Store) ClaimIndex() (*vindex.Index, error) {
	return s.codeIndex("claims", func() (*vindex.Index, error) {
		if err := s.backfillCodes(`SELECT id, cen FROM (SELECT c.id, `+claimCentroid+` AS cen FROM claims c LEFT JOIN claim_codes k ON k.claim_id = c.id
			WHERE k.claim_id IS NULL) WHERE cen IS NOT NULL AND length(cen) > 0`, `INSERT OR REPLACE INTO claim_codes(claim_id, code) VALUES (?, ?)`); err != nil {
			return nil, err
		}
		return s.loadCodes(`SELECT claim_id, code FROM claim_codes`)
	})
}

// backfillCodes writes codes for vectors stored before codes were.
func (s *Store) backfillCodes(sel, ins string, model ...any) error {
	rows, err := s.db.Query(sel, model...)
	if err != nil {
		return err
	}
	type row struct {
		id   int64
		code []byte
	}
	var todo []row
	for rows.Next() {
		var r row
		var b []byte
		if err := rows.Scan(&r.id, &b); err != nil {
			rows.Close()
			return err
		}
		r.code = vindex.Code(decodeVec(b))
		todo = append(todo, r)
	}
	if err := rows.Close(); err != nil || len(todo) == 0 {
		return err
	}
	return s.tx(func(tx *sql.Tx) error {
		for _, r := range todo {
			args := []any{r.id}
			args = append(args, model...)
			args = append(args, r.code)
			if _, err := tx.Exec(ins, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) loadCodes(q string, args ...any) (*vindex.Index, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ix *vindex.Index
	for rows.Next() {
		var id int64
		var code []byte
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		if ix == nil {
			ix = vindex.New(len(code) * 8)
		}
		ix.Add(id, code)
	}
	if ix == nil {
		ix = vindex.New(0)
	}
	return ix, rows.Err()
}

// ClaimCentroidsOf returns the centroids of the given claims.
func (s *Store) ClaimCentroidsOf(ids []int64) (map[int64][]float32, error) {
	out := map[int64][]float32{}
	for i := 0; i < len(ids); i += 500 {
		part := ids[i:min(i+500, len(ids))]
		args := make([]any, len(part))
		ph := make([]byte, 0, 2*len(part))
		for k, id := range part {
			args[k] = id
			if k > 0 {
				ph = append(ph, ',')
			}
			ph = append(ph, '?')
		}
		rows, err := s.db.Query(`SELECT c.id, `+claimCentroid+` FROM claims c WHERE c.id IN (`+string(ph)+`)`, args...)
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
			if b == nil {
				continue
			}
			out[id] = decodeVec(b)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EachClaimCentroid calls fn with every claim's centroid, one at a time,
// so callers that only aggregate don't hold them all.
func (s *Store) EachClaimCentroid(fn func(id int64, v []float32)) error {
	rows, err := s.db.Query(`SELECT c.id, ` + claimCentroid + ` FROM claims c`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return err
		}
		if b == nil {
			continue
		}
		fn(id, decodeVec(b))
	}
	return rows.Err()
}

// EachThreadStatement calls fn with every statement that belongs to a
// thread: its id, item and text. Unscoped: for indexes, never shown.
func (s *Store) EachThreadStatement(fn func(id int64, itemID, text string)) error {
	rows, err := s.db.Query(`SELECT st.id, st.item_id, st.text FROM statements st WHERE st.id IN (SELECT statement_id FROM thread_items)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var item, text string
		if err := rows.Scan(&id, &item, &text); err != nil {
			return err
		}
		fn(id, item, text)
	}
	return rows.Err()
}
