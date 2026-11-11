package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

func digestStore(t *testing.T, itemLevel sensitivity.Level) (*Store, string, []Chunk) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id := "t:m1"
	it := item.Item{ID: id, Source: "t", Ref: "m1", Kind: item.KindMeeting, Title: "Planning sync", OccurredAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
		Segments: []item.Segment{{Text: "said"}}}
	if _, err := s.UpsertItem(it); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceChunks(id, "v", "h", []Chunk{{Seq: 0, SegTo: 1, Text: "Robin Vale: I will send the plan."}, {Seq: 1, SegTo: 2, Text: "Kai Moreno: Okay."}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLabels(id, "v", "h", sensitivity.Label{Level: itemLevel}, nil, []string{"owner@corp.example"}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Chunks(id)
	if err != nil {
		t.Fatal(err)
	}
	return s, id, cs
}

func sampleDigest(id string, cs []Chunk) Digest {
	at := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	return Digest{
		Overview: "A planning sync.",
		Sections: []DigestSection{
			{N: 1, Subject: "Plan", Text: "Robin Vale will send the plan.", People: []string{"Robin Vale"}, Grounded: true,
				Spans: []Span{{FromChunk: cs[0].ID, FromLine: 1, ToChunk: cs[0].ID, ToLine: 1}}},
			{N: 2, Subject: "Pay", Text: "Salary review for Kai Moreno.", Grounded: true,
				Own: sensitivity.Label{Level: sensitivity.Restricted, Categories: sensitivity.Compensation}},
		},
		Todos: []DigestTodo{
			{N: 1, Text: "Robin Vale will send the plan.", Owners: []string{"Robin Vale"}, Agreed: "agreed", DueDate: "2026-03-06", SaidAt: at, Grounded: true,
				Evidence: []Evidence{{ChunkID: cs[0].ID, Line: 1, Quote: "I will send the plan"}}},
			{N: 2, Text: "Kai Moreno could look at the venue.", Owners: []string{"Kai Moreno"}, Agreed: "floated", SaidAt: at, Grounded: true},
			{N: 3, Text: "Raise the pay review.", Agreed: "declined", SaidAt: at, Grounded: true,
				Own: sensitivity.Label{Level: sensitivity.Restricted, Categories: sensitivity.Compensation}},
		},
	}
}

// A digest is read under the scope: a row as sensitive as its own label
// is hidden from a narrower scope, and an item the scope can't see has no
// digest at all.
func TestMeetingDigestScoped(t *testing.T) {
	s, id, cs := digestStore(t, sensitivity.Internal)
	if err := s.ReplaceDigest(id, "d1", "h1", "in1", sampleDigest(id, cs)); err != nil {
		t.Fatal(err)
	}
	d, err := s.MeetingDigest(sensitivity.Owner(), id)
	if err != nil || d == nil {
		t.Fatalf("owner: %v %v", d, err)
	}
	if len(d.Sections) != 2 || len(d.Todos) != 3 || d.ItemTitle != "Planning sync" {
		t.Errorf("owner sees %d sections, %d to-dos, %q", len(d.Sections), len(d.Todos), d.ItemTitle)
	}
	d, err = s.MeetingDigest(sensitivity.SharedScreen(), id)
	if err != nil || d == nil {
		t.Fatalf("shared screen: %v %v", d, err)
	}
	if len(d.Sections) != 1 || d.Sections[0].Subject != "Plan" || len(d.Todos) != 2 {
		t.Errorf("shared screen sees %d sections, %d to-dos", len(d.Sections), len(d.Todos))
	}

	// A confidential item: nothing of its digest on a shared screen.
	s2, id2, cs2 := digestStore(t, sensitivity.Confidential)
	if err := s2.ReplaceDigest(id2, "d1", "h1", "in1", sampleDigest(id2, cs2)); err != nil {
		t.Fatal(err)
	}
	if d, err := s2.MeetingDigest(sensitivity.SharedScreen(), id2); err != nil || d != nil {
		t.Errorf("confidential item's digest on a shared screen: %v %v", d, err)
	}
	if l, _ := s2.DigestedItems(sensitivity.SharedScreen(), 10); len(l) != 0 {
		t.Errorf("meeting list on a shared screen: %d", len(l))
	}
	if l, _ := s2.DigestedItems(sensitivity.Owner(), 10); len(l) != 1 || l[0].Sections != 2 || l[0].Todos != 3 {
		t.Errorf("owner's meeting list: %+v", l)
	}
}

// Replacing a digest replaces everything of it; forgetting the item takes
// it too.
func TestReplaceAndForgetDigest(t *testing.T) {
	s, id, cs := digestStore(t, sensitivity.Internal)
	d := sampleDigest(id, cs)
	if err := s.ReplaceDigest(id, "d1", "h1", "in1", d); err != nil {
		t.Fatal(err)
	}
	d.Sections, d.Todos = d.Sections[:1], d.Todos[:1]
	if err := s.ReplaceDigest(id, "d1", "h2", "in1", d); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MeetingDigest(sensitivity.Owner(), id); len(got.Sections) != 1 || len(got.Todos) != 1 {
		t.Errorf("after replace: %d sections, %d to-dos", len(got.Sections), len(got.Todos))
	}
	if st, _ := s.StageStamp("digest", id); st != "d1/h2" {
		t.Errorf("stamp %q", st)
	}
	if _, _, err := s.Forget(id); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, tb := range []string{"meeting_digests", "meeting_sections", "meeting_todos"} {
		s.db.QueryRow(`SELECT count(*) FROM ` + tb).Scan(&n)
		if n != 0 {
			t.Errorf("%s keeps %d rows after the item is forgotten", tb, n)
		}
	}
}

// DigestCommitments: a to-do that matched a statement is that statement
// (its id, so what came of it is read as before) with the to-do's own
// words; one that matched none has a negative id; floated ones need
// proposals; declined ones are closed by the meeting.
func TestDigestCommitments(t *testing.T) {
	s, id, cs := digestStore(t, sensitivity.Internal)
	at := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	if err := s.ReplaceStatements(id, "v", "h", []Statement{
		{ChunkID: cs[0].ID, Line: 1, Kind: "commitment", Status: "agreed", Text: "Robin Vale will send the plan", Holder: "Robin Vale", SaidAt: at},
		{ChunkID: cs[1].ID, Line: 1, Kind: "fact", Status: "stated", Text: "The plan was sent", Holder: "Robin Vale", SaidAt: at.Add(24 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	sts, _ := s.ItemStatements(id)
	if err := s.RelateStatements(sts[1].ID, sts[0].ID, "completes"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDigest(id, "d1", "h1", "in1", sampleDigest(id, cs)); err != nil {
		t.Fatal(err)
	}
	todos, _ := s.DigestTodos(id)
	if err := s.SetDigestLinks(id, "l1", []DigestLink{{Todo: todos[0].ID, Statement: sts[0].ID, Score: 0.9}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.DigestCommitments(sensitivity.Owner(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("without proposals: %d commitments, want the agreed and the declined", len(got))
	}
	byText := map[string]Commitment{}
	for _, c := range got {
		byText[c.Text] = c
	}
	plan := byText["Robin Vale will send the plan."]
	if plan.ID != sts[0].ID || plan.DueDate != "2026-03-06" || len(plan.Later) != 1 || plan.Later[0].Rel != "completes" || plan.Status != "agreed" {
		t.Errorf("linked to-do: id %d (statement %d), due %q, later %+v, status %q", plan.ID, sts[0].ID, plan.DueDate, plan.Later, plan.Status)
	}
	declined := byText["Raise the pay review."]
	if declined.ID >= 0 || len(declined.Later) != 1 || declined.Later[0].Rel != "supersedes" {
		t.Errorf("declined to-do: id %d later %+v", declined.ID, declined.Later)
	}
	// Each carries its effective label (item and digest row), matched or
	// not: an unset label would read as public.
	if plan.Label.Level != sensitivity.Internal || declined.Label.Level != sensitivity.Restricted || declined.Label.Categories != sensitivity.Compensation {
		t.Errorf("labels: plan %+v, declined %+v", plan.Label, declined.Label)
	}
	got, _ = s.DigestCommitments(sensitivity.Owner(), true)
	if len(got) != 3 {
		t.Errorf("with proposals: %d", len(got))
	}
	// The pay review is restricted: not on a shared screen.
	got, _ = s.DigestCommitments(sensitivity.SharedScreen(), true)
	for _, c := range got {
		if c.Text == "Raise the pay review." {
			t.Error("a restricted to-do on a shared screen")
		}
	}

	// Re-extraction deletes the statements: the to-do is left unlinked,
	// not dangling.
	if err := s.ReplaceStatements(id, "v", "h2", nil); err != nil {
		t.Fatal(err)
	}
	todos, _ = s.DigestTodos(id)
	if todos[0].StatementID != 0 {
		t.Errorf("to-do still linked to a deleted statement: %d", todos[0].StatementID)
	}
}

// MeetingTopics: threads holding enough of the meeting's statements, most
// first.
func TestMeetingTopics(t *testing.T) {
	s, id, cs := digestStore(t, sensitivity.Internal)
	at := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	var sts []Statement
	for i := 0; i < 5; i++ {
		sts = append(sts, Statement{ChunkID: cs[0].ID, Line: 1, Kind: "fact", Text: "fact " + string(rune('a'+i)), SaidAt: at})
	}
	if err := s.ReplaceStatements(id, "v", "h", sts); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ItemStatements(id)
	for _, tp := range []struct {
		id    int64
		label string
		n     []int
	}{{1, "Big thread", []int{0, 1, 2, 3}}, {2, "Small thread", []int{4}}} {
		s.db.Exec(`INSERT INTO topics(id, label, summary, label_level, size) VALUES (?,?,?,?,?)`, tp.id, tp.label, "", 0, 10)
		for _, k := range tp.n {
			s.db.Exec(`INSERT INTO thread_items(topic_id, statement_id, current) VALUES (?,?,1)`, tp.id, got[k].ID)
		}
	}
	ts, err := s.MeetingTopics(sensitivity.Owner(), id, 1)
	if err != nil || len(ts) != 2 || ts[0].Label != "Big thread" || ts[0].Statements != 4 {
		t.Fatalf("min 1: %+v %v", ts, err)
	}
	ts, _ = s.MeetingTopics(sensitivity.Owner(), id, 3)
	if len(ts) != 1 || ts[0].Label != "Big thread" {
		t.Errorf("min 3: %+v", ts)
	}
}
