package gui

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/live"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// prepFixture is a store with a calendar event under way at `at`, an
// earlier meeting with one of its people, and no embedder (so nothing
// calls a model).
func prepFixture(t *testing.T, at time.Time) *engine.Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.DefaultConfig()
	cfg.Owner = config.Owner{Name: "Sam Owner", Email: "sam@corp.example", InternalDomains: []string{"corp.example"}}
	people := []item.Participant{{Name: "Robin Vale", Email: "robin@corp.example"}, {Name: "Kim New", Email: "kim@corp.example"}, {Name: "Sam Owner", Email: "sam@corp.example", Owner: true}}
	for _, it := range []item.Item{
		{ID: "m365-calendar:ev1", Source: "m365-calendar", Ref: "ev1", Kind: item.KindEvent, Title: "Juniper Weekly",
			OccurredAt: at.Add(-5 * time.Minute), EndedAt: at.Add(25 * time.Minute), Native: map[string]string{"response": "accepted"}, Participants: people},
		{ID: "m365-calendar:ev0", Source: "m365-calendar", Ref: "ev0", Kind: item.KindEvent, Title: "Vendor Call",
			OccurredAt: at.Add(-5 * time.Minute), EndedAt: at.Add(25 * time.Minute), Native: map[string]string{"response": "declined"}, Participants: people[:1]},
		{ID: "tomoe:m1", Source: "tomoe", Ref: "m1", Kind: item.KindMeeting, Title: "Juniper Weekly",
			OccurredAt: at.Add(-7 * 24 * time.Hour), EndedAt: at.Add(-7*24*time.Hour + 30*time.Minute), Participants: []item.Participant{people[0], people[2]}},
	} {
		if _, err := st.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
	}
	return &engine.Engine{Cfg: cfg, Store: st}
}

// The live session's event is the one the header names (the accepted
// invitation, not the declined one), and its prep is the Prep tab's view
// of that event plus the talking points drafted ahead.
func TestLivePrepForTheSessionsEvent(t *testing.T) {
	at := time.Now().Truncate(time.Second)
	e := prepFixture(t, at)
	id, title, _, err := e.Store.EventMatchAt(at)
	if err != nil || id != "m365-calendar:ev1" || title != "Juniper Weekly" {
		t.Fatalf("matched %q %q %v", id, title, err)
	}
	if err := e.Store.SavePrepBrief(store.PrepBrief{ItemID: id, Input: "x", Model: "m", Text: "- Ask Robin about the rollout [1]",
		Sources: []store.Hit{{ItemID: "tomoe:m1", Label: sensitivity.Label{Level: sensitivity.Internal}}}, MadeAt: at}); err != nil {
		t.Fatal(err)
	}
	p, err := livePrep(context.Background(), e, live.Session{ID: "s1", CreatedAt: at, EventID: id}, "owner")
	if err != nil || p == nil {
		t.Fatalf("prep %+v %v", p, err)
	}
	if p.EventID != id || p.SessionID != "s1" || p.View == nil || p.View.Title != "Juniper Weekly" {
		t.Fatalf("prep %+v", p)
	}
	if !slices.Equal(p.View.People, []string{"Kim New", "Robin Vale"}) || !slices.Equal(p.View.New, []string{"Kim New"}) || p.View.Met != 1 {
		t.Errorf("people %v, new %v, met %d", p.View.People, p.View.New, p.View.Met)
	}
	if p.Brief == nil || p.Brief.Text != "- Ask Robin about the rollout [1]" {
		t.Errorf("brief %+v", p.Brief)
	}
	// No calendar event matched: no card.
	if p, err := livePrep(context.Background(), e, live.Session{ID: "s2", CreatedAt: at}, "owner"); p != nil || err != nil {
		t.Errorf("no event: %+v %v", p, err)
	}
}

// The card reads in the panel's scope: talking points the shared screen
// may not see (no sources: confidential) are left out there, and an
// unknown scope fails rather than reading as the owner.
func TestLivePrepFollowsScope(t *testing.T) {
	at := time.Now().Truncate(time.Second)
	e := prepFixture(t, at)
	id := "m365-calendar:ev1"
	if err := e.Store.SavePrepBrief(store.PrepBrief{ItemID: id, Input: "x", Model: "m", Text: "- Raise the reorg", MadeAt: at}); err != nil {
		t.Fatal(err)
	}
	s := live.Session{ID: "s1", CreatedAt: at, EventID: id}
	own, err := livePrep(context.Background(), e, s, "owner")
	if err != nil || own == nil || own.Brief == nil {
		t.Fatalf("owner: %+v %v", own, err)
	}
	shared, err := livePrep(context.Background(), e, s, "shared-screen")
	if err != nil || shared == nil || shared.View == nil {
		t.Fatalf("shared: %+v %v", shared, err)
	}
	if shared.Brief != nil || shared.Scope != "shared-screen" {
		t.Errorf("shared screen got %+v (scope %q)", shared.Brief, shared.Scope)
	}
	if p, err := livePrep(context.Background(), e, s, "everyone"); err == nil || p != nil {
		t.Errorf("unknown scope read: %+v %v", p, err)
	}
}

// With live.prep_card off, or no meeting, there is no card and no draft.
func TestLivePrepOffOrIdle(t *testing.T) {
	at := time.Now()
	e := prepFixture(t, at)
	a := &App{ctx: context.Background(), eng: e, emitFn: func(string, any) {}}
	a.live = live.New(e, a.emit, nil)
	if p, err := a.LivePrep(); p != nil || err != nil {
		t.Errorf("idle: %+v %v", p, err)
	}
	if err := a.LivePrepBrief("x"); err == nil {
		t.Error("drafted with no meeting")
	}
	e.Cfg.Live.PrepCard = false
	if p, err := a.LivePrep(); p != nil || err != nil {
		t.Errorf("off: %+v %v", p, err)
	}
	if err := a.LivePrepBrief("x"); err == nil {
		t.Error("drafted with the card off")
	}
}
