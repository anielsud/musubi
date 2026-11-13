package diarize

import (
	"testing"

	"github.com/anielsud/musubi/internal/recorder/session"
)

func TestNameShortLines(t *testing.T) {
	hints := []Hint{
		{T: 10.6, Name: "Ana Lomax"}, {T: 11.2, Name: "Ana Lomax"}, // during the 10.0-11.0 line
		{T: 20.5, Name: "Ana Lomax"}, {T: 21.0, Name: "Bix Ilo"}, // two people during 20.0-21.0
		{T: 30.7, Candidates: []string{"Ana Lomax", "Bix Ilo"}}, // several lit
	}
	labelOf := func(name string) (string, bool) {
		return map[string]string{"Ana Lomax": "Person 1 (Ana Lomax)", "Bix Ilo": "Person 2 (Bix Ilo)"}[name], true
	}
	segs := []session.Segment{
		{Source: "monitor", Speaker: "Person 2 (Bix Ilo)", StartTime: 10, EndTime: 11}, // renamed to Ana
		{Source: "monitor", Speaker: "Person 2 (Bix Ilo)", StartTime: 20, EndTime: 21}, // highlight unclear: kept
		{Source: "monitor", Speaker: "Person 2 (Bix Ilo)", StartTime: 30, EndTime: 31}, // several lit: kept
		{Source: "monitor", Speaker: "Person 2 (Bix Ilo)", StartTime: 9, EndTime: 13},  // too long: kept
		{Source: "mic", Speaker: "You", StartTime: 10, EndTime: 11},                    // the host's own line: kept
		{Source: "monitor", Speaker: "Person 2 (Bix Ilo)", StartTime: 50, EndTime: 51}, // no hints: kept
	}
	if n := NameShortLines(segs, hints, labelOf); n != 1 {
		t.Errorf("changed %d lines, want 1", n)
	}
	want := []string{"Person 1 (Ana Lomax)", "Person 2 (Bix Ilo)", "Person 2 (Bix Ilo)", "Person 2 (Bix Ilo)", "You", "Person 2 (Bix Ilo)"}
	for i, s := range segs {
		if s.Speaker != want[i] {
			t.Errorf("line %d: %q, want %q", i, s.Speaker, want[i])
		}
	}
}
