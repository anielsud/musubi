package speaker

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"same vector", []float32{0.5, -1, 2, 4}, []float32{0.5, -1, 2, 4}, 1},
		{"same direction, other length", []float32{3, 0, 4}, []float32{0.6, 0, 0.8}, 1},
		{"reversed", []float32{2, -1}, []float32{-2, 1}, -1},
		{"at right angles", []float32{0, 3, 0}, []float32{5, 0, 0}, 0},
		{"45 degrees", []float32{1, 0}, []float32{1, 1}, 1 / math.Sqrt2},
		// No fingerprint, or one that can't be compared, must never
		// look like a match.
		{"zero vector", []float32{0, 0}, []float32{1, 1}, 0},
		{"both empty", nil, nil, 0},
		{"lengths differ", []float32{1, 1}, []float32{1, 1, 1}, 0},
	}
	for _, c := range cases {
		got := CosineSimilarity(c.a, c.b)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s: CosineSimilarity(%v, %v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
		if back := CosineSimilarity(c.b, c.a); back != got {
			t.Errorf("%s: not symmetric (%v vs %v)", c.name, got, back)
		}
	}
}
