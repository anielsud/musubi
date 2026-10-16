package vindex

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

func unit(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var n float64
	for i := range v {
		v[i] = float32(r.NormFloat64())
		n += float64(v[i]) * float64(v[i])
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(n))
	}
	return v
}

func cos(a, b []float32) float64 {
	var d float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
	}
	return d
}

// Clustered vectors; a query near a stored one. The exact top 10 should be
// among the 400 nearest codes.
func TestNearestRecall(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	const dim, n = 768, 5000
	centers := make([][]float32, 50)
	for i := range centers {
		centers[i] = unit(r, dim)
	}
	vs := make([][]float32, n)
	x := New(dim)
	for i := range vs {
		c := centers[i%len(centers)]
		v := make([]float32, dim)
		for j := range v {
			v[j] = c[j] + float32(r.NormFloat64()*0.8/math.Sqrt(dim))
		}
		vs[i] = v
		x.Add(int64(i), Code(v))
	}
	if x.Len() != n {
		t.Fatalf("len %d", x.Len())
	}
	hits := 0
	for qi := 0; qi < 20; qi++ {
		q := vs[r.Intn(n)]
		ids := make([]int, n)
		for i := range ids {
			ids[i] = i
		}
		sort.Slice(ids, func(a, b int) bool { return cos(q, vs[ids[a]]) > cos(q, vs[ids[b]]) })
		got := map[int64]bool{}
		for _, id := range x.Nearest(q, 400) {
			got[id] = true
		}
		for _, id := range ids[:10] {
			if got[int64(id)] {
				hits++
			}
		}
	}
	if hits < 195 {
		t.Errorf("recall@10 within 400 candidates: %d/200", hits)
	}
	// Nearest first: the query's own vector leads.
	if got := x.Nearest(vs[7], 3); got[0] != 7 {
		t.Errorf("nearest to a stored vector: %v", got)
	}
	if got := x.Nearest(vs[0], n+10); len(got) != n {
		t.Errorf("k above size: %d", len(got))
	}
}

func TestCodeBits(t *testing.T) {
	c := Code([]float32{1, -1, 0, 2, -3, 4, 5, -6, 7})
	if len(c) != 8 || c[0] != 0b01101001 || c[1] != 1 {
		t.Errorf("code %08b", c[:2])
	}
}
