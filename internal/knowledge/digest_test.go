package knowledge

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// digestLLM answers the summary and the to-do call of a digest.
type digestLLM struct {
	mu    sync.Mutex
	calls []provider.ChatOptions
	users []string
	sys   []string
}

func (f *digestLLM) Local() bool       { return true }
func (f *digestLLM) ModelName() string { return "fake" }
func (f *digestLLM) Complete(_ context.Context, msgs []provider.Message, opt provider.ChatOptions, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.users, f.sys = append(f.calls, opt), append(f.users, msgs[1].Content), append(f.sys, msgs[0].Content)
	if opt.Kind == "digest-summary" {
		return `{"overview":"Headcount review: the TAM group goes; no one is told before December.","sections":[
			{"subject":"TAM group","summary":"Raj Patel said no one in the TAM group should be kept, and that no one is notified before December.","people":["Raj Patel","You"],"lines":[[1,4]],"sensitive":["hr"]},
			{"subject":"Grace Holm out Friday","summary":"Grace Holm is out Friday for a family event.","people":["Grace Holm"],"lines":[[5,6]],"sensitive":[]},
			{"subject":"Nowhere","summary":"A section that cites lines outside the meeting.","people":[],"lines":[[1,99],[0,2]],"sensitive":[]},
			{"subject":"","summary":"No subject, so dropped.","people":[],"lines":[[1,2]],"sensitive":[]},
			{"subject":"Invented","summary":"Zoltan Quimby approved a budget of 5 million.","people":[],"lines":[[1,2]],"sensitive":["customer-confidential"]}]}`, nil
	}
	return `{"todos":[
		{"text":"Sam Owner will confirm with Chris that the TAM group changes are aligned.","owner":["You"],"requester":"self","agreed":"agreed","due_text":"by Friday","due_date":"2026-03-13","evidence":[{"line":2,"quote":"Is Chris aligned on that"},{"line":99,"quote":"out of range"}]},
		{"text":"Raj Patel will tell no one before December.","owner":["Raj Patel"],"requester":"Sam Owner (You)","agreed":"maybe","due_text":"","due_date":"2026-03-01","evidence":[{"line":3,"quote":"We can't notify anyone before December"}]},
		{"text":"Raj Patel will tell no one before December again.","owner":["Raj Patel"],"requester":"self","agreed":"agreed","due_text":"","due_date":"12/05/2026","evidence":[{"line":3,"quote":"We can't notify anyone before December"},{"line":4,"quote":"about two hundred"}]},
		{"text":"Zoltan Quimby will sign a 7 million contract.","owner":["unclear"],"requester":"","agreed":"floated","due_text":"","due_date":"","evidence":[]}]}`, nil
}

