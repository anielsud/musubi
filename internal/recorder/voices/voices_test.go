package voices

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unit is a 4-dimensional fingerprint pointing mostly along axis i, a
// little along j.
func unit(i, j int, tilt float64) []float32 {
	v := make([]float32, 4)
	v[i] = float32(math.Sqrt(1 - tilt*tilt))
	v[j] += float32(tilt)
	return v
}

func voiceOf(n int, emb []float32) Voice {
	var v Voice
	for range n {
		v.Add(emb)
	}
	return v
}

func sameFold(a, b string) bool { return strings.EqualFold(a, b) }

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "voices", "m.json")
	// One meeting is enough here; TestMinMeetings covers the rule.
	params := DefaultParams()
	params.MinMeetings = 1
	s, err := Open(path, sameFold, params)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestLearnAndMatch(t *testing.T) {
	s, path := open(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.Learn("m1", at, []Learned{
		{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.1))},
		{Name: "Wayde Tyrrel", Source: FromTile, Voice: voiceOf(20, unit(1, 0, 0.1))},
		{Name: "Too Short", Source: FromTile, Voice: voiceOf(3, unit(2, 0, 0))},
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Profiles()); n != 2 {
		t.Fatalf("%d profiles, want 2 (a voice under MinFingerprints isn't learnt)", n)
	}
	got := s.Match(map[int]Voice{
		1: voiceOf(10, unit(0, 1, 0.2)),  // Ilse
		2: voiceOf(10, unit(2, 3, 0.1)),  // nobody
		3: voiceOf(10, unit(1, 0, 0.25)), // Wayde
		4: voiceOf(3, unit(0, 1, 0.1)),   // Ilse, but too few fingerprints
	}, nil)
	if got[1].Name != "Ilse Novak" || got[3].Name != "Wayde Tyrrel" || got[2].Name != "" || got[4].Name != "" {
		t.Errorf("Match = %v", got)
	}

	// Reopened from disk, it's the same.
	s2, err := Open(path, sameFold, s.params)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0.2))}, nil); got[1].Name != "Ilse Novak" {
		t.Errorf("after reopening: %v", got)
	}
}

func TestMatchOneVoicePerProfileAndTaken(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	got := s.Match(map[int]Voice{
		1: voiceOf(10, unit(0, 1, 0.3)),
		2: voiceOf(10, unit(0, 1, 0.1)), // likelier
	}, nil)
	if got[2].Name != "Ilse Novak" || got[1].Name != "" || got[2].Similarity < 0.99 {
		t.Errorf("one voice per profile, the likest: %v", got)
	}
	got = s.Match(map[int]Voice{2: voiceOf(10, unit(0, 1, 0.1))}, func(n string) bool { return n == "Ilse Novak" })
	if got[2].Name != "" {
		t.Errorf("a name already someone else's in the meeting was given: %v", got)
	}
}

func TestMatchNeedsMargin(t *testing.T) {
	s, _ := open(t)
	// Two profiles about equally like the voice: neither is named.
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.3))},
		{Name: "Wayde Tyrrel", Voice: voiceOf(20, unit(1, 0, 0.3))},
	})
	v := voiceOf(10, []float32{0.7071, 0.7071, 0, 0})
	if got := s.Match(map[int]Voice{1: v}, nil); got[1].Name != "" {
		t.Errorf("named without a margin: %v", got)
	}
}

func TestLearnReplacesTheMeetingAndKeepsUnlikeVoicesApart(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(40, unit(0, 1, 0))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "ilse novak", Voice: voiceOf(10, unit(0, 1, 0.1))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(12, unit(0, 1, 0.1))}})
	ps := s.Profiles()
	if len(ps) != 1 || len(ps[0].Meetings) != 2 || ps[0].Voice().N != 52 {
		t.Fatalf("relearning a meeting should replace it: %+v", ps)
	}
	// A misread name, or another Ilse: the voice is nothing like the
	// profile's, so it starts its own profile instead of being dropped.
	names, _ := s.Learn("m3", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(14, unit(3, 2, 0))}})
	ps = s.Profiles()
	if len(names) != 1 || len(ps) != 2 {
		t.Fatalf("an unlike voice should start a second profile: %v, %+v", names, ps)
	}
	for _, p := range ps {
		if p.Name != "Ilse Novak" || len(p.Meetings) != map[bool]int{true: 1, false: 2}[p.Meetings[0].Session == "m3"] {
			t.Errorf("profile %+v", p)
		}
	}
}

