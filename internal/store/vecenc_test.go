package store

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

func TestF16RoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	v := make([]float32, 768)
	for i := range v {
		v[i] = float32(r.NormFloat64() * 0.05)
	}
	v = append(v, 0, float32(math.Copysign(0, -1)), 1, -1, 65504, 1e-7, 6e-8, 0.1)
	b := encodeVecAs(v, VecF16)
	if len(b) != 1+2*len(v) {
		t.Fatalf("f16 blob is %d bytes", len(b))
	}
	got := decodeVec(b)
	for i, x := range v {
		// Half precision keeps 11 significant bits: relative error ≤ 2⁻¹¹
		// for normal values, absolute ≤ 2⁻²⁵ for subnormal ones.
		if d := math.Abs(float64(got[i] - x)); d > math.Abs(float64(x))/2048+math.Ldexp(1, -25) {
			t.Errorf("component %d: %g read back as %g", i, x, got[i])
		}
	}
	// Every half value converts back to itself.
	for h := 0; h < 1<<16; h++ {
		f := f16ToF32(uint16(h))
		if f != f { // NaN
			continue
		}
		if back := f32ToF16(f); back != uint16(h) {
			t.Fatalf("half %#04x → %g → %#04x", h, f, back)
		}
	}
	// Ties round to even.
	if got := f32ToF16(1 + 1.0/2048); got != 0x3c00 {
		t.Errorf("1+2⁻¹¹ → %#04x, want 0x3c00", got)
	}
	if got := f32ToF16(1 + 3.0/2048); got != 0x3c02 {
		t.Errorf("1+3·2⁻¹¹ → %#04x, want 0x3c02", got)
	}
	// f32 is byte for byte what sqlite-vec writes, and an empty vector
	// stays NULL as it was.
	want, _ := sqlite_vec.SerializeFloat32(v)
	if !bytes.Equal(encodeVecAs(v, VecF32), want) || !bytes.Equal(encodeVecAs(v, ""), want) {
		t.Error("f32 encoding differs from sqlite_vec.SerializeFloat32")
	}
	if encodeVecAs(nil, VecF16) != nil || encodeVecAs([]float32{}, VecF32) != nil {
		t.Error("an empty vector must encode as nil")
	}
}

