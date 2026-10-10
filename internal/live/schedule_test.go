package live

import "testing"

func TestScheduleDraftsThenFinalAfterGap(t *testing.T) {
	segs := []Segment{
		{ID: "a", Source: "mic", Start: 0, End: 3, Text: "Hello there, Acme.", Words: []Word{{"Hello", 0, 1}, {"there,", 1, 2}, {"Acme.", 2, 3}}},
		{ID: "b", Source: "monitor", Start: 3.5, End: 5, Text: "Hi.", Words: []Word{{"Hi.", 3.5, 5}}},
	}
	s := buildSchedule(segs)
	var drafts []string
	var finalA float64
	for _, e := range s {
		if e.Seg.ID == "a" && e.Seg.Status == "live" {
			drafts = append(drafts, e.Seg.Text)
		}
		if e.Seg.ID == "a" && e.Seg.Status == "" {
			finalA = e.At
		}
	}
	if len(drafts) != 2 || drafts[0] != "hello" || drafts[1] != "hello there" {
		t.Errorf("drafts = %q", drafts)
	}
	// The other side starts at 3.5, before the 2 s gap would close the turn.
	if want := 3.5 + 0.3; finalA < want-0.01 || finalA > want+0.01 {
		t.Errorf("final at %.2f, want %.2f", finalA, want)
	}
	for i := 1; i < len(s); i++ {
		if s[i].At < s[i-1].At {
			t.Fatal("schedule not in time order")
		}
	}
}

func TestDropOwner(t *testing.T) {
	qs := []Suggestion{{Query: "anil sud meeting schedule"}, {Query: "Aniel's notes"}, {Query: "patch deadline for the bank"}, {Query: "Sudan trip"}}
	keep, dropped := dropOwner(qs, "Aniel Sud")
	if len(keep) != 2 || keep[0].Query != "patch deadline for the bank" || keep[1].Query != "Sudan trip" {
		t.Errorf("keep = %v, dropped = %v", keep, dropped)
	}
}