func TestLearnPicksTheLikestSameNamedProfile(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(2, 3, 0))}}) // another Ilse
	if n := len(s.Profiles()); n != 2 {
		t.Fatalf("%d profiles, want 2: a young profile also keeps an unlike voice out", n)
	}
	// Each later meeting joins the Ilse it sounds like.
	_, _ = s.Learn("m3", time.Now(), []Learned{{Name: "ilse novak", Voice: voiceOf(14, unit(2, 3, 0.1))}})
	_, _ = s.Learn("m4", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(14, unit(0, 1, 0.1))}})
	ps := s.Profiles()
	if len(ps) != 2 {
		t.Fatalf("%d profiles, want 2", len(ps))
	}
	for _, p := range ps {
		var sessions []string
		for _, h := range p.Meetings {
			sessions = append(sessions, h.Session)
		}
		got := strings.Join(sessions, ",")
		if got != "m1,m4" && got != "m2,m3" {
			t.Errorf("profile %s learnt %s", p.ID, got)
		}
	}
	// Relearning a meeting replaces it in the profile it joined.
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(12, unit(2, 3, 0))}})
	total := 0
	for _, p := range s.Profiles() {
		total += len(p.Meetings)
	}
	if total != 4 {
		t.Errorf("relearning m2 left %d meetings, want 4", total)
	}
	// A name given twice keeps the last voice (the user's naming wins).
	_, _ = s.Learn("m5", time.Now(), []Learned{
		{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(14, unit(3, 1, 0))},
		{Name: "Ilse Novak", Source: FromNamed, Voice: voiceOf(14, unit(0, 1, 0.05))},
	})
	if n := len(s.Profiles()); n != 2 {
		t.Errorf("%d profiles after a name given twice, want 2", n)
	}
}

func TestSameNamed(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))},
		{Name: "Wayde Tyrrel", Voice: voiceOf(20, unit(1, 0, 0))},
	})
	if got := s.SameNamed(); len(got) != 0 {
		t.Fatalf("SameNamed with one profile per name = %v", got)
	}
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(2, 3, 0))}})
	_, _ = s.Learn("m3", time.Now(), []Learned{{Name: "ilse novak", Voice: voiceOf(20, unit(3, 2, 0))}})
	got := s.SameNamed()
	if len(got) != 3 {
		t.Fatalf("SameNamed = %v, want the three pairs of Ilses", got)
	}
	// Different people: the pair isn't offered again.
	if err := s.Apart(got[0].A, got[0].B); err != nil {
		t.Fatal(err)
	}
	if after := s.SameNamed(); len(after) != 2 {
		t.Errorf("after keeping a pair apart: %v", after)
	}
	// Same person: one profile, no alias of its own name, and still apart
	// from whoever the merged-away profile was kept apart from.
	into, from := got[1].A, got[1].B
	if err := s.Merge(into, from); err != nil {
		t.Fatal(err)
	}
	if after := s.SameNamed(); len(after) != 0 {
		t.Errorf("after merging, the pair kept apart is offered again: %v", after)
	}
	for _, p := range s.Profiles() {
		if p.ID == into && contains(p.Aliases, p.Name) {
			t.Errorf("merging same-named profiles added the name as an alias: %v", p.Aliases)
		}
	}
}

func TestKnowsAnySameNamedProfile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "v.json"), sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Source: FromNamed, Voice: voiceOf(20, unit(2, 3, 0))}})
	// The first Ilse (one meeting, from a tile) isn't in use; the second
	// (named by the user) is.
	if !s.Knows("ilse novak") {
		t.Error("Knows missed the second, in-use profile of the name")
	}
	if s.Knows("Wayde Tyrrel") {
		t.Error("Knows a name with no profile")
	}
}

