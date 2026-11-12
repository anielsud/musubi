package store

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
)

// Stored vectors come in two encodings, told apart by length so old blobs
// keep reading without a schema change:
//
//   - f32: 4 bytes a component, little-endian (sqlite-vec's format); the
//     length is a multiple of 4.
//   - f16: one tag byte (vecTagF16) then 2 bytes a component, IEEE half
//     precision, little-endian; the length is odd.
//
// Every read decodes to float32 for scoring. f16 halves the vectors' space
// (a 768-d vector is 1.5 KB, two to a 4 KB page instead of one); what it
// costs in ranking was measured before it became the default
// (docs/research-log.md, 2026-11-12).
const (
	VecF32    = "f32"
	VecF16    = "f16"
	vecTagF16 = 0x16
)

// encodeVec writes v in the store's encoding. An empty vector is nil
// (stored as NULL), as sqlite_vec.SerializeFloat32 made it.
func (s *Store) encodeVec(v []float32) []byte {
	return encodeVecAs(v, s.VectorEncoding)
}

func encodeVecAs(v []float32, enc string) []byte {
	if len(v) == 0 {
		return nil
	}
	if enc == VecF16 {
		b := make([]byte, 1+2*len(v))
		b[0] = vecTagF16
		for i, x := range v {
			binary.LittleEndian.PutUint16(b[1+2*i:], f32ToF16(x))
		}
		return b
	}
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// decodeVec reads either encoding.
func decodeVec(b []byte) []float32 {
	if len(b)%2 == 1 && b[0] == vecTagF16 {
		v := make([]float32, (len(b)-1)/2)
		for i := range v {
			v[i] = f16Table[binary.LittleEndian.Uint16(b[1+2*i:])]
		}
		return v
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		u := uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24
		v[i] = float32frombits(u)
	}
	return v
}

// SetVectorEncoding sets how vectors are written (store.vector_encoding):
// "f32" or "" for full precision, "f16" for half.
func (s *Store) SetVectorEncoding(enc string) error {
	if enc != "" && enc != VecF32 && enc != VecF16 {
		return fmt.Errorf("store.vector_encoding: %q is not f32 or f16", enc)
	}
	s.VectorEncoding = enc
	return nil
}

var f16Table = func() *[1 << 16]float32 {
	var t [1 << 16]float32
	for i := range t {
		t[i] = f16ToF32(uint16(i))
	}
	return &t
}()

// f32ToF16 rounds to the nearest half, ties to even; out-of-range values
// become infinities, tiny ones subnormals or zero.
func f32ToF16(f float32) uint16 {
	u := math.Float32bits(f)
	sign := uint16(u>>16) & 0x8000
	exp := int32(u>>23) & 0xff
	man := u & 0x7fffff
	if exp == 0xff { // Inf or NaN
		if man != 0 {
			return sign | 0x7e00
		}
		return sign | 0x7c00
	}
	e := exp - 127 + 15
	if e >= 0x1f {
		return sign | 0x7c00
	}
	if e <= 0 {
		if e < -10 {
			return sign
		}
		man |= 0x800000
		shift := uint32(14 - e)
		half := man >> shift
		rem := man & (1<<shift - 1)
		mid := uint32(1) << (shift - 1)
		if rem > mid || (rem == mid && half&1 == 1) {
			half++
		}
		return sign | uint16(half)
	}
	half := uint32(e)<<10 | man>>13
	rem := man & 0x1fff
	if rem > 0x1000 || (rem == 0x1000 && half&1 == 1) {
		half++ // may carry into the exponent, up to infinity: correct
	}
	return sign | uint16(half)
}

func f16ToF32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	man := uint32(h & 0x3ff)
	switch {
	case exp == 0 && man == 0:
		return math.Float32frombits(sign)
	case exp == 0: // subnormal
		e := uint32(127 - 15 + 1)
		for man&0x400 == 0 {
			man <<= 1
			e--
		}
		return math.Float32frombits(sign | e<<23 | (man&0x3ff)<<13)
	case exp == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | man<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | man<<13)
}

// cosineDistance32 is sqlite-vec's vec_distance_cosine in Go, for passage
// vectors stored as f16 (which sqlite-vec can't read): float32 sums, the
// division in double.
func cosineDistance32(a, b []float32) float64 {
	var dot, na, nb float32
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		dot += float32(a[i] * b[i])
		na += float32(a[i] * a[i])
		nb += float32(b[i] * b[i])
	}
	return float64(float32(1 - float64(dot)/(math.Sqrt(float64(na))*math.Sqrt(float64(nb)))))
}

