package knowledge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

func TestPrepNames(t *testing.T) {
	if k := PrepNameKey("Ruiz, Dana"); k != "dana ruiz" {
		t.Errorf("key %q", k)
	}
	g := newPeople([]string{"Dana Ruiz", "Lee Chen", "Lee Park"})
	// A first name matches only when no one else in the group has it.
	for name, want := range map[string]string{"dana": "Dana Ruiz", "Ruiz, Dana": "Dana Ruiz", "lee": "", "Lee Chen": "Lee Chen"} {
		if got, _ := g.match(name); got != want {
			t.Errorf("match(%q) = %q, want %q", name, got, want)
		}
	}
}

// Prep for a meeting with Dana: Juniper is hers; what was said about it in
// a meeting she wasn't in is worth sharing, but never the restricted HR
// line.
func TestPrep(t *testing.T) {
	f := newCardFixture(t)
	f.st.ClearThreads()
	th := &Threader{Store: f.st, LLM: &scriptedThreads{}, Chunk: 70, Parallel: 2}
	items := []ThreadItem{
		{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)},
		{ID: "m:3", Start: time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)},
		{ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)},
	}
	for _, it := range items {
		if _, err := th.Thread(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := f.st.AllItems()
	for i := range all {
		switch all[i].ID {
		case "m:1":
			all[i].Participants = []item.Participant{{Name: "Ruiz, Dana", Role: "attendee"}, {Name: "Sam Owner", Owner: true}}
		default:
			all[i].Participants = []item.Participant{{Name: "Lee Chen", Role: "attendee"}}
		}
	}
	v, err := Prep(f.st, sensitivity.Owner(), PrepRequest{Template: "m:1"}, all, "Sam Owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.People) != 1 || v.People[0] != "Ruiz, Dana" || v.Met != 1 {
		t.Fatalf("people %v, met %d", v.People, v.Met)
	}
	if len(v.Subjects) == 0 || v.Subjects[0].Name != "Juniper programme" {
		t.Fatalf("subjects %+v", v.Subjects)
	}
	shared := false
	for _, s := range v.Share {
		if s.Item.Label.Categories != 0 || s.Item.Label.Level >= sensitivity.Restricted {
			t.Errorf("suggested sharing %q", s.Item.Text)
		}
		if s.Item.ItemID == "m:1" {
			t.Errorf("suggested sharing what she heard: %q", s.Item.Text)
		}
		shared = shared || s.Item.ItemID == "m:2"
	}
	if !shared {
		t.Errorf("the later Juniper count, said without her, wasn't suggested: %+v", v.Share)
	}
	// Juniper is ordinary work: not marked for the owner alone.
	v, err = Prep(f.st, sensitivity.Owner(), PrepRequest{Template: "m:1", MarkPrivate: true, QuietAfter: 21 * 24 * time.Hour}, all, "Sam Owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Subjects) == 0 || v.Subjects[0].Name != "Juniper programme" || v.Subjects[0].Private {
		t.Fatalf("subjects with MarkPrivate %+v", v.Subjects)
	}
}

func TestPersonIn(t *testing.T) {
	mk := func(id string, names ...string) item.Item {
		it := item.Item{ID: id, Kind: item.KindMeeting}
		for _, n := range names {
			it.Participants = append(it.Participants, item.Participant{Name: n})
		}
		return it
	}
	items := []item.Item{
		mk("1", "Ruiz, Dana", "Lee Chen", "Sam Owner"), mk("2", "Dana Ruiz", "Lee Chen"),
		mk("3", "Lee Park"), mk("4", "Lee Park"), mk("5", "Ana Once"),
	}
	for q, want := range map[string]string{
		"what's open with dana?":                "Dana Ruiz",
		"prep me for my 1:1 with Lee Chen":      "Lee Chen",
		"what's open with lee?":                 "", // two Lees
		"what should I raise with ana":          "", // met once
		"what's open between me and sam owner?": "",
	} {
		if got := PersonIn(q, items, "Sam Owner"); got != want {
			t.Errorf("PersonIn(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestSpellings(t *testing.T) {
	sp := Spellings{"ruis": "Ruiz", "daka": "Dhaka"}
	if got := sp.Canonical("Dana Ruis visited the Daka office."); got != "Dana Ruiz visited the Dhaka office." {
		t.Errorf("canonical %q", got)
	}
	if got := sp.Expand("open with ruis?"); got != "open with ruis? open with Ruiz?" {
		t.Errorf("expand %q", got)
	}
	if got := sp.Canonical("Ruisdael"); got != "Ruisdael" {
		t.Errorf("part of a word changed: %q", got)
	}
}

// Close spellings of one name are proposed; two people with near names
// (both on invites under different full names) are not; the owner's name
// is never folded into another.
func TestAliasCandidates(t *testing.T) {
	mk := func(id string, names ...string) item.Item {
		it := item.Item{ID: id, Kind: item.KindMeeting}
		for _, n := range names {
			it.Participants = append(it.Participants, item.Participant{Name: n})
		}
		return it
	}
	items := []item.Item{mk("1", "Dana Ruiz", "Katrin Holm"), mk("2", "Catrin Berg"), mk("3", "Sam Owner")}
	st := func(item, text string) store.Statement { return store.Statement{ItemID: item, Text: text} }
	sts := []store.Statement{
		st("1", "Ruiz said the Dhaka office is ready."), st("1", "Ruiz again: Dhaka hiring."), st("2", "Ruis will check the Daka numbers."),
		st("2", "Ruis and Daka again."), st("1", "Katrin presented."), st("2", "Catrin agreed."), st("2", "Katrin and Catrin."),
		st("3", "Owner Sam and Sams team."), st("3", "Dhaka is the second site."),
		st("1", "Ruiz and Dhaka."), st("1", "Ruiz in Dhaka."), st("1", "Ruiz, Dhaka."), st("2", "Ruiz: Dhaka."),
		// A colleague with a near name, credited with statements: a person.
		{ItemID: "2", Text: "Ilse said the plan holds.", Who: []string{"Ilse Novak"}}, st("1", "Ilsa agreed."), st("1", "Ilsa again."), st("1", "Ilsa, three."),
		st("1", "Ilsa, four."), st("1", "Ilsa, five."), st("1", "Ilsa, six."),
		// A word that is also near a name.
		st("1", "Wayde approved."), st("1", "Wayde again."), st("1", "Wayde, three."), st("1", "Wayde, four."), st("1", "Wayde, five."), st("1", "Wayde, six."), st("2", "They will Wade across."),
	}
	got := map[string]string{}
	for _, c := range AliasCandidates(items, sts, "Sam Owner") {
		got[c.Variant] = c.Keep
	}
	if got["Ruis"] != "Ruiz" || got["Daka"] != "Dhaka" {
		t.Errorf("variants not proposed: %v", got)
	}
	if _, ok := got["Katrin"]; ok {
		t.Errorf("two people proposed as one: %v", got)
	}
	if _, ok := got["Catrin"]; ok {
		t.Errorf("two people proposed as one: %v", got)
	}
	// Ilse is credited with a statement under a full name: a person.
	if _, ok := got["Ilse"]; ok {
		t.Errorf("a colleague proposed as another's variant: %v", got)
	}
	for _, pair := range [][2]string{{"Paul", "Paula"}, {"Mario", "Marie"}, {"Anders", "Anderson"}} {
		if !endingOnly(PrepNameKey(pair[0]), PrepNameKey(pair[1])) {
			t.Errorf("%v: two names, not a mishearing", pair)
		}
	}
	if endingOnly("ivor", "ivar") || endingOnly("rasko", "rasco") || endingOnly("ruiz", "ruis") || endingOnly("jenni", "jenny") {
		t.Error("a mishearing in the middle taken for two names")
	}
	if _, ok := got["Miles"]; ok && len(englishWords()) > 0 {
		t.Errorf("a word proposed as a name: %v", got)
	}
}

func TestDueDate(t *testing.T) {
	for s, want := range map[string]bool{"2026-09-25": true, "2026-09-25 (later today)": true, "end of quarter": false, "ASAP (set 9/22)": false, "": false} {
		if _, ok := dueDate(s); ok != want {
			t.Errorf("dueDate(%q) = %v", s, ok)
		}
	}
}

type fakeClassifier map[string]float64

func (f fakeClassifier) Name() string { return "fake" }
func (f fakeClassifier) Classify(_ context.Context, state any, _ map[string]provider.Question) (map[string]provider.Answer, error) {
	if state.(map[string]string)["meeting"] == "" {
		return nil, fmt.Errorf("no meeting given")
	}
	return map[string]provider.Answer{"sensitive": {Probabilities: f}}, nil
}

// A candidate's background is HR; the sensitive options' probabilities add
// up, so a split between two of them still labels.
func TestStatementSensitivityTyped(t *testing.T) {
	c, p, err := StatementSensitivityTyped(context.Background(), fakeClassifier{"candidate": 0.35, "people": 0.25, "work": 0.4}, "They grew the team to 90.", "Interview - VP")
	if err != nil || c != sensitivity.HR || p < 0.59 || p > 0.61 {
		t.Errorf("got %v %.2f %v", c, p, err)
	}
}

func TestAliasRules(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"Ruiz", "Ruis", true}, {"Mara", "Nara", false}, {"Karla", "Carla", true}, {"Philip", "Filip", true}} {
		if sameStart(c.a, c.b) != c.want {
			t.Errorf("sameStart(%q, %q) = %v", c.a, c.b, !c.want)
		}
	}
	if !acronym("CIOs") || !acronym("FDEs") || acronym("Ruiz") || acronym("A") {
		t.Error("acronym")
	}
	// A variant near two kept forms is put to nobody.
	st := func(item, text string) store.Statement { return store.Statement{ItemID: item, Text: text} }
	var sts []store.Statement
	for i := 0; i < 8; i++ {
		sts = append(sts, st("1", "Marten agreed."), st("1", "Martin agreed."))
	}
	sts = append(sts, st("2", "Marton said so."), st("2", "Marton again."))
	for _, c := range AliasCandidates(nil, sts, "Sam Owner") {
		if c.Variant == "Marton" {
			t.Errorf("ambiguous variant proposed: %+v", c)
		}
	}
}

// The same unresolved point carried by two related threads is listed once.
func TestDedupeOpen(t *testing.T) {
	got := dedupeOpen([]PrepOpen{
		{Text: "Rules to stop the vendor overloading the platform via the grading endpoint are not defined (1234)."},
		{Text: "Whether the team can define rules so the vendor won't overload the platform via the grading endpoint."},
		{Text: "The dataset must be reduced to the internal corpus."},
	})
	if len(got) != 2 {
		t.Errorf("kept %d: %+v", len(got), got)
	}
}

func TestStripIDsRange(t *testing.T) {
	if got := StripIDs("the disputed figure (20358-20365) and more [12, 14]."); got != "the disputed figure and more." {
		t.Errorf("got %q", got)
	}
}