func TestMergeApartForget(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.05))},
		{Name: "I. Novak (guest)", Voice: voiceOf(20, unit(0, 1, 0.1))},
		{Name: "Wayde Tyrrel", Voice: voiceOf(20, unit(1, 0, 0))},
	})
	ids := map[string]string{}
	for _, p := range s.Profiles() {
		ids[p.Name] = p.ID
	}
	pairs := s.LookAlike(0.7)
	if len(pairs) != 1 {
		t.Fatalf("LookAlike = %v, want the two Ilse profiles", pairs)
	}
	if err := s.Apart(pairs[0].A, pairs[0].B); err != nil {
		t.Fatal(err)
	}
	if len(s.LookAlike(0.7)) != 0 {
		t.Error("a pair said to be different people was offered again")
	}
	if err := s.Merge(ids["Ilse Novak"], ids["I. Novak (guest)"]); err != nil {
		t.Fatal(err)
	}
	if ps := s.Profiles(); len(ps) != 2 {
		t.Fatalf("after merging: %d profiles", len(ps))
	}
	if err := s.Rename(ids["Ilse Novak"], "Ilse M. Novak"); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ids["Wayde Tyrrel"]); err != nil {
		t.Fatal(err)
	}
	if ps := s.Profiles(); len(ps) != 1 || ps[0].Name != "Ilse M. Novak" || len(ps[0].Apart) != 0 {
		t.Errorf("after forgetting Wayde: %+v", ps)
	}
	// Learning under a merged-away or old name teaches the same profile.
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "I. Novak (guest)", Voice: voiceOf(20, unit(0, 1, 0.05))}})
	_, _ = s.Learn("m3", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.05))}})
	// m1 (both merged profiles learnt from it, once), m2, m3.
	if ps := s.Profiles(); len(ps) != 1 || len(ps[0].Meetings) != 3 {
		t.Errorf("old names should teach the profile they're part of: %+v", ps)
	}
	if err := s.Forget(ids["Ilse Novak"]); err != nil || len(s.Profiles()) != 0 {
		t.Errorf("Forget: %v, %d left", err, len(s.Profiles()))
	}
	// A forgotten person isn't learnt again, under any of their names.
	_, _ = s.Learn("m4", time.Now(), []Learned{
		{Name: "Ilse M. Novak", Voice: voiceOf(20, unit(0, 1, 0))},
		{Name: "I. Novak (guest)", Voice: voiceOf(20, unit(0, 1, 0))},
	})
	if n := len(s.Profiles()); n != 0 {
		t.Errorf("a forgotten profile came back: %d", n)
	}
}

func TestStoreSeesAnotherProcesssChanges(t *testing.T) {
	s, path := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	other, err := Open(path, sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = other.Learn("m2", time.Now(), []Learned{{Name: "Wayde Tyrrel", Voice: voiceOf(20, unit(1, 0, 0))}})
	// Make sure the modification time differs from what s last saw.
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	if n := len(s.Profiles()); n != 2 {
		t.Errorf("the other process's profile wasn't seen: %d profiles", n)
	}
}

func TestRejectStartOverAndMeetings(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.1))}})
	id := s.Profiles()[0].ID
	stranger := voiceOf(10, unit(0, 2, 0.4)) // 0.92 like Ilse: named her
	if got := s.Match(map[int]Voice{1: stranger}, nil); got[1].Name != "Ilse Novak" {
		t.Fatalf("setup: %v", got)
	}
	if err := s.Reject(id, "m9", time.Now(), stranger); err != nil {
		t.Fatal(err)
	}
	if got := s.Match(map[int]Voice{1: stranger}, nil); got[1].Name != "" {
		t.Errorf("a rejected voice was named again: %v", got)
	}
	if got := s.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0.05))}, nil); got[1].Name != "Ilse Novak" {
		t.Errorf("Ilse herself stopped being named: %v", got)
	}

	// Taking a meeting out: it's never learnt from again.
	if err := s.ForgetMeeting(id, "m2"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0.1))}})
	if p := s.Profiles()[0]; len(p.Meetings) != 1 || p.Meetings[0].Session != "m1" {
		t.Errorf("a removed meeting came back: %+v", p.Meetings)
	}

	// Starting over: no meetings, the old ones stay out, new ones teach.
	if err := s.StartOver(id); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	if p := s.Profiles()[0]; len(p.Meetings) != 0 {
		t.Errorf("an old meeting was learnt after starting over: %+v", p.Meetings)
	}
	if got := s.Match(map[int]Voice{1: voiceOf(10, unit(0, 1, 0))}, nil); got[1].Name != "" {
		t.Errorf("a started-over profile with nothing learnt named a voice: %v", got)
	}
	_, _ = s.Learn("m3", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	if p := s.Profiles()[0]; len(p.Meetings) != 1 || p.Meetings[0].Session != "m3" {
		t.Errorf("a new meeting wasn't learnt after starting over: %+v", p.Meetings)
	}
}

