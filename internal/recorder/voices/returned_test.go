package voices

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// meetingOf is profile name's meeting session (nil if none).
func meetingOf(s *Store, name, session string) *Heard {
	for _, p := range s.Profiles() {
		if p.Name != name {
			continue
		}
		for _, h := range p.Meetings {
			if h.Session == session {
				return &h
			}
		}
	}
	return nil
}

func profileID(t *testing.T, s *Store, name string) string {
	t.Helper()
	for _, p := range s.Profiles() {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("no profile %s", name)
	return ""
}

func repeat(n int, e []float32) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		out[i] = e
	}
	return out
}

// "Not them" on a sample returns it: it waits under "Who's this?" until
// named or skipped, and stays out of its profile through relearns either
// way. Naming it teaches an existing profile of that name its voice fits,
// or a new one; a relearn keeps what it taught, once.
func TestReturnedSampleNamedAndSkipped(t *testing.T) {
	s, _ := open(t)
	a, b, c := unit(0, 1, 0.1), unit(2, 3, 0.1), unit(1, 2, 0.1)
	at := time.Now()
	// Bo Example is known from another meeting, by voice b.
	if _, err := s.Learn("m2", at, []Learned{{Name: "Bo Example", Source: FromTile, Voice: voiceOf(20, b), Clip: Clip{Start: 0, End: 4}}}); err != nil {
		t.Fatal(err)
	}
	// m1: Ilse 12 s, then 3 s of b (Bo), then 4 s of c (nobody known).
	embs := append(append(repeat(12, a), repeat(3, b)...), repeat(4, c)...)
	l := learnedWithParts("Ilse Novak", 0, embs...)
	if _, err := s.LearnMeeting("m1", at, []Learned{l}); err != nil {
		t.Fatal(err)
	}
	ilse := profileID(t, s, "Ilse Novak")
	bSpan, cSpan := Clip{Start: 12, End: 15}, Clip{Start: 15, End: 19}
	if err := s.ExcludeSpan(ilse, "m1", bSpan, voiceOf(3, b), "Ilse Novak"); err != nil {
		t.Fatal(err)
	}
	if err := s.ExcludeSpan(ilse, "m1", cSpan, voiceOf(4, c), "Ilse Novak"); err != nil {
		t.Fatal(err)
	}
	rs := s.Returned()
	if len(rs) != 2 || !rs[0].Pending() || rs[0].From != ilse || rs[0].Label != "Ilse Novak" || rs[0].Voice == nil || rs[0].Voice.N != 3 {
		t.Fatalf("returned %+v", rs)
	}
	if h := meetingOf(s, "Ilse Novak", "m1"); h.Voice.N != 12 {
		t.Fatalf("Ilse keeps %d", h.Voice.N)
	}

	// Named Bo Example: Bo's profile (its voice fits) learns the sample.
	prof, err := s.AssignReturned("m1", bSpan, ilse, "bo  example", Voice{})
	if err != nil || prof.Name != "Bo Example" {
		t.Fatalf("assigned to %+v, %v", prof, err)
	}
	h := meetingOf(s, "Bo Example", "m1")
	if h == nil || h.Voice.N != 3 || h.Source != FromNamed || len(h.Assigned) != 1 {
		t.Fatalf("Bo's m1: %+v", h)
	}
	if _, err := s.AssignReturned("m1", bSpan, ilse, "Bo Example", Voice{}); err == nil {
		t.Error("a sample was named twice")
	}
	// Can't tell on the other.
	if err := s.SkipReturned("m1", cSpan, ilse); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.Returned() {
		if r.Pending() {
			t.Errorf("still pending: %+v", r)
		}
	}
	// Put back is refused once the sample teaches someone else.
	if err := s.IncludeSpan(ilse, "m1", bSpan, voiceOf(3, b)); err == nil {
		t.Error("a sample named as Bo was put back into Ilse")
	}

	// Relearning m1, with Bo now named on its own lines too (2 s of b at
	// 30 s): Ilse stays without either sample; Bo has both, the sample once.
	bo := learnedWithParts("Bo Example", 30, repeat(12, b)...)
	bo.Source = FromNamed
	if _, err := s.LearnMeeting("m1", at, []Learned{l, bo}); err != nil {
		t.Fatal(err)
	}
	if h := meetingOf(s, "Ilse Novak", "m1"); h.Voice.N != 12 {
		t.Errorf("relearnt Ilse %d, want 12", h.Voice.N)
	}
	if h := meetingOf(s, "Bo Example", "m1"); h == nil || h.Voice.N != 15 || len(h.Assigned) != 1 {
		t.Errorf("relearnt Bo's m1: %+v", h)
	}

	// A name no profile fits starts one.
	ex := learnedWithParts("Ilse Novak", 0, append(repeat(12, a), repeat(3, unit(3, 0, 0.1))...)...)
	if _, err := s.LearnMeeting("m3", at, []Learned{ex}); err != nil {
		t.Fatal(err)
	}
	span := Clip{Start: 12, End: 15}
	if err := s.ExcludeSpan(ilse, "m3", span, voiceOf(3, unit(3, 0, 0.1)), ""); err != nil {
		t.Fatal(err)
	}
	prof, err = s.AssignReturned("m3", span, ilse, "Bo Example", Voice{})
	if err != nil || prof.ID == profileID(t, s, "Bo Example") && len(s.Profiles()) == 2 {
		t.Fatalf("assigned %+v %v", prof, err)
	}
	n := 0
	for _, p := range s.Profiles() {
		if p.Name == "Bo Example" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d profiles named Bo Example, want a new one for the unlike voice", n)
	}
}

