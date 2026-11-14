package phone

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// fakeAgendaStore holds synthetic events, labels and briefs.
type fakeAgendaStore struct {
	items  []item.Item
	labels map[string]sensitivity.Label
	briefs map[string]store.PrepBrief
}

func (f fakeAgendaStore) AllItems() ([]item.Item, error) { return f.items, nil }
func (f fakeAgendaStore) ItemLabel(id string) (sensitivity.Label, []string, error) {
	l, ok := f.labels[id]
	if !ok {
		return sensitivity.Label{}, nil, errors.New("no label")
	}
	return l, nil, nil
}
func (f fakeAgendaStore) PrepBriefFor(sc sensitivity.Scope, id string) (*store.PrepBrief, error) {
	b, ok := f.briefs[id]
	if !ok || !sensitivity.Visible(b.Label, nil, sc) {
		return nil, nil
	}
	return &b, nil
}

func TestAgendaScope(t *testing.T) {
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	ev := func(id string, in time.Duration, native map[string]string) item.Item {
		return item.Item{ID: id, Kind: item.KindEvent, Title: "Subject " + id, OccurredAt: now.Add(in), EndedAt: now.Add(in + time.Hour), Native: native,
			Participants: []item.Participant{{Name: "Robin Vale", Role: "organizer"}, {Name: "Kai Moreno", Role: "attendee"}, {Name: "Sam Owner", Role: "attendee", Owner: true}}}
	}
	day := 24 * time.Hour
	f := fakeAgendaStore{
		items: []item.Item{
			ev("calendar:later", 3*day, map[string]string{"location": "Hall B"}),
			ev("calendar:now", -30*time.Minute, nil), // under way: kept
			ev("calendar:over", -2*time.Hour, nil),   // ended: dropped
			ev("calendar:far", 13*day, nil),          // inside 14 days
			ev("calendar:too-far", 15*day, nil),      // beyond the cap
			ev("calendar:cancelled", day, map[string]string{"cancelled": "true"}),
			ev("calendar:private", 2*day, nil),    // confidential event
			ev("calendar:unlabelled", 2*day, nil), // no label: confidential
			{ID: "tomoe:m", Kind: item.KindMeeting, OccurredAt: now.Add(day), EndedAt: now.Add(day + time.Hour)},
		},
		labels: map[string]sensitivity.Label{
			"calendar:later": {Level: sensitivity.Internal}, "calendar:now": {Level: sensitivity.Internal},
			"calendar:over": {Level: sensitivity.Internal}, "calendar:far": {Level: sensitivity.Internal},
			"calendar:too-far": {Level: sensitivity.Internal}, "calendar:cancelled": {Level: sensitivity.Internal},
			"calendar:private": {Level: sensitivity.Confidential},
		},
		briefs: map[string]store.PrepBrief{
			"calendar:later": {ItemID: "calendar:later", Text: "Ask about the rollout.", Label: sensitivity.Label{Level: sensitivity.Confidential}, MadeAt: now},
			"calendar:far":   {ItemID: "calendar:far", Text: "Public plan.", Label: sensitivity.Label{Level: sensitivity.Public}, MadeAt: now},
		},
	}
	ids := func(evs []Event) string {
		var s []string
		for _, e := range evs {
			s = append(s, e.ID)
		}
		return strings.Join(s, " ")
	}
	evs, err := BuildAgenda(f, sensitivity.Owner(), now, 30)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(evs); got != "calendar:now calendar:private calendar:unlabelled calendar:later calendar:far" {
		t.Errorf("owner, 30 days (capped at 14): %s", got)
	}
	if got := ids(must(BuildAgenda(f, sensitivity.Owner(), now, 10))); got != "calendar:now calendar:private calendar:unlabelled calendar:later" {
		t.Errorf("owner, 10 days: %s", got)
	}
	later := evs[3]
	if later.Location != "Hall B" || later.Organizer != "Robin Vale" || len(later.Attendees) != 2 || later.Brief == nil {
		t.Fatalf("event %+v", later)
	}
	if b := later.Brief.Sensitivity; b.Level != "confidential" || b.Share != sensitivity.ShareParty || b.Why == "" {
		t.Errorf("brief label %+v", b)
	}
	// A narrower scope sees neither the confidential events nor the
	// confidential brief.
	narrow := must(BuildAgenda(f, sensitivity.UpTo(sensitivity.ShareInternal), now, 14))
	if got := ids(narrow); got != "calendar:now calendar:later calendar:far" {
		t.Errorf("internal scope: %s", got)
	}
	if narrow[1].Brief != nil || narrow[2].Brief == nil {
		t.Error("internal scope: briefs not scoped")
	}
	// The reply's JSON shape is the contract's.
	b, _ := json.Marshal(Agenda{Events: evs[3:4], Drafting: 2, MadeAt: now})
	for _, k := range []string{`"id":`, `"start":`, `"end":`, `"subject":`, `"location":`, `"organizer":`, `"attendees":[{"name":`, `"brief":{"text":`, `"made_at":`, `"sensitivity":{"level":`, `"share":`, `"why":`, `"drafting":2`, `"made_at":"2026-11-16T08:00:00Z"}`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("agenda JSON lacks %s: %s", k, b)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestDrafter(t *testing.T) {
	d := &Drafter{Every: time.Hour}
	now := time.Now()
	release := make(chan struct{})
	var visit func(string)
	got := make(chan struct{})
	n := d.Start(now, func() []string { return []string{"calendar:a", "calendar:b"} }, func(v func(string)) {
		visit = v
		close(got)
		<-release
	})
	<-got
	if n != 2 {
		t.Fatalf("first: %d", n)
	}
	visit("calendar:a")
	if n := d.Start(now, func() []string { t.Error("missing asked during a pass"); return nil }, nil); n != 1 {
		t.Errorf("during the pass: %d", n)
	}
	close(release)
	for i := 0; i < 100; i++ {
		d.mu.Lock()
		running := d.running
		d.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Within Every of the last pass, nothing starts.
	if n := d.Start(time.Now(), func() []string { t.Error("missing asked too soon"); return nil }, nil); n != 0 {
		t.Errorf("too soon: %d", n)
	}
	// Later, with nothing missing, nothing runs.
	if n := d.Start(time.Now().Add(2*time.Hour), func() []string { return nil }, func(func(string)) { t.Error("ran with nothing missing") }); n != 0 {
		t.Errorf("nothing missing: %d", n)
	}
}