func TestUnforget(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	_ = s.Forget(s.Profiles()[0].ID)
	if err := s.Unforget("ilse novak"); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}}); len(names) != 1 {
		t.Errorf("a name the user gave again wasn't learnt: %v", names)
	}
}

func TestMinMeetings(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "v.json"), sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	ilse := voiceOf(10, unit(0, 1, 0))
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0))},
		{Name: "Wayde Tyrrel", Source: FromNamed, Voice: voiceOf(20, unit(1, 0, 0))},
		{Name: "Bodil Ekdahl", Source: FromTile, Voice: voiceOf(20, unit(2, 0, 0))},
	})
	got := s.Match(map[int]Voice{1: ilse, 2: voiceOf(10, unit(1, 0, 0)), 3: voiceOf(10, unit(2, 0, 0))}, nil)
	if got[1].Name != "" || got[2].Name != "Wayde Tyrrel" || got[3].Name != "" {
		t.Errorf("after one meeting only a person the user named should name voices: %v", got)
	}
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0.05))}})
	var bodil string
	for _, p := range s.Profiles() {
		if p.Name == "Bodil Ekdahl" {
			bodil = p.ID
		}
	}
	_ = s.Confirm(bodil)
	got = s.Match(map[int]Voice{1: ilse, 3: voiceOf(10, unit(2, 0, 0))}, nil)
	if got[1].Name != "Ilse Novak" || got[3].Name != "Bodil Ekdahl" {
		t.Errorf("heard twice, or confirmed, should name voices: %v", got)
	}
}

func TestMove(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(20, unit(0, 1, 0))},
		{Name: "Wayde Tyrrel", Source: FromTile, Voice: voiceOf(20, unit(1, 0, 0))},
	})
	// Wayde, under a misread name: unlike Ilse, so a second Ilse profile.
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Source: FromTile, Voice: voiceOf(30, unit(1, 0, 0.05))}})
	var misread, wayde Profile
	for _, p := range s.Profiles() {
		switch {
		case p.Name == "Wayde Tyrrel":
			wayde = p
		case len(p.Meetings) == 1 && p.Meetings[0].Session == "m2":
			misread = p
		}
	}
	if misread.ID == "" || wayde.ID == "" {
		t.Fatalf("setup: %+v", s.Profiles())
	}
	if err := s.Move(misread.ID, "m2", time.Now(), misread.Meetings[0].Voice, misread.ID, ""); err == nil {
		t.Error("moving a meeting to its own profile")
	}
	if err := s.Move(misread.ID, "m2", time.Now(), misread.Meetings[0].Voice, wayde.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Profiles() {
		switch p.ID {
		case misread.ID:
			if len(p.Meetings) != 0 || !contains(p.Excluded, "m2") {
				t.Errorf("the misread profile after the move: %+v", p)
			}
		case wayde.ID:
			if len(p.Meetings) != 2 || p.Voice().N != 50 {
				t.Errorf("Wayde after the move: %d meetings, %d fingerprints", len(p.Meetings), p.Voice().N)
			}
		}
	}
	// No Ilse learns m2 again, and no new one is made for it.
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(30, unit(1, 0, 0.05))}})
	n := len(s.Profiles())
	for _, p := range s.Profiles() {
		for _, h := range p.Meetings {
			if p.Name == "Ilse Novak" && h.Session == "m2" {
				t.Errorf("a moved meeting came back: %+v", p.Meetings)
			}
		}
	}
	if n != 3 {
		t.Errorf("%d profiles, want 3", n)
	}
	// To a new profile, by name: a profile of its own even when the name
	// is taken.
	ilse := s.Profiles()
	var first Profile
	for _, p := range ilse {
		if p.Name == "Ilse Novak" && len(p.Meetings) == 1 {
			first = p
		}
	}
	if err := s.Move(first.ID, "m1", time.Now(), first.Meetings[0].Voice, "", "Wayde Tyrrel"); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Profiles()); got != 4 {
		t.Errorf("%d profiles after moving to a new one, want 4", got)
	}
	if err := s.Move(first.ID, "m1", time.Now(), first.Meetings[0].Voice, "", ""); err == nil {
		t.Error("a move with no profile to move to")
	}
}

