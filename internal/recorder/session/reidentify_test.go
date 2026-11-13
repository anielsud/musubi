package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRelabelPicksLargestOverlap(t *testing.T) {
	segs := []Segment{
		{Speaker: "You", Source: "mic", StartTime: 0, EndTime: 4},
		{Speaker: "Person 7", Source: "monitor", StartTime: 1, EndTime: 4},   // 1s with 3, 2s with 8
		{Speaker: "Person 7", Source: "monitor", StartTime: 10, EndTime: 11}, // nothing overlaps
		{Speaker: "Person 7", Source: "monitor", StartTime: 2, EndTime: 2},   // zero length
	}
	diar := []DiarizeSegment{{Start: 0, End: 2, Speaker: 3}, {Start: 2, End: 6, Speaker: 8}}
	n := relabelByDiarization(segs, diar, map[int]string{3: "Person 1", 8: "Person 2"}, false)
	want := []string{"You", "Person 2", "Person 7", "Person 7"}
	if n != 1 || !slices.Equal(speakers(segs), want) {
		t.Errorf("relabeled %d: %q, want 1: %q", n, speakers(segs), want)
	}
}

// With equal overlaps the earlier diarization turn wins.
func TestRelabelOverlapTieGoesToFirstTurn(t *testing.T) {
	segs := []Segment{{Speaker: "x", Source: "monitor", StartTime: 1, EndTime: 3}}
	diar := []DiarizeSegment{{Start: 0, End: 2, Speaker: 5}, {Start: 2, End: 4, Speaker: 6}}
	assigned, _ := DiarizationLabels(segs, diar, map[int]string{5: "Person 1", 6: "Person 2"})
	if assigned[0] != 5 {
		t.Errorf("assigned %d, want 5", assigned[0])
	}
}

func TestRelabelCarriesHintNames(t *testing.T) {
	segs := []Segment{
		{Speaker: "Person 4 (Robin Ch)", Source: "monitor", StartTime: 0, EndTime: 3},
		{Speaker: "Person 2", LiveSpeaker: "Person 4 (Robin Chen)", Source: "monitor", StartTime: 3, EndTime: 6},
		{Speaker: "Person 9", Source: "monitor", StartTime: 6, EndTime: 7},
		{Speaker: "Person 1 (Dee)", Source: "monitor", StartTime: 20, EndTime: 21},
		{Speaker: "Person 1 (Max)", Source: "monitor", StartTime: 21, EndTime: 24},
		{Speaker: "Person 3", Source: "monitor", StartTime: 30, EndTime: 31},
	}
	diar := []DiarizeSegment{
		{Start: 0, End: 8, Speaker: 0},
		{Start: 19, End: 25, Speaker: 1},
		{Start: 29, End: 32, Speaker: 2},
	}
	RelabelByDiarization(segs, diar, map[int]string{0: "Person 1", 1: "Person 2", 2: "Person 3"})
	want := []string{
		"Person 1 (Robin Chen)", "Person 1 (Robin Chen)", "Person 1 (Robin Chen)", // tie: longer name
		"Person 2 (Max)", "Person 2 (Max)", // more speaking time
		"Person 3", // no name to carry
	}
	if !slices.Equal(speakers(segs), want) {
		t.Errorf("speakers = %q\nwant %q", speakers(segs), want)
	}
}

func TestMostHeardNameTies(t *testing.T) {
	cases := []struct {
		in   map[string]float64
		want string
	}{
		{nil, ""},
		{map[string]float64{"Al": 1, "Bea": 2}, "Bea"},
		{map[string]float64{"Al": 2, "Alan": 2}, "Alan"},
		{map[string]float64{"Cy": 2, "Bo": 2, "Di": 2}, "Bo"},
		{map[string]float64{"Zed": 0}, "Zed"},
	}
	for _, c := range cases {
		for range 5 { // map order varies; the answer must not
			if got := mostHeardName(c.in); got != c.want {
				t.Errorf("mostHeardName(%v) = %q, want %q", c.in, got, c.want)
			}
		}
	}
}

func TestDiarizableAndHintName(t *testing.T) {
	cases := []struct {
		seg  Segment
		want bool
	}{
		{Segment{Speaker: "Person 1", Source: "monitor"}, true},
		{Segment{Speaker: "Person 1", Source: "mic"}, false},
		{Segment{Speaker: "You", Source: "monitor"}, false},
		{Segment{Speaker: "System Audio", Source: "monitor"}, false},
		{Segment{Speaker: "Person 2", LiveSpeaker: "System Audio", Source: "monitor"}, false},
	}
	for _, c := range cases {
		if got := Diarizable(c.seg); got != c.want {
			t.Errorf("Diarizable(%+v) = %v", c.seg, got)
		}
	}
	for label, want := range map[string]string{
		"Person 3 (Robin Chen)": "Robin Chen", "Person 3": "", "Robin (x)": "", "Person 12 (A (B))": "A (B)",
	} {
		if got := HintName(label); got != want {
			t.Errorf("HintName(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestPersonLabelsByFirstAppearance(t *testing.T) {
	got := personLabels([]DiarizeSegment{{Speaker: 4}, {Speaker: 1}, {Speaker: 4}, {Speaker: 0}})
	if len(got) != 3 || got[4] != "Person 1" || got[1] != "Person 2" || got[0] != "Person 3" {
		t.Errorf("labels = %v", got)
	}
}

func TestMergeSimilarSpeakersNeedsTwoClusters(t *testing.T) {
	segs := []DiarizeSegment{{Start: 0, End: 1, Speaker: 0}}
	labels := map[int]string{0: "Person 1"}
	gotSegs, gotLabels := MergeSimilarSpeakers(segs, labels, nil, "/no/model", 0.5, false)
	if len(gotSegs) != 1 || len(gotLabels) != 1 {
		t.Errorf("got %v %v", gotSegs, gotLabels)
	}
}

func TestReidentifyStopsEarly(t *testing.T) {
	audio := filepath.Join(t.TempDir(), "audio.m4a")
	if err := os.WriteFile(audio, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReidentifyByDiarization(&Session{}, DiarizeConfig{}); err == nil {
		t.Error("no audio path: no error")
	}
	if _, err := ReidentifyByDiarization(&Session{AudioPath: audio + ".gone"}, DiarizeConfig{}); err == nil {
		t.Error("missing audio: no error")
	}
	// Nothing diarizable, or no monitor source: done before decoding the
	// (fake) audio or loading a model.
	for _, sess := range []*Session{
		{AudioPath: audio, Sources: []string{"mic", "monitor"}, Segments: []Segment{
			{Speaker: "You", Source: "mic"}, {Speaker: "System Audio", Source: "monitor"}}},
		{AudioPath: audio, Sources: []string{"mic"}, Segments: []Segment{{Speaker: "Person 1", Source: "monitor"}}},
	} {
		if n, err := ReidentifyByDiarization(sess, DiarizeConfig{}); n != 0 || err != nil {
			t.Errorf("ReidentifyByDiarization = %d, %v", n, err)
		}
	}
}
