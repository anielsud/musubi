//go:build darwin

package meeting

import "testing"

func TestStreamChangeClassifiers(t *testing.T) {
	type check struct {
		name string
		fn   func(int, int) bool
		fac  int
		ev   int
	}
	// Each classifier accepts exactly one facility/event pair.
	all := []check{
		{"mic new", isSourceOutputNew, paFacilitySourceOutput, paEventNew},
		{"mic remove", isSourceOutputRemove, paFacilitySourceOutput, paEventRemove},
		{"speaker new", isSinkInputNew, paFacilitySinkInput, paEventNew},
		{"speaker remove", isSinkInputRemove, paFacilitySinkInput, paEventRemove},
	}
	for _, c := range all {
		for _, other := range all {
			want := c.fac == other.fac && c.ev == other.ev
			if got := c.fn(other.fac, other.ev); got != want {
				t.Errorf("%s(%s pair) = %v, want %v", c.name, other.name, got, want)
			}
		}
	}
}

func TestStoppedDetectorIgnoresChanges(t *testing.T) {
	d := NewDetector()
	d.stopped = true
	d.candidatePID = 11
	for _, fac := range []int{paFacilitySourceOutput, paFacilitySinkInput} {
		for _, ev := range []int{paEventNew, paEventRemove} {
			d.onSubscribeEvent(fac, ev, 0)
		}
	}
	if d.candidatePID != 11 || d.current != nil {
		t.Error("stopped detector reacted to a stream change")
	}
}