// Junk is left out of every profile's learning, a relearn included, and
// Put back undoes it.
func TestJunkSpanLeftOutOfAllLearning(t *testing.T) {
	s, _ := open(t)
	a, x := unit(0, 1, 0.1), unit(2, 3, 0.1)
	at := time.Now()
	l := learnedWithParts("Ilse Novak", 0, append(repeat(12, a), repeat(3, x)...)...)
	if _, err := s.LearnMeeting("m1", at, []Learned{l}); err != nil {
		t.Fatal(err)
	}
	ilse := profileID(t, s, "Ilse Novak")
	junk := Clip{Start: 12, End: 15}
	if err := s.JunkSpan(ilse, "m1", junk, voiceOf(3, x)); err != nil {
		t.Fatal(err)
	}
	if h := meetingOf(s, "Ilse Novak", "m1"); h.Voice.N != 12 || len(h.ExcludedSpans) != 0 {
		t.Fatalf("after junk: %+v", h)
	}
	if len(s.Returned()) != 0 {
		t.Error("junk went to Who's this")
	}
	// Relearnt, and another name learnt over the same stretch (12 s from
	// 10 s, 3 of them junk): neither learns the junk.
	other := learnedWithParts("Kai Example", 10, repeat(12, x)...)
	if _, err := s.LearnMeeting("m1", at, []Learned{l, other}); err != nil {
		t.Fatal(err)
	}
	if h := meetingOf(s, "Ilse Novak", "m1"); h.Voice.N != 12 {
		t.Errorf("relearnt Ilse %d, want 12", h.Voice.N)
	}
	if h := meetingOf(s, "Kai Example", "m1"); h == nil || h.Voice.N != 9 {
		t.Errorf("Kai learnt junk: %+v", h)
	}
	// Persisted.
	s2, err := Open(s.path, sameFold, s.Params())
	if err != nil {
		t.Fatal(err)
	}
	if _, junk := s2.HeldSpans(ilse, "m1"); len(junk) != 1 {
		t.Errorf("junk after reopening: %v", junk)
	}
	if err := s.UnjunkSpan(ilse, "m1", junk, voiceOf(3, x)); err != nil {
		t.Fatal(err)
	}
	if h := meetingOf(s, "Ilse Novak", "m1"); h.Voice.N != 15 {
		t.Errorf("put back: %d", h.Voice.N)
	}
	if err := s.UnjunkSpan(ilse, "m1", junk, voiceOf(3, x)); err == nil {
		t.Error("put back twice")
	}
}

