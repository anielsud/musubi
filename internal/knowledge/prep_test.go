package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
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
