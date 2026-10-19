package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
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
	items := []item.Item{mk("1", "Dana Ruiz", "Kristian Holm"), mk("2", "Christian Berg"), mk("3", "Sam Owner")}
	st := func(item, text string) store.Statement { return store.Statement{ItemID: item, Text: text} }
	sts := []store.Statement{
		st("1", "Ruiz said the Dhaka office is ready."), st("1", "Ruiz again: Dhaka hiring."), st("2", "Ruis will check the Daka numbers."),
		st("2", "Ruis and Daka again."), st("1", "Kristian presented."), st("2", "Christian agreed."), st("2", "Kristian and Christian."),
		st("3", "Owner Sam and Sams team."), st("3", "Dhaka is the second site."),
	}
	got := map[string]string{}
	for _, c := range AliasCandidates(items, sts, "Sam Owner") {
		got[c.Variant] = c.Keep
	}
	if got["Ruis"] != "Ruiz" || got["Daka"] != "Dhaka" {
		t.Errorf("variants not proposed: %v", got)
	}
	if _, ok := got["Kristian"]; ok {
		t.Errorf("two people proposed as one: %v", got)
	}
	if _, ok := got["Christian"]; ok {
		t.Errorf("two people proposed as one: %v", got)
	}
}
