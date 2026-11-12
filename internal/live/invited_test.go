package live

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/store"
)

// Who's in the call leaves the owner out, by name or by email.
func TestInvitedLeavesOwnerOut(t *testing.T) {
	got := others([]item.Participant{
		{Name: "Sam Owner", Email: "sam@acme.example"},
		{Name: "Robin Vale", Email: "robin@acme.example"},
		{Name: "S. Owner", Email: "SAM@acme.example"},
		{Name: ""},
	}, "Sam Owner", "sam@acme.example")
	if !slices.Equal(got, []string{"Robin Vale"}) {
		t.Errorf("invited = %v", got)
	}
}

// A meeting that starts during a calendar event takes the event's title,
// its other invitees, and its item id (for the prep card).
func TestSessionStartMatchesEvent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	if _, err := st.UpsertItem(item.Item{ID: "m365-calendar:e1", Source: "m365-calendar", Ref: "e1", Kind: item.KindEvent, Title: "Roadmap Review",
		OccurredAt: at, EndedAt: at.Add(time.Hour), Native: map[string]string{"response": "accepted"},
		Participants: []item.Participant{{Name: "Robin Vale", Email: "robin@acme.example"}, {Name: "Sam Owner", Email: "sam@acme.example"}}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Owner.Name, cfg.Owner.Email = "Sam Owner", "sam@acme.example"
	l := New(&engine.Engine{Cfg: cfg, Store: st}, func(string, any) {}, nil)
	l.handle(Event{V: 1, Type: "session", State: "started", Session: &Session{ID: "s", Title: "Recording 15:02", CreatedAt: at.Add(2 * time.Minute)}}, "live")
	s := l.Status().Session
	if s == nil || s.Title != "Roadmap Review" || s.EventID != "m365-calendar:e1" || !slices.Equal(s.Invited, []string{"Robin Vale"}) {
		t.Fatalf("session %+v", s)
	}
}
