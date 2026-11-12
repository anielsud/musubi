package knowledge

import "testing"

// Nearby: threads linked to someone's, not theirs, most linked first. A
// link by a name on everything, or by likeness alone, doesn't count; one
// the scope can't name is left out.
func TestNearby(t *testing.T) {
	var ts []MapTopic
	for id, label := range []string{"", "Renewal", "Pricing", "Migration plan", "Hiring", "Board prep", "Roadmap", "Offsite"} {
		if id > 0 {
			ts = append(ts, MapTopic{TopicSummary: TopicSummary{ID: int64(id), Label: label, Hidden: label == "Board prep"}})
		}
	}
	m := &TopicMap{Topics: ts, Edges: []TopicEdge{
		{A: 1, B: 3, Kind: "similar", Sim: 0.9, Together: 2},
		{A: 3, B: 2, Kind: "same", Sim: 0.9, Via: []string{"Acme"}},
		{A: 1, B: 4, Kind: "together", Sim: 0.9, Together: 4},
		{A: 1, B: 2, Kind: "shared", Shared: 3},              // both theirs
		{A: 2, B: 5, Kind: "similar", Sim: 0.9, Together: 5}, // hidden label
		{A: 2, B: 6, Kind: "similar", Sim: 0.95},             // alike, never together
		{A: 1, B: 7, Kind: "same", Sim: 0.9, Via: []string{"Bigco"}},
		// Bigco is on everything.
		{A: 3, B: 4, Kind: "same", Via: []string{"Bigco"}}, {A: 3, B: 5, Kind: "same", Via: []string{"Bigco"}},
		{A: 4, B: 5, Kind: "same", Via: []string{"Bigco"}}, {A: 6, B: 7, Kind: "same", Via: []string{"Bigco"}},
	}}
	got := Nearby(m, []int64{1, 2}, 5)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 4 {
		t.Fatalf("nearby = %+v, want 3 then 4", got)
	}
	if got[0].Links != 2 || got[0].Kind != "together" || got[0].From != "Renewal" {
		t.Errorf("3: %+v, want linked to both, closest to Renewal (discussed together)", got[0])
	}
	if len(Nearby(m, []int64{1, 2}, 1)) != 1 {
		t.Error("limit not applied")
	}
}
