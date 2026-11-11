package main

import (
	"math"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/session"
)

var t0 = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

// rec is a session recorded from minute from to minute to after t0.
func rec(id string, from, to float64) *session.Session {
	start := t0.Add(time.Duration(from * float64(time.Minute)))
	return &session.Session{ID: id, CreatedAt: start, EndedAt: t0.Add(time.Duration(to * float64(time.Minute)))}
}

func ids(ss []*session.Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

func TestCompareShadowPairsByLargestOverlap(t *testing.T) {
	ours := []*session.Session{rec("a", 0, 30), rec("b", 31, 40), rec("c", 100, 110)}
	theirs := []*session.Session{rec("A", 0.1, 40), rec("B", 32, 39), rec("D", 200, 210)}
	rep := compareShadow(ours, theirs)
	var got []string
	for _, p := range rep.pairs {
		got = append(got, p.ours.ID+p.theirs.ID)
	}
	// a overlaps A most; b overlaps A too, but A is taken, so b gets B.
	if len(got) != 2 || got[0] != "aA" || got[1] != "bB" {
		t.Errorf("pairs = %v, want [aA bB]", got)
	}
	if o := ids(rep.onlyOurs); len(o) != 1 || o[0] != "c" {
		t.Errorf("only ours = %v, want [c]", o)
	}
	if o := ids(rep.onlyTheirs); len(o) != 1 || o[0] != "D" {
		t.Errorf("only theirs = %v, want [D]", o)
	}
}

func TestCompareShadowSessionWithoutEndUsesDuration(t *testing.T) {
	open := &session.Session{ID: "a", CreatedAt: t0, Duration: 600}
	rep := compareShadow([]*session.Session{open}, []*session.Session{rec("A", 5, 20)})
	if len(rep.pairs) != 1 || rep.pairs[0].overlap != 5*time.Minute {
		t.Errorf("pairs = %+v, want one with 5m overlap", rep.pairs)
	}
	if len(compareShadow([]*session.Session{rec("a", 0, 10)}, []*session.Session{rec("A", 10, 20)}).pairs) != 0 {
		t.Error("sessions that only touch were paired")
	}
}

func lines(speakerText ...string) []session.Segment {
	var segs []session.Segment
	for i := 0; i < len(speakerText); i += 2 {
		segs = append(segs, session.Segment{StartTime: float64(i), Speaker: speakerText[i], Text: speakerText[i+1]})
	}
	return segs
}

func TestScoreText(t *testing.T) {
	tomoe := &session.Session{Segments: lines(
		"Person 1", "the budget is due on friday",
		"You", "okay I will send it",
	)}
	ours := &session.Session{Segments: lines(
		"Person 1", "the budget was due on friday",
		"Person 2", "okay I will send it today",
	)}
	sc := scoreText(ours, tomoe)
	// One substitution and one insertion over eleven reference words.
	if want := 2.0 / 11; math.Abs(sc.wer-want) > 1e-9 {
		t.Errorf("WER = %v, want %v", sc.wer, want)
	}
	// Ten words align equal: the first line's five carry the same
	// speaker, the second line's five don't.
	if sc.matched != 10 || sc.same != 5 {
		t.Errorf("matched %d, same %d; want 10, 5", sc.matched, sc.same)
	}
	if sc := scoreText(tomoe, tomoe); sc.wer != 0 || sc.same != sc.matched {
		t.Errorf("a session against itself: %+v", sc)
	}
}