// A bare first name matches every full name starting with it for
// learning, but three people called Kevin are never offered as one.
func TestSameNamedNeedsTheSameName(t *testing.T) {
	prefix := func(a, b string) bool {
		a, b = strings.ToLower(a), strings.ToLower(b)
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	}
	s, err := Open(filepath.Join(t.TempDir(), "v.json"), prefix, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Kevin Li", Voice: voiceOf(20, unit(1, 0, 0))},
		{Name: "Kevin Goldsmith", Voice: voiceOf(20, unit(0, 1, 0))},
	})
	_, _ = s.Learn("m2", time.Now(), []Learned{{Name: "Kevin", Voice: voiceOf(20, unit(0, 0, 1))}})
	if got := s.SameNamed(); len(got) != 0 {
		t.Fatalf("Kevin, Kevin Li and Kevin Goldsmith offered as one: %v", got)
	}
	_, _ = s.Learn("m3", time.Now(), []Learned{{Name: "kevin  li", Voice: voiceOf(20, unit(0, 1, 1))}})
	if got := s.SameNamed(); len(got) != 1 || got[0].Name != "Kevin Li" {
		t.Errorf("two Kevin Lis: %v", got)
	}
}

// A confirmed meeting stays in the profile and is marked; a meeting the
// profile didn't learn from can't be confirmed.
func TestConfirmMeeting(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))}})
	p := s.Profiles()[0]
	if err := s.ConfirmMeeting(p.ID, "m1"); err != nil {
		t.Fatal(err)
	}
	if got := s.Profiles()[0]; len(got.Meetings) != 1 || !got.Meetings[0].Confirmed {
		t.Errorf("after confirming: %+v", got.Meetings)
	}
	if err := s.ConfirmMeeting(p.ID, "m9"); err == nil {
		t.Error("confirmed a meeting the profile never heard")
	}
}

// A few seconds of voice names a speaker but doesn't teach: a voice under
// MinLearn isn't learnt, and one already in a profile (learnt before the
// cutoff) is left out of its voice and its meeting count.
func TestThinVoicesDontTeach(t *testing.T) {
	s, _ := open(t)
	params := DefaultParams() // MinMeetings 2: does a thin meeting count?
	s.SetParams(params)
	if names, _ := s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(9, unit(0, 1, 0))}}); len(names) != 0 || len(s.Profiles()) != 0 {
		t.Fatalf("learnt from 9 fingerprints: %v", names)
	}
	_, _ = s.Learn("m1", time.Now(), []Learned{{Name: "Ilse Novak", Voice: voiceOf(40, unit(0, 1, 0))}})
	// An old thin meeting, as profiles learnt with the earlier floor hold.
	s.mu.Lock()
	p := s.profiles[0]
	p.Meetings = append(p.Meetings, Heard{Session: "m2", At: time.Now(), Voice: voiceOf(8, unit(1, 0, 0))})
	p.refresh(s.params.MinLearn)
	s.mu.Unlock()
	got := s.Profiles()[0]
	if got.LearnedVoice(s.MinLearn()).N != 40 {
		t.Errorf("thin meeting in the voice: %d fingerprints", got.LearnedVoice(s.MinLearn()).N)
	}
	if s.InUse(&got) {
		t.Error("one learnt meeting and a thin one put the profile in use (MinMeetings 2)")
	}
}

// Merging two profiles that both learnt from one meeting keeps one entry
// for it, with both voices.
func TestMergeFoldsTheSameMeeting(t *testing.T) {
	s, _ := open(t)
	_, _ = s.Learn("m1", time.Now(), []Learned{
		{Name: "Ilse Novak", Voice: voiceOf(20, unit(0, 1, 0))},
		{Name: "Wayde Tyrrel", Voice: voiceOf(15, unit(0, 1, 0.1))},
	})
	ps := s.Profiles()
	if err := s.Merge(ps[0].ID, ps[1].ID); err != nil {
		t.Fatal(err)
	}
	got := s.Profiles()
	if len(got) != 1 || len(got[0].Meetings) != 1 || got[0].Meetings[0].Voice.N != 35 {
		t.Fatalf("after merging: %+v", got)
	}
}