// claimStore makes a store with n statements in one meeting, each with a
// vector, and no claims yet.
func claimStore(t *testing.T, path string, vecs [][]float32) (*Store, []Statement) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := "t:claims"
	it := item.Item{ID: id, Source: "t", Ref: "claims", Kind: item.KindMeeting, Title: "planning", OccurredAt: time.Unix(86400, 0),
		Segments: []item.Segment{{Text: "said"}}}
	if _, err := s.UpsertItem(it); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceChunks(id, "v", "h", []Chunk{{Seq: 0, SegTo: 1, Text: "said"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLabels(id, "v", "h", sensitivity.Label{}, nil, []string{"owner@corp.example"}); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.Chunks(id)
	var sts []Statement
	for i := range vecs {
		sts = append(sts, Statement{ChunkID: cs[0].ID, Kind: "fact", Text: fmt.Sprintf("statement %d", i), Holder: "Lee", SaidAt: it.OccurredAt, Status: "stated"})
	}
	if err := s.ReplaceStatements(id, "v", "h", sts); err != nil {
		t.Fatal(err)
	}
	un, _ := s.UnlinkedStatements()
	for i, st := range un {
		if err := s.PutStatementVec(st.ID, "m", vecs[i]); err != nil {
			t.Fatal(err)
		}
	}
	return s, un
}

func storedCentroid(t *testing.T, s *Store, claim int64) []byte {
	t.Helper()
	var b []byte
	if err := s.db.QueryRow(`SELECT centroid FROM claims WHERE id = ?`, claim).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sameVec(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Float32bits(a[i]) != math.Float32bits(b[i]) {
			return false
		}
	}
	return true
}

// A claim of one statement stores no centroid and reads its statement's
// vector everywhere centroids are read; a second statement joining writes
// the centroid; replacing the statement's vector keeps the claim's.
func TestClaimCentroidFromStatement(t *testing.T) {
	for _, enc := range []string{VecF32, VecF16} {
		t.Run(enc, func(t *testing.T) {
			va, vb := []float32{0.6, 0.8, 0}, []float32{0, 0.8, 0.6}
			s, sts := claimStore(t, filepath.Join(t.TempDir(), "t.db"), [][]float32{va, vb})
			defer s.Close()
			s.SetVectorEncoding(enc)
			// Re-store the vectors in this encoding (the store was filled as f32).
			for i, v := range [][]float32{va, vb} {
				if err := s.PutStatementVec(sts[i].ID, "m", v); err != nil {
					t.Fatal(err)
				}
			}
			want := decodeVec(encodeVecAs(va, enc))
			id, err := s.NewClaim(sts[0], want)
			if err != nil {
				t.Fatal(err)
			}
			if b := storedCentroid(t, s, id); b != nil {
				t.Fatalf("single-statement claim stored a %d-byte centroid", len(b))
			}
			check := func(want []float32) {
				t.Helper()
				all, _ := s.ClaimCentroids()
				of, _ := s.ClaimCentroidsOf([]int64{id})
				var each []float32
				s.EachClaimCentroid(func(c int64, v []float32) {
					if c == id {
						each = v
					}
				})
				rows, _ := s.Claims()
				if len(rows) != 1 || !sameVec(rows[0].Centroid, want) || !sameVec(all[id], want) || !sameVec(of[id], want) || !sameVec(each, want) {
					t.Errorf("centroid reads differ from %v: Claims %v, ClaimCentroids %v, Of %v, Each %v", want, rows, all[id], of[id], each)
				}
			}
			check(want)
			ix, err := s.ClaimIndex()
			if err != nil || ix.Len() != 1 {
				t.Errorf("claim index holds %d (err %v)", ix.Len(), err)
			}

			// Replacing the statement's vector keeps the claim on the old one.
			if err := s.PutStatementVec(sts[0].ID, "m", []float32{1, 0, 0}); err != nil {
				t.Fatal(err)
			}
			if storedCentroid(t, s, id) == nil {
				t.Error("re-embedding the statement left the claim following the new vector")
			}
			check(want)

			// A second statement joining writes the centroid.
			mean := []float32{0.3, 0.8, 0.3}
			if err := s.JoinClaim(sts[1].ID, id, 0.9, "judged", mean); err != nil {
				t.Fatal(err)
			}
			if b := storedCentroid(t, s, id); !bytes.Equal(b, encodeVecAs(mean, enc)) {
				t.Errorf("joined claim stored %v", decodeVec(b))
			}
			check(decodeVec(encodeVecAs(mean, enc)))
		})
	}
}

// The upgrade drops only centroids equal byte for byte to their single
// statement's vector, and every read returns the same vectors after it.
func TestClaimCentroidUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	v0, v1, v2, v3 := []float32{1, 0, 0}, []float32{0, 1, 0}, []float32{0, 0, 1}, []float32{0, 0.6, 0.8}
	s, sts := claimStore(t, path, [][]float32{v0, v1, v2, v3})
	// Claims as the earlier code stored them: every centroid written.
	put := func(cen []float32, members ...Statement) int64 {
		b, _ := sqlite_vec.SerializeFloat32(cen)
		res, err := s.db.Exec(`INSERT INTO claims(kind, created_at, centroid) VALUES ('fact', 0, ?)`, b)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		for _, m := range members {
			if _, err := s.db.Exec(`INSERT INTO statement_claims(statement_id, claim_id, sim, how) VALUES (?,?,1,'new')`, m.ID, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	same := put(v0, sts[0])                        // its one statement's vector
	drifted := put([]float32{0, 0.9, 0.1}, sts[1]) // one statement, another vector
	pair := put(v2, sts[2], sts[3])                // equal to a member's, but two members

	before, _ := s.ClaimCentroids()
	// The database as before the upgrade.
	if _, err := s.db.Exec(`DELETE FROM store_meta WHERE key = 'claim_centroids_dedup'`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nulls := map[int64]bool{}
	rows, _ := s.db.Query(`SELECT id, centroid IS NULL FROM claims`)
	for rows.Next() {
		var id int64
		var n bool
		rows.Scan(&id, &n)
		nulls[id] = n
	}
	rows.Close()
	if !nulls[same] || nulls[drifted] || nulls[pair] {
		t.Errorf("NULL centroids after the upgrade: %v (want only claim %d)", nulls, same)
	}
	after, _ := s.ClaimCentroids()
	if len(after) != len(before) {
		t.Fatalf("%d centroids before, %d after", len(before), len(after))
	}
	for id, v := range before {
		if !sameVec(after[id], v) {
			t.Errorf("claim %d: %v before, %v after", id, v, after[id])
		}
	}
	// Once only: a centroid stored again later stays.
	s.db.Exec(`UPDATE claims SET centroid = ? WHERE id = ?`, encodeVecAs(v0, VecF32), same)
	if err := upgradeClaimCentroids(s.db); err != nil {
		t.Fatal(err)
	}
	if storedCentroid(t, s, same) == nil {
		t.Error("the upgrade ran twice")
	}
}

// Passage and statement vectors stored partly as f32 and partly as f16 all
// read, and passage search ranks across both; re-encoding converts every
// vector and back.
func TestMixedEncodings(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "t:mixed"
	if _, err := s.UpsertItem(item.Item{ID: id, Source: "t", Ref: "m", Kind: item.KindEmail, Title: "x", OccurredAt: time.Unix(1, 0),
		Segments: []item.Segment{{Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceChunks(id, "v", "h", []Chunk{{Seq: 0, SegTo: 1, Text: "a"}, {Seq: 1, SegTo: 1, Text: "b"}, {Seq: 2, SegTo: 1, Text: "c"}, {Seq: 3, SegTo: 1, Text: "d"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLabels(id, "v", "h", sensitivity.Label{}, nil, []string{"owner@corp.example"}); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.Chunks(id)
	vs := [][]float32{{1, 0, 0}, {0.9, 0.1, 0}, {0.5, 0.5, 0}, {0, 0, 1}}
	all := map[int64][]float32{}
	for i, c := range cs {
		all[c.ID] = vs[i]
	}
	if err := s.PutVecs(id, "embed", "m", "v", "h", all); err != nil {
		t.Fatal(err)
	}
	q := SearchQuery{Vec: []float32{1, 0.05, 0}, Model: "m"}
	want, err := s.vectorIDs(sensitivity.Owner(), q, 4)
	if err != nil {
		t.Fatal(err)
	}
	// Chunks b and d as f16, as a database part way through re-encoding.
	s.metaSetForTest(t)
	for _, i := range []int{1, 3} {
		if _, err := s.db.Exec(`UPDATE chunk_vecs SET vec = ? WHERE chunk_id = ?`, encodeVecAs(vs[i], VecF16), cs[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.vectorIDs(sensitivity.Owner(), q, 4)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("mixed ranking %v, f32 ranking %v", got, want)
	}
	if got, _ := s.vectorIDs(sensitivity.Owner(), q, 2); fmt.Sprint(got) != fmt.Sprint(want[:2]) {
		t.Errorf("mixed top 2 %v, want %v", got, want[:2])
	}

	stats, err := s.ReencodeVectors(VecF16, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats[0].Table != "chunk_vecs" || stats[0].Rows != 4 || stats[0].Rewritten != 2 {
		t.Errorf("f16: %+v", stats)
	}
	var odd int
	s.db.QueryRow(`SELECT count(*) FROM chunk_vecs WHERE length(vec) = 7`).Scan(&odd)
	if odd != 4 {
		t.Errorf("%d of 4 passage vectors are f16", odd)
	}
	if got, _ := s.vectorIDs(sensitivity.Owner(), q, 4); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("f16 ranking %v, f32 ranking %v", got, want)
	}
	if _, err := s.ReencodeVectors(VecF32, nil); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.metaGet(metaChunkF16); f != "" {
		t.Error("f16 marker left after converting everything back to f32")
	}
	var even int
	s.db.QueryRow(`SELECT count(*) FROM chunk_vecs WHERE length(vec) = 12`).Scan(&even)
	if even != 4 {
		t.Errorf("%d of 4 passage vectors are f32", even)
	}
	if got, _ := s.vectorIDs(sensitivity.Owner(), q, 4); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("back to f32: ranking %v, want %v", got, want)
	}
}

func (s *Store) metaSetForTest(t *testing.T) {
	t.Helper()
	if err := metaSet(s.db, metaChunkF16, "1"); err != nil {
		t.Fatal(err)
	}
}
