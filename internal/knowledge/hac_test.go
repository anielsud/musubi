package knowledge

import "testing"

func TestHACGroupsAndStops(t *testing.T) {
	// Two tight pairs far apart, and a loner.
	vs := [][]float32{
		normalized([]float32{1, 0, 0}), normalized([]float32{0.95, 0.05, 0}),
		normalized([]float32{0, 1, 0}), normalized([]float32{0.05, 0.95, 0}),
		normalized([]float32{0, 0, 1}),
	}
	cl := hac(vs, 0.9, nil)
	if len(cl) != 3 {
		t.Fatalf("got %d clusters %v, want 3", len(cl), cl)
	}
	sizes := map[int]int{}
	for _, c := range cl {
		sizes[len(c)]++
	}
	if sizes[2] != 2 || sizes[1] != 1 {
		t.Errorf("cluster sizes %v", cl)
	}
	if got := hac(vs, 0.0, nil); len(got) != 1 {
		t.Errorf("threshold 0 should merge everything, got %d", len(got))
	}
}
