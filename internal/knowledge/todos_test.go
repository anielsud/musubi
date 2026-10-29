package knowledge

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// TestToDos: what came of each commitment is read from later statements
// (done, changed, raised again), under the scope; proposals only on request.
func TestToDos(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	day := func(n int) time.Time { return time.Date(2026, 3, n, 10, 0, 0, 0, time.UTC) }
	type st struct{ kind, status, text, who string }
	meetings := []struct {
		ref   string
		at    time.Time
		level sensitivity.Level
		sts   []st
	}{
		{"m1", day(1), 0, []st{
			{"commitment", "agreed", "Robin Vale will send the plan by 20 March", "Robin Vale"},
			{"commitment", "agreed", "Kai Moreno will book the venue", "Kai Moreno"},
			{"position", "proposed", "Kai Moreno proposed moving the offsite", "Kai Moreno"},
			{"commitment", "agreed", "Kai Moreno will fix the build", "Kai Moreno"},
		}},
		{"m2", day(2), 0, []st{
			{"commitment", "agreed", "Kai Moreno will draft the memo", "Kai Moreno"},
		}},
		{"m3", day(3), 0, []st{
			{"fact", "stated", "The venue is booked", "Kai Moreno"},
			{"commitment", "agreed", "Kai Moreno will draft the memo this week", "Kai Moreno"},
		}},
		{"secret", day(4), sensitivity.Confidential, []st{
			{"fact", "stated", "The build is fixed", "Kai Moreno"},
			{"commitment", "agreed", "Robin Vale will review the salaries", "Robin Vale"},
		}},
	}
	ids := map[string]int64{}
	for _, m := range meetings {
		id := "t:" + m.ref
		it := item.Item{ID: id, Source: "t", Ref: m.ref, Kind: item.KindMeeting, Title: "meeting " + m.ref, OccurredAt: m.at,
			Segments: []item.Segment{{Text: "said"}}}
		if _, err := s.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceChunks(id, "v", "h", []store.Chunk{{Seq: 0, SegTo: 1, Text: "said"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceLabels(id, "v", "h", sensitivity.Label{Level: m.level}, nil, []string{"owner@corp.example"}); err != nil {
			t.Fatal(err)
		}
		cs, _ := s.Chunks(id)
		var sts []store.Statement
		for _, x := range m.sts {
			sts = append(sts, store.Statement{ChunkID: cs[0].ID, Kind: x.kind, Status: x.status, Text: x.text, Holder: x.who, Who: []string{x.who}, SaidAt: m.at})
		}
		if err := s.ReplaceStatements(id, "v", "h", sts); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.AllStatements()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range all {
		ids[x.Text] = x.ID
	}
	for _, r := range [][3]string{
		{"The venue is booked", "Kai Moreno will book the venue", "completes"},
		{"Kai Moreno will draft the memo this week", "Kai Moreno will draft the memo", "repeats"},
		{"The build is fixed", "Kai Moreno will fix the build", "completes"},
	} {
		if err := s.RelateStatements(ids[r[0]], ids[r[1]], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	list := func(sc sensitivity.Scope, req ToDoRequest) map[string]ToDo {
		ts, err := Views{Store: s, Scope: sc, Now: day(10)}.ToDos(req, "Robin Vale")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]ToDo{}
		for _, x := range ts {
			out[x.Text] = x
		}
		return out
	}

	got := list(sensitivity.Owner(), ToDoRequest{})
	if x := got["Robin Vale will send the plan by 20 March"]; x.State != "open" || !x.Mine || x.DueAt.IsZero() || x.Overdue {
		t.Errorf("plan: %+v", x)
	}
	if x := got["Kai Moreno will book the venue"]; x.State != "done" || x.Outcome == nil || x.Outcome.Text != "The venue is booked" || x.Mine {
		t.Errorf("venue: %+v", x)
	}
	if _, ok := got["Kai Moreno will draft the memo"]; ok {
		t.Error("a repeated commitment is listed twice")
	}
	if x := got["Kai Moreno will draft the memo this week"]; x.Raised != 2 || len(x.Repeats) != 1 {
		t.Errorf("memo: raised %d, repeats %d", x.Raised, len(x.Repeats))
	}
	if x := got["Kai Moreno will fix the build"]; x.State != "done" {
		t.Errorf("owner sees the build fixed: %+v", x.State)
	}
	if _, ok := got["Kai Moreno proposed moving the offsite"]; ok {
		t.Error("a proposal listed without proposals")
	}
	if len(got) != 5 {
		t.Errorf("owner sees %d", len(got))
	}

	// On a shared screen the confidential meeting is unseen: its commitment
	// isn't listed, and what it said came of another is unknown.
	got = list(sensitivity.SharedScreen(), ToDoRequest{Proposals: true})
	if _, ok := got["Robin Vale will review the salaries"]; ok {
		t.Error("a confidential commitment on a shared screen")
	}
	if x := got["Kai Moreno will fix the build"]; x.State != "open" || x.Outcome != nil {
		t.Errorf("shared screen sees the build fixed: %+v", x)
	}
	if x := got["Kai Moreno proposed moving the offsite"]; x.State != "proposed" {
		t.Errorf("proposal: %+v", x.State)
	}

	// Quiet: nothing said of it for longer than QuietAfter.
	got = list(sensitivity.Owner(), ToDoRequest{QuietAfter: 8 * 24 * time.Hour})
	if got["Robin Vale will send the plan by 20 March"].State != "quiet" || got["Kai Moreno will draft the memo this week"].State != "open" {
		t.Errorf("quiet: %s, %s", got["Robin Vale will send the plan by 20 March"].State, got["Kai Moreno will draft the memo this week"].State)
	}
}