// Samples taken out before returned samples existed are returned when
// the file is read, once.
func TestExcludedSpansMigrateToReturned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.json")
	old := `{"version":1,"rules":2,"profiles":[{"id":"aa11","name":"Ilse Novak","created":"2026-11-01T10:00:00Z","meetings":[
	 {"session":"m1","at":"2026-11-01T10:00:00Z","source":"tile","voice":{"sum":[12,0,0,0],"n":12},"clip":{"start":0,"end":4},
	  "excludedSpans":[{"start":12,"end":15},{"start":20,"end":22}]}]}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	rs := s.Returned()
	if len(rs) != 2 || rs[0].From != "aa11" || rs[0].FromName != "Ilse Novak" || !rs[0].Pending() || rs[0].Span != (Clip{Start: 12, End: 15}) {
		t.Fatalf("migrated %+v", rs)
	}
	// Saved, read again: still two.
	if err := s.SkipReturned("m1", Clip{Start: 20, End: 22}, "aa11"); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path, sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if rs := s2.Returned(); len(rs) != 2 || rs[1].Pending() {
		t.Errorf("reopened %+v", rs)
	}
}

// The owner's naming beats a tile: relearning a meeting the owner named
// for a profile from its tile leaves the named voice.
func TestTileDoesNotReplaceOwnerNamed(t *testing.T) {
	s, _ := open(t)
	at := time.Now()
	named := voiceOf(30, unit(0, 1, 0.1))
	if _, err := s.Learn("m1", at, []Learned{{Name: "Ilse Novak", Source: FromNamed, Voice: named}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Learn("m2", at, []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.15))}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LearnMeeting("m1", at, []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(12, unit(0, 2, 0.4))}}); err != nil {
		t.Fatal(err)
	}
	h := meetingOf(s, "Ilse Novak", "m1")
	if h == nil || h.Source != FromNamed || h.Voice.N != 30 {
		t.Fatalf("m1 after a tile relearn: %+v", h)
	}
	// The owner's naming learnt again does replace it.
	if _, err := s.LearnMeeting("m1", at, []Learned{{Name: "Ilse Novak", Source: FromNamed, Voice: voiceOf(25, unit(0, 1, 0.1))}}); err != nil {
		t.Fatal(err)
	}
	if h := meetingOf(s, "Ilse Novak", "m1"); h.Voice.N != 25 {
		t.Errorf("named relearn: %d", h.Voice.N)
	}
}

// A meeting's Confirmed mark is kept when it is learnt again, and the
// samples taken out of it stay out when its contribution is dropped and
// it is learnt again later.
func TestRelearnKeepsConfirmedAndTakenOut(t *testing.T) {
	s, _ := open(t)
	at := time.Now()
	a, x := unit(0, 1, 0.1), unit(2, 3, 0.1)
	l := learnedWithParts("Ilse Novak", 0, append(repeat(12, a), repeat(3, x)...)...)
	if _, err := s.LearnMeeting("m1", at, []Learned{l}); err != nil {
		t.Fatal(err)
	}
	ilse := profileID(t, s, "Ilse Novak")
	if err := s.ConfirmMeeting(ilse, "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LearnMeeting("m1", at, []Learned{l}); err != nil {
		t.Fatal(err)
	}
	if h := meetingOf(s, "Ilse Novak", "m1"); !h.Confirmed {
		t.Fatal("relearn dropped Confirmed")
	}

	// m2: a tile meeting with a sample taken out, then dropped by a
	// relearn that no longer teaches it, then learnt again.
	l2 := learnedWithParts("Ilse Novak", 100, append(repeat(12, a), repeat(3, x)...)...)
	if _, err := s.LearnMeeting("m2", at, []Learned{l2}); err != nil {
		t.Fatal(err)
	}
	span := Clip{Start: 112, End: 115}
	if err := s.ExcludeSpan(ilse, "m2", span, voiceOf(3, x), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LearnMeeting("m2", at, nil); err != nil {
		t.Fatal(err)
	}
	if meetingOf(s, "Ilse Novak", "m2") != nil {
		t.Fatal("the relearn kept a meeting it no longer teaches")
	}
	if _, err := s.LearnMeeting("m2", at, []Learned{l2}); err != nil {
		t.Fatal(err)
	}
	h := meetingOf(s, "Ilse Novak", "m2")
	if h == nil || h.Voice.N != 12 || len(h.ExcludedSpans) != 1 {
		t.Fatalf("learnt again after the drop: %+v", h)
	}
}