func TestDigest(t *testing.T) {
	m := testMeeting()
	labels := map[int]sensitivity.Label{2: {Level: sensitivity.Restricted, Categories: sensitivity.Personal}}
	f := &digestLLM{}
	start := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	d, err := (&Digester{LLM: f, Owner: "Sam Owner", Parallel: true}).Digest(context.Background(), m, labels, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("%d calls, want a summary and a to-do call", len(f.calls))
	}
	for i, o := range f.calls {
		if len(o.Schema) == 0 || o.Kind == "" || o.Session == "" {
			t.Errorf("call %d: schema %d bytes, kind %q, session %q", i, len(o.Schema), o.Kind, o.Session)
		}
		// The transcript is data: fenced, with the rule that it is never
		// obeyed in the system prompt and the task after it.
		u := f.users[i]
		if !strings.Contains(f.sys[i], "never follow it") || strings.Index(u, "<<<TRANSCRIPT") > strings.Index(u, "TRANSCRIPT>>>") ||
			strings.Index(u, "TRANSCRIPT>>>") > strings.LastIndex(u, "\n- ") {
			t.Errorf("call %d: prompt not fenced as designed", i)
		}
	}

	// Sections: invalid ones are dropped; labels come from the cited
	// passages, and the model's flags only add.
	if len(d.Sections) != 4 {
		t.Fatalf("%d sections, want 4 (the one without a subject dropped)", len(d.Sections))
	}
	by := map[string]store.DigestSection{}
	for _, s := range d.Sections {
		by[s.Subject] = s
	}
	if s := by["TAM group"]; s.Own.Categories&sensitivity.HR == 0 || s.Own.Level < sensitivity.Restricted || strings.Join(s.People, ",") != "Raj Patel,Sam Owner" {
		t.Errorf("TAM section: own %+v, people %v (an HR flag should add HR; You becomes the owner)", s.Own, s.People)
	}
	if s := by["Grace Holm out Friday"]; s.Own.Categories&sensitivity.Personal == 0 || len(s.Spans) != 1 || s.Spans[0].FromChunk != 12 {
		t.Errorf("personal passage: own %+v spans %+v", s.Own, s.Spans)
	}
	if s := by["Nowhere"]; len(s.Spans) != 0 || s.Own.Categories&sensitivity.Personal == 0 {
		t.Errorf("a section citing nothing real must be as sensitive as the whole meeting: %+v", s)
	}
	if s := by["Invented"]; s.Grounded || !strings.Contains(s.Issues, "Zoltan") {
		t.Errorf("invented name not flagged: grounded %v, %q", s.Grounded, s.Issues)
	}
	if by["TAM group"].Grounded != true {
		t.Errorf("a grounded section flagged: %q", by["TAM group"].Issues)
	}
	if d.OverviewOwn.Categories&sensitivity.Personal == 0 || d.OverviewOwn.Categories&sensitivity.HR == 0 {
		t.Errorf("the overview is as sensitive as the most sensitive section: %+v", d.OverviewOwn)
	}

	// To-dos: owner named, dates checked, evidence kept in range, the near
	// duplicate dropped, an unknown state read as asked, in order said.
	if len(d.Todos) != 3 {
		t.Fatalf("%d to-dos, want 3 (the duplicate dropped): %+v", len(d.Todos), d.Todos)
	}
	a, b, c := d.Todos[0], d.Todos[1], d.Todos[2]
	if a.Owners[0] != "Sam Owner" || a.DueDate != "2026-03-13" || len(a.Evidence) != 1 || a.Requester != "self" || a.N != 1 {
		t.Errorf("first to-do: %+v", a)
	}
	if b.Agreed != "agreed" && b.Agreed != "asked" || b.DueDate != "" {
		t.Errorf("second to-do (date before the meeting is dropped): %+v", b)
	}
	if b.DueDate != "" || len(b.Evidence) == 0 {
		t.Errorf("second to-do: %+v", b)
	}
	if c.Grounded || !strings.Contains(c.Issues, "Zoltan") {
		t.Errorf("an invented name in a to-do not flagged: %+v", c)
	}
	// With no evidence the to-do is as sensitive as the whole meeting.
	if c.Own.Categories&sensitivity.Personal == 0 {
		t.Errorf("to-do with no evidence: own %+v", c.Own)
	}
	if !a.SaidAt.Equal(start.Add(60*time.Second)) && !a.SaidAt.Equal(start) {
		t.Errorf("said at %v", a.SaidAt)
	}
}

