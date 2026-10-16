package store

import (
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Vector search through the code index returns what an exact scan would,
// under the scope: a confidential item's passages, nearest to the query,
// never reach a shared screen, and the visible results are still the
// nearest visible ones.
func TestIndexedVectorSearch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := rand.New(rand.NewSource(1))
	const dim, per = 64, 1200 // over exactBelow in total
	unit := func(v []float32) []float32 {
		var n float64
		for _, x := range v {
			n += float64(x) * float64(x)
		}
		for i := range v {
			v[i] /= float32(math.Sqrt(n))
		}
		return v
	}
	center := make([]float32, dim)
	for i := range center {
		center[i] = float32(r.NormFloat64())
	}
	vecs := map[int64][]float32{}
	level := map[int64]sensitivity.Level{}
	for k, lvl := range []sensitivity.Level{sensitivity.Internal, sensitivity.Confidential} {
		id := fmt.Sprintf("t:%d", k)
		if _, err := s.UpsertItem(item.Item{ID: id, Source: "t", Ref: id, Kind: item.KindEmail, Title: "x", OccurredAt: time.Unix(int64(k), 0),
			Segments: []item.Segment{{Text: "x"}}}); err != nil {
			t.Fatal(err)
		}
		chunks := make([]Chunk, per)
		for i := range chunks {
			chunks[i] = Chunk{Seq: i, SegTo: 1, Text: "probe"}
		}
		if err := s.ReplaceChunks(id, "v", "h", chunks); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceLabels(id, "v", "h", sensitivity.Label{Level: lvl}, nil, []string{"owner@corp.example"}); err != nil {
			t.Fatal(err)
		}
		cs, _ := s.Chunks(id)
		vs := map[int64][]float32{}
		for _, c := range cs {
			// The confidential item sits nearer the query's region.
			spread := 1.0
			if lvl == sensitivity.Confidential {
				spread = 0.5
			}
			v := make([]float32, dim)
			for i := range v {
				v[i] = center[i] + float32(r.NormFloat64()*spread)
			}
			vs[c.ID] = unit(v)
			vecs[c.ID], level[c.ID] = vs[c.ID], lvl
		}
		if err := s.PutVecs(id, "embed", "m", "v", "h", vs); err != nil {
			t.Fatal(err)
		}
	}
	q := unit(append([]float32(nil), center...))
	exact := func(sc sensitivity.Scope) []int64 {
		var ids []int64
		for id := range vecs {
			if level[id] <= sc.MaxLevel {
				ids = append(ids, id)
			}
		}
		dot := func(id int64) float64 {
			var d float64
			for i := range q {
				d += float64(q[i]) * float64(vecs[id][i])
			}
			return d
		}
		sort.Slice(ids, func(a, b int) bool { return dot(ids[a]) > dot(ids[b]) })
		return ids[:10]
	}
	for _, sc := range []sensitivity.Scope{sensitivity.Owner(), sensitivity.SharedScreen()} {
		got, err := s.vectorIDs(sc, SearchQuery{Vec: q, Model: "m"}, 10)
		if err != nil {
			t.Fatal(err)
		}
		want := exact(sc)
		match := 0
		for i := range got {
			if level[got[i]] > sc.MaxLevel {
				t.Fatalf("%s: passage %d above the scope", sc.Name, got[i])
			}
			for _, w := range want {
				if got[i] == w {
					match++
				}
			}
		}
		if len(got) != 10 || match < 9 {
			t.Errorf("%s: %d results, %d of the exact top 10", sc.Name, len(got), match)
		}
	}
	ix, _ := s.passageIndex("m")
	if ix.Len() != 2*per {
		t.Errorf("index holds %d codes", ix.Len())
	}
}