// claimCentroid is a claim's centroid (claims aliased c): stored only for
// claims of two or more statements; a claim of one statement has that
// statement's vector, read from statement_vecs rather than stored twice
// (99.7% of claims, 2026-11-12). NULL centroid ⇒ one statement: JoinClaim
// writes the centroid whenever a statement joins, and PutStatementVec keeps
// the old vector on the claim before replacing it.
const claimCentroid = `coalesce(c.centroid, (SELECT v0.vec FROM statement_claims sc0 JOIN statement_vecs v0 ON v0.statement_id = sc0.statement_id WHERE sc0.claim_id = c.id LIMIT 1))`

// metaChunkF16 marks that chunk_vecs may hold f16 vectors, so passage
// search scores in Go (rankMixed); unset, it runs sqlite-vec's query
// exactly as before.
const metaChunkF16 = "chunk_vecs_f16"

func metaSet(q interface {
	Exec(string, ...any) (sql.Result, error)
}, key, value string) error {
	_, err := q.Exec(`INSERT INTO store_meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) metaGet(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM store_meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// upgradeClaimCentroids drops the centroids that only repeat their single
// statement's vector, byte for byte, so every read gets the same vector as
// before. Once per database (store_meta).
func upgradeClaimCentroids(db *sql.DB) error {
	var done int
	if err := db.QueryRow(`SELECT count(*) FROM store_meta WHERE key = 'claim_centroids_dedup'`).Scan(&done); err != nil || done > 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE claims SET centroid = NULL WHERE centroid IS NOT NULL
		AND (SELECT count(*) FROM statement_claims sc WHERE sc.claim_id = claims.id) = 1
		AND centroid = (SELECT v.vec FROM statement_claims sc JOIN statement_vecs v ON v.statement_id = sc.statement_id WHERE sc.claim_id = claims.id)`); err != nil {
		return err
	}
	if err := metaSet(tx, "claim_centroids_dedup", "1"); err != nil {
		return err
	}
	return tx.Commit()
}

// EncodeStats counts what ReencodeVectors rewrote.
type EncodeStats struct {
	Table     string
	Rows      int
	Rewritten int
}

// ReencodeVectors rewrites every stored vector (passages, statements, claim
// centroids, thread centroids) in enc. f32 → f16 rounds each component to
// half precision; f16 → f32 only widens the rounded values: the original
// float32 vectors are gone until re-embedded. Claim centroids left NULL
// keep following their statement. The file doesn't shrink until VACUUM.
func (s *Store) ReencodeVectors(enc string, progress func(EncodeStats)) ([]EncodeStats, error) {
	if enc != VecF32 && enc != VecF16 {
		return nil, fmt.Errorf("unknown vector encoding %q (f32 or f16)", enc)
	}
	tables := []struct{ name, col string }{
		{"chunk_vecs", "vec"}, {"statement_vecs", "vec"}, {"claims", "centroid"}, {"topics", "centroid"},
	}
	var out []EncodeStats
	if enc == VecF16 {
		// Set first: a passage search during the rewrite sees both kinds.
		if err := metaSet(s.db, metaChunkF16, "1"); err != nil {
			return nil, err
		}
	}
	for _, t := range tables {
		st := EncodeStats{Table: t.name}
		var last int64 = -1
		for {
			rows, err := s.db.Query(fmt.Sprintf(`SELECT rowid, %s FROM %s WHERE rowid > ? AND %s IS NOT NULL ORDER BY rowid LIMIT 2000`, t.col, t.name, t.col), last)
			if err != nil {
				return out, err
			}
			type upd struct {
				id int64
				b  []byte
			}
			var todo []upd
			n := 0
			for rows.Next() {
				var id int64
				var b []byte
				if err := rows.Scan(&id, &b); err != nil {
					rows.Close()
					return out, err
				}
				n++
				last = id
				if len(b) == 0 {
					continue
				}
				nb := encodeVecAs(decodeVec(b), enc)
				if string(nb) != string(b) {
					todo = append(todo, upd{id, nb})
				}
			}
			if err := rows.Close(); err != nil {
				return out, err
			}
			if n == 0 {
				break
			}
			st.Rows += n
			if len(todo) > 0 {
				err := s.tx(func(tx *sql.Tx) error {
					for _, u := range todo {
						if _, err := tx.Exec(fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, t.name, t.col), u.b, u.id); err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					return out, err
				}
				st.Rewritten += len(todo)
			}
			if progress != nil {
				progress(st)
			}
		}
		out = append(out, st)
	}
	if enc == VecF32 {
		var left int
		if err := s.db.QueryRow(`SELECT count(*) FROM chunk_vecs WHERE length(vec) % 2 = 1`).Scan(&left); err != nil {
			return out, err
		}
		if left == 0 {
			if _, err := s.db.Exec(`DELETE FROM store_meta WHERE key = ?`, metaChunkF16); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// Vacuum rebuilds the database file so freed pages are returned.
func (s *Store) Vacuum() error {
	_, err := s.db.Exec(`VACUUM`)
	return err
}