func TestDigestDueDate(t *testing.T) {
	meet := time.Date(2026, 3, 9, 15, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{
		"2026-03-09": "2026-03-09", "2026-03-13": "2026-03-13", "2026-03-08": "", "2029-03-09": "2029-03-09", "2029-03-10": "",
		"12/05/2026": "", "2026-13-40": "", "": "", "2026-03-13; drop table": "", " 2026-04-01 ": "2026-04-01",
	} {
		if got := digestDueDate(in, meet); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestDigestLimits(t *testing.T) {
	m := testMeeting()
	var raw rawSummary
	for i := 0; i < maxSections+10; i++ {
		raw.Sections = append(raw.Sections, rawSection{Subject: strings.Repeat("s", 500), Summary: strings.Repeat("word ", 1000), Lines: [][]int{{1, 2}, {3, 4}, {1, 2}, {1, 2}, {1, 2}, {1, 2}, {1, 2}, {1, 2}}})
	}
	secs, _ := m.sectionsFrom(raw, nil, "Sam Owner")
	if len(secs) != maxSections || len([]rune(secs[0].Subject)) != maxSubjectLen || len([]rune(secs[0].Text)) > maxSectionLen || len(secs[0].Spans) != maxSpans {
		t.Errorf("limits: %d sections, subject %d, text %d, spans %d", len(secs), len([]rune(secs[0].Subject)), len([]rune(secs[0].Text)), len(secs[0].Spans))
	}
}

func TestLinkDigestTodos(t *testing.T) {
	sts := []store.Statement{
		{ID: 1, ChunkID: 10, Line: 3, Kind: "commitment", Holder: "Raj Patel", Text: "Raj Patel will notify no one before December."},
		{ID: 2, ChunkID: 10, Line: 3, Kind: "fact", Text: "Notice goes out after December begins."},
		{ID: 3, ChunkID: 11, Line: 1, Kind: "commitment", Holder: "Grace Holm", Text: "Grace Holm will send the onboarding deck to the new hires.", Evidence: []store.Evidence{{ChunkID: 11, Line: 1}}},
		{ID: 4, ChunkID: 12, Line: 9, Kind: "commitment", Text: "Someone else will do an unrelated audit of the vendor contracts."},
	}
	todos := []store.DigestTodo{
		{ID: 100, Text: "Raj Patel will tell no one before December.", Owners: []string{"Raj Patel"}, Evidence: []store.Evidence{{ChunkID: 10, Line: 4}}},
		{ID: 101, Text: "Grace Holm will send the new hires the onboarding deck.", Owners: []string{"Grace Holm"}, Evidence: []store.Evidence{{ChunkID: 11, Line: 2}}},
		{ID: 102, Text: "Pat Reyes will book the venue.", Evidence: []store.Evidence{{ChunkID: 10, Line: 3}}},
		{ID: 103, Text: "Raj Patel will tell no one before December this year.", Evidence: nil},
	}
	got := LinkDigestTodos(todos, sts)
	m := map[int64]int64{}
	for _, l := range got {
		m[l.Todo] = l.Statement
	}
	if m[100] != 1 || m[101] != 3 {
		t.Errorf("links: %v", m)
	}
	if _, ok := m[102]; ok {
		t.Errorf("a to-do that shares no words got a statement: %v", m)
	}
	// One statement for one to-do: the better match keeps it.
	if m[103] != 0 {
		t.Errorf("statement 1 matched twice: %v", m)
	}
}

// ToDos from a digest: the digest's date and state, with what came of the
// matched statement read as before, under the scope.
func TestToDosFromDigest(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	day := func(n int) time.Time { return time.Date(2026, 3, n, 10, 0, 0, 0, time.UTC) }
	mk := func(ref string, at time.Time, lvl sensitivity.Level) (string, []store.Chunk) {
		id := "t:" + ref
		it := item.Item{ID: id, Source: "t", Ref: ref, Kind: item.KindMeeting, Title: "meeting " + ref, OccurredAt: at, Segments: []item.Segment{{Text: "said"}}}
		if _, err := s.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceChunks(id, "v", "h", []store.Chunk{{Seq: 0, SegTo: 1, Text: "said"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceLabels(id, "v", "h", sensitivity.Label{Level: lvl}, nil, []string{"owner@corp.example"}); err != nil {
			t.Fatal(err)
		}
		cs, _ := s.Chunks(id)
		return id, cs
	}
	m1, c1 := mk("m1", day(1), 0)
	m2, c2 := mk("m2", day(2), 0)
	secret, c3 := mk("secret", day(3), sensitivity.Confidential)
	if err := s.ReplaceStatements(m1, "v", "h", []store.Statement{{ChunkID: c1[0].ID, Kind: "commitment", Status: "agreed", Text: "Robin will send the plan", Holder: "Robin Vale", Who: []string{"Robin Vale"}, SaidAt: day(1)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStatements(m2, "v", "h", []store.Statement{{ChunkID: c2[0].ID, Kind: "fact", Text: "The plan went out", Holder: "Robin Vale", SaidAt: day(2)}}); err != nil {
		t.Fatal(err)
	}
	// A meeting not summarised yet keeps its commitment statements.
	m4, c4 := mk("m4", day(4), 0)
	if err := s.ReplaceStatements(m4, "v", "h", []store.Statement{{ChunkID: c4[0].ID, Kind: "commitment", Status: "agreed", Text: "Kai will draft the agenda", Holder: "Kai Moreno", Who: []string{"Kai Moreno"}, SaidAt: day(4)}}); err != nil {
		t.Fatal(err)
	}
	st1, _ := s.ItemStatements(m1)
	st2, _ := s.ItemStatements(m2)
	if err := s.RelateStatements(st2[0].ID, st1[0].ID, "completes"); err != nil {
		t.Fatal(err)
	}
	put := func(id string, cs []store.Chunk, todos ...store.DigestTodo) {
		if err := s.ReplaceDigest(id, "d1", "h", "i", store.Digest{Overview: "o", Todos: todos}); err != nil {
			t.Fatal(err)
		}
	}
	put(m1, c1,
		store.DigestTodo{N: 1, Text: "Robin Vale will send the plan by Friday.", Owners: []string{"Robin Vale"}, Agreed: "agreed", DueDate: "2026-03-06", SaidAt: day(1), Grounded: true},
		store.DigestTodo{N: 2, Text: "Kai Moreno will book the venue.", Owners: []string{"Kai Moreno"}, Agreed: "asked", DueDate: "2026-03-20", SaidAt: day(1), Grounded: true},
		store.DigestTodo{N: 3, Text: "We could move the offsite.", Agreed: "floated", SaidAt: day(1), Grounded: true},
		store.DigestTodo{N: 4, Text: "Hold a retro.", Agreed: "declined", SaidAt: day(1), Grounded: true})
	put(secret, c3, store.DigestTodo{N: 1, Text: "Review the salaries.", Owners: []string{"Robin Vale"}, Agreed: "agreed", SaidAt: day(3), Grounded: true})
	todos, _ := s.DigestTodos(m1)
	if err := s.SetDigestLinks(m1, "l", []store.DigestLink{{Todo: todos[0].ID, Statement: st1[0].ID, Score: 0.8}}); err != nil {
		t.Fatal(err)
	}

	list := func(sc sensitivity.Scope, proposals bool) map[string]ToDo {
		ts, err := Views{Store: s, Scope: sc, Now: day(10)}.ToDos(ToDoRequest{Proposals: proposals, Source: "digest"}, "Robin Vale")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]ToDo{}
		for _, x := range ts {
			out[x.Text] = x
		}
		return out
	}
	got := list(sensitivity.Owner(), false)
	if x, ok := got["Kai will draft the agenda"]; !ok || x.State != "open" || x.Digest != 0 {
		t.Errorf("a meeting without a digest lost its to-do: %+v", x)
	}
	if _, ok := got["Robin will send the plan"]; ok {
		t.Error("a summarised meeting's statement listed besides its digest to-do")
	}
	only, err := Views{Store: s, Scope: sensitivity.Owner(), Now: day(10)}.ToDos(ToDoRequest{Source: "digest-only"}, "Robin Vale")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range only {
		if x.Digest == 0 {
			t.Errorf("digest-only listed a statement: %q", x.Text)
		}
	}
	if x := got["Robin Vale will send the plan by Friday."]; x.State != "done" || x.Outcome == nil || !x.Mine || x.Digest == 0 {
		t.Errorf("linked to-do: %+v", x)
	}
	x := got["Kai Moreno will book the venue."]
	if x.State != "open" || x.Mine || x.ID >= 0 || x.DueAt.IsZero() || x.Due == "" {
		t.Errorf("unlinked to-do: state %s, id %d, due %q", x.State, x.ID, x.Due)
	}
	if got["Hold a retro."].State != "changed" {
		t.Errorf("declined to-do: %+v", got["Hold a retro."])
	}
	if _, ok := got["We could move the offsite."]; ok {
		t.Error("a floated to-do without proposals")
	}
	if x := list(sensitivity.Owner(), true)["We could move the offsite."]; x.State != "proposed" || !x.DueAt.IsZero() {
		t.Errorf("floated to-do: %+v", x)
	}
	if _, ok := list(sensitivity.SharedScreen(), true)["Review the salaries."]; ok {
		t.Error("a confidential meeting's to-do on a shared screen")
	}

	if x := got["Kai Moreno will book the venue."]; x.Agreed != "asked" {
		t.Errorf("agreed reading: %q", x.Agreed)
	}

	// The owner's word on a to-do no statement matches: who it's for, then
	// done; each applies by meeting and text, and undoes.
	kai := got["Kai Moreno will book the venue."]
	who, err := s.LinkStatements(kai.ID, "who", 0, "Robin Vale, Kai Moreno", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if x := list(sensitivity.Owner(), false)["Kai Moreno will book the venue."]; !x.Mine || len(x.Owners) != 2 || len(x.Marks) != 1 {
		t.Errorf("after who: mine %v, owners %v, marks %d", x.Mine, x.Owners, len(x.Marks))
	}
	if _, err := s.LinkStatements(kai.ID, "done", 0, "booked", "owner"); err != nil {
		t.Fatal(err)
	}
	if x := list(sensitivity.Owner(), false)["Kai Moreno will book the venue."]; x.State != "done" || x.Outcome == nil || !x.Outcome.Owner {
		t.Errorf("after done: %+v", x)
	}
	if err := s.UndoOwnerLink(who); err != nil {
		t.Fatal(err)
	}
	if x := list(sensitivity.Owner(), false)["Kai Moreno will book the venue."]; x.Mine {
		t.Error("who still applied after undo")
	}
	if _, err := s.LinkStatements(-99999, "done", 0, "", "owner"); err == nil {
		t.Error("marked a to-do that doesn't exist")
	}

	// The meeting page: the digest, each to-do's state, and the topics.
	page, err := Views{Store: s, Scope: sensitivity.Owner(), Now: day(10)}.MeetingPage(m1, 1, 0, "Robin Vale")
	if err != nil || page == nil || len(page.ToDos) != 4 || page.Title != "meeting m1" {
		t.Fatalf("meeting page: %+v %v", page, err)
	}
	states := map[string]string{}
	for _, t := range page.ToDos {
		states[t.Text] = t.State
	}
	if states["Robin Vale will send the plan by Friday."] != "done" || states["Hold a retro."] != "declined" || states["We could move the offsite."] != "proposed" {
		t.Errorf("states on the page: %v", states)
	}
	if p, _ := (Views{Store: s, Scope: sensitivity.SharedScreen(), Now: day(10)}).MeetingPage(secret, 1, 0, ""); p != nil {
		t.Error("a confidential meeting's page on a shared screen")
	}
}

// emptyThenFull answers the to-do call with an empty list the first time.
type emptyThenFull struct {
	digestLLM
	todoCalls int
}

func (f *emptyThenFull) Complete(ctx context.Context, msgs []provider.Message, opt provider.ChatOptions, tok func(string)) (string, error) {
	if opt.Kind == "digest-todos" {
		f.mu.Lock()
		f.todoCalls++
		first := f.todoCalls == 1
		f.mu.Unlock()
		if first {
			return `{"todos":[]}`, nil
		}
	}
	return f.digestLLM.Complete(ctx, msgs, opt, tok)
}

// A long meeting answered with no to-dos is asked again; a short one isn't.
func TestDigestAsksAgainWhenEmpty(t *testing.T) {
	long := testMeeting()
	for i := 0; i < emptyTodosRetryLines; i++ {
		long.lines = append(long.lines, long.lines[0])
	}
	f := &emptyThenFull{}
	d, err := (&Digester{LLM: f, Owner: "Sam Owner"}).Digest(context.Background(), long, nil, time.Now())
	if err != nil || f.todoCalls != 2 || len(d.Todos) == 0 {
		t.Errorf("long meeting: %d to-do calls, %d to-dos, %v", f.todoCalls, len(d.Todos), err)
	}
	short := &emptyThenFull{}
	d, err = (&Digester{LLM: short, Owner: "Sam Owner"}).Digest(context.Background(), testMeeting(), nil, time.Now())
	if err != nil || short.todoCalls != 1 || len(d.Todos) != 0 {
		t.Errorf("short meeting: %d to-do calls, %d to-dos, %v", short.todoCalls, len(d.Todos), err)
	}
}

// bareThenFull answers the first to-do call with to-dos that cite nothing.
type bareThenFull struct {
	digestLLM
	todoCalls int
}

func (f *bareThenFull) Complete(ctx context.Context, msgs []provider.Message, opt provider.ChatOptions, tok func(string)) (string, error) {
	if opt.Kind == "digest-todos" {
		f.mu.Lock()
		f.todoCalls++
		first := f.todoCalls == 1
		f.mu.Unlock()
		if first {
			return `{"todos":[{"text":"Raj Patel will tell no one before December.","owner":[],"requester":"unclear","agreed":"agreed","due_text":"","due_date":"","evidence":[]},
				{"text":"Sam Owner will confirm with Chris.","owner":[],"requester":"unclear","agreed":"agreed","due_text":"","due_date":"","evidence":[]}]}`, nil
		}
	}
	return f.digestLLM.Complete(ctx, msgs, opt, tok)
}

// To-dos that cite no lines are asked for again, and the reading that cites
// more is kept; one that is no better than the first isn't swapped in.
func TestDigestAsksAgainWithoutEvidence(t *testing.T) {
	f := &bareThenFull{}
	long := testMeeting()
	for i := 0; i < emptyTodosRetryLines; i++ {
		long.lines = append(long.lines, long.lines[0])
	}
	d, err := (&Digester{LLM: f, Owner: "Sam Owner"}).Digest(context.Background(), long, nil, time.Now())
	if err != nil || f.todoCalls != 2 {
		t.Fatalf("%d to-do calls, %v", f.todoCalls, err)
	}
	for _, td := range d.Todos {
		if len(td.Evidence) == 0 && td.Grounded {
			t.Errorf("a to-do without evidence reads as grounded: %+v", td)
		}
	}
	if len(d.Todos) == 0 || len(d.Todos[0].Evidence) == 0 {
		t.Errorf("the second reading wasn't kept: %+v", d.Todos)
	}
}
