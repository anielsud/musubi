package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Limits on what one call returns, so a careless request can't pull the
// whole record into one answer.
const (
	maxQuery      = 2000
	passageChars  = 2000
	sourceChars   = 60000
	maxSearch     = 25
	maxThreads    = 50
	maxItems      = 100
	maxMeetings   = 100
	maxToDos      = 200
	maxRecipients = 50
)

// Every read tool takes "for", the optional audience (the schema
// generator doesn't flatten embedded structs, so each input repeats it):
//
//	For []string `json:"for,omitempty" jsonschema:"..."`
const forDoc = `who will read what you write from this: email addresses, "internal" (other colleagues) and/or "external" (people outside the company); each result then says whether it fits`

func checkFor(f []string) error {
	if len(f) > maxRecipients {
		return fmt.Errorf("for: at most %d recipients", maxRecipients)
	}
	return nil
}

func (s *Server) views() knowledge.Views {
	return knowledge.Views{Store: s.e.Store, Scope: s.sc, Now: time.Now()}
}

func day(s, field string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return t, fmt.Errorf("%s: want a date as 2006-01-02", field)
	}
	return t, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Passage is a piece of a source item.
type Passage struct {
	N           int       `json:"n,omitempty"`
	ItemID      string    `json:"itemId"`
	Title       string    `json:"title"`
	Kind        string    `json:"kind"`
	When        time.Time `json:"when"`
	Speakers    string    `json:"speakers,omitempty"`
	Text        string    `json:"text"`
	Sensitivity Sens      `json:"sensitivity"`
}

func (l *labeller) passage(h store.Hit, n int) Passage {
	return Passage{N: n, ItemID: h.ItemID, Title: h.Title, Kind: h.Kind, When: h.OccurredAt, Speakers: h.Speakers,
		Text: clip(h.Text, passageChars), Sensitivity: l.item(h.Label, h.ItemID)}
}

// Item is a statement: something said, in a thread or a to-do.
type Item struct {
	ID          int64     `json:"id"`
	Said        time.Time `json:"said"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status,omitempty"`
	Who         string    `json:"who,omitempty"`
	Text        string    `json:"text"`
	ItemID      string    `json:"itemId"`
	Source      string    `json:"source"`
	Sensitivity Sens      `json:"sensitivity"`
}

func (l *labeller) statement(st store.Statement) Item {
	return Item{ID: st.ID, Said: st.SaidAt, Kind: st.Kind, Status: st.Status, Who: st.Holder, Text: st.Text,
		ItemID: st.ItemID, Source: st.ItemTitle, Sensitivity: l.item(st.Label, st.ItemID)}
}

func (l *labeller) statements(its []store.ThreadItem, n int) []Item {
	out := []Item{}
	for _, it := range its[:min(len(its), n)] {
		out = append(out, l.statement(it.Statement))
	}
	return out
}

// statusLabels is each thread's status label, for labelling statuses.
func (s *Server) statusLabels() (map[int64]sensitivity.Label, error) {
	rows, err := s.e.Store.ThreadStatuses(s.sc)
	if err != nil {
		return nil, err
	}
	out := map[int64]sensitivity.Label{}
	for id, r := range rows {
		out[id] = sensitivity.Label{Level: r.Level, Categories: r.Categories}
	}
	return out, nil
}

func (s *Server) readTools() []tool {
	return []tool{
		define(s, &mcp.Tool{Name: "search", Annotations: readOnly("Search Musubi"),
			Description: "Search the owner's meetings, email and chat for passages (keyword and meaning). Use for what someone said or when something came up; for \"where are we on X\" use gather or thread."}, s.search),
		define(s, &mcp.Tool{Name: "gather", Annotations: readOnly("Gather what Musubi knows about a question"),
			Description: "Everything Musubi would read to answer a question: the threads it is about (written status and latest items) and passages found by search, numbered for citation. Write the answer yourself from it; prefer the newest information and cite sources by number."}, s.gather),
		define(s, &mcp.Tool{Name: "threads", Annotations: readOnly("Find threads"),
			Description: "Threads are subjects followed across meetings, with a written status. With a query, the threads it is about; without, the most recently active."}, s.threads),
		define(s, &mcp.Tool{Name: "thread", Annotations: readOnly("Read a thread"),
			Description: "One thread: its written status (now, what changed, what is open, who drives it), its current items (newest first), optionally retired ones, and related threads. Status points cite item ids."}, s.thread),
		define(s, &mcp.Tool{Name: "meetings", Annotations: readOnly("List meetings"),
			Description: "Recent meetings that have a summary, newest first, with their overview."}, s.meetings),
		define(s, &mcp.Tool{Name: "meeting", Annotations: readOnly("Read a meeting summary"),
			Description: "One meeting's summary: overview, sections by subject, to-dos with how they stand now, and the threads it touched."}, s.meeting),
		define(s, &mcp.Tool{Name: "source", Annotations: readOnly("Read a source"),
			Description: "The text of one source item (a meeting transcript, email or chat) in order, with who was party to it. Long items come in pages: pass from_seq from next_seq."}, s.source),
		define(s, &mcp.Tool{Name: "todos", Annotations: readOnly("List to-dos"),
			Description: "What was said would or should happen, and what came of it: open first (overdue, then due soonest), then quiet ones. Ids can be passed to mark_todo."}, s.todos),
		define(s, &mcp.Tool{Name: "prep", Annotations: readOnly("Prepare for a meeting"),
			Description: "Material for an upcoming meeting: the subjects shared with the people in it (status and what's new since you last met), what is open between you, and what they likely haven't heard. Without event or people, lists meetings to prepare for."}, s.prep),
	}
}

// --- search ---

type SearchIn struct {
	Query string   `json:"query" jsonschema:"what to look for"`
	Since string   `json:"since,omitempty" jsonschema:"only from this date on (2006-01-02)"`
	Until string   `json:"until,omitempty" jsonschema:"only before this date (2006-01-02)"`
	Limit int      `json:"limit,omitempty" jsonschema:"how many passages (default 10, at most 25)"`
	For   []string `json:"for,omitempty"`
}

func (s *Server) search(ctx context.Context, in SearchIn) (*result, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" || len(q) > maxQuery {
		return nil, fmt.Errorf("query: give 1 to %d characters", maxQuery)
	}
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	since, err := day(in.Since, "since")
	if err != nil {
		return nil, err
	}
	until, err := day(in.Until, "until")
	if err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	sq := store.SearchQuery{Text: q, K: clamp(in.Limit, 10, maxSearch), Mode: store.ModeKeyword, Since: since, Until: until}
	if vec := s.e.Embed(ctx, q); vec != nil {
		sq.Mode, sq.Vec, sq.Model = store.ModeHybrid, vec, s.e.Embedder.Model()
	}
	hits, err := s.e.Store.Search(s.sc, sq)
	if err != nil {
		return nil, err
	}
	out := []Passage{}
	for _, h := range hits {
		out = append(out, l.passage(h, 0))
	}
	note := ""
	if sq.Mode == store.ModeKeyword {
		note = " (keyword search only: the embedding model isn't answering)"
	}
	return &result{body: map[string]any{"passages": out}, summary: fmt.Sprintf("%d passages%s.", len(out), note), labels: l}, nil
}

// --- gather ---

type GatherIn struct {
	Question string   `json:"question" jsonschema:"the question, in the owner's words"`
	For      []string `json:"for,omitempty"`
}

type ThreadRef struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	AsOf        time.Time `json:"asOf"`
	Now         string    `json:"now,omitempty"`
	Sensitivity *Sens     `json:"statusSensitivity,omitempty"`
}

func (s *Server) gather(ctx context.Context, in GatherIn) (*result, error) {
	q := strings.TrimSpace(in.Question)
	if q == "" || len(q) > maxQuery {
		return nil, fmt.Errorf("question: give 1 to %d characters", maxQuery)
	}
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	g, err := ask.Gather(ctx, s.e, s.sc, q, 8, nil)
	if err != nil {
		return nil, err
	}
	labels, err := s.statusLabels()
	if err != nil {
		return nil, err
	}
	threads := []ThreadRef{}
	for _, t := range g.Answer.Threads {
		r := ThreadRef{ID: t.ID, Name: t.Name, AsOf: t.AsOf, Now: t.Now}
		if lab, ok := labels[t.ID]; ok && t.Status != nil {
			b := l.mixed(lab)
			r.Sensitivity = &b
		}
		threads = append(threads, r)
	}
	sources := []Passage{}
	for i, h := range g.Answer.Sources {
		sources = append(sources, l.passage(h, i+1))
	}
	body := map[string]any{"context": g.Context, "threads": threads, "sources": sources}
	return &result{body: body, summary: fmt.Sprintf("%d threads, %d numbered sources; the context cites sources as [n].", len(threads), len(sources)), labels: l}, nil
}

// --- threads ---

type ThreadsIn struct {
	Query string   `json:"query,omitempty" jsonschema:"what the threads are about; empty lists the most recently active"`
	Limit int      `json:"limit,omitempty" jsonschema:"how many (default 10, at most 50)"`
	For   []string `json:"for,omitempty"`
}

type ThreadSummary struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Last        time.Time `json:"last"`
	Now         string    `json:"now,omitempty"`
	Sensitivity Sens      `json:"sensitivity"`
}

func (s *Server) threads(ctx context.Context, in ThreadsIn) (*result, error) {
	q := strings.TrimSpace(in.Query)
	if len(q) > maxQuery {
		return nil, fmt.Errorf("query: at most %d characters", maxQuery)
	}
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	n := clamp(in.Limit, 10, maxThreads)
	all, err := s.views().Topics()
	if err != nil {
		return nil, err
	}
	byID := map[int64]knowledge.TopicSummary{}
	for _, t := range all {
		byID[t.ID] = t
	}
	var picked []knowledge.TopicSummary
	switch {
	case q == "":
		picked = append(picked, all...)
		sort.Slice(picked, func(i, j int) bool { return picked[i].Last.After(picked[j].Last) })
	default:
		f, err := s.e.ThreadFinder()
		if err != nil {
			return nil, err
		}
		if vec := s.e.Embed(ctx, q); f != nil && vec != nil {
			for _, m := range f.Find(s.sc, vec, q, n) {
				if t, ok := byID[m.ID]; ok {
					picked = append(picked, t)
				}
			}
		} else {
			// Without the embedding model: names and keywords that contain
			// the words.
			lq := strings.ToLower(q)
			for _, t := range all {
				if strings.Contains(strings.ToLower(t.Label+" "+strings.Join(t.Keywords, " ")), lq) {
					picked = append(picked, t)
				}
			}
		}
	}
	picked = picked[:min(len(picked), n)]
	statuses, err := s.statusLabels()
	if err != nil {
		return nil, err
	}
	names, err := s.e.Store.ThreadLabels()
	if err != nil {
		return nil, err
	}
	out := []ThreadSummary{}
	for _, t := range picked {
		lab, ok := statuses[t.ID]
		if !ok {
			// No status: the name is as sensitive as what it was written
			// from. Its categories aren't recorded, so a categorised thread
			// shows as its level only; an unknown one fails closed.
			lab = sensitivity.Label{Level: sensitivity.Confidential}
			if n, ok := names[t.ID]; ok {
				lab.Level = n.LabelLevel
			}
		}
		out = append(out, ThreadSummary{ID: t.ID, Name: t.Label, Last: t.Last, Now: t.Now, Sensitivity: l.mixed(lab)})
	}
	return &result{body: map[string]any{"threads": out}, summary: fmt.Sprintf("%d threads.", len(out)), labels: l}, nil
}

// --- thread ---

type ThreadIn struct {
	ID      int64    `json:"id" jsonschema:"the thread's id"`
	Retired bool     `json:"retired,omitempty" jsonschema:"also list retired items (replaced, done or answered later)"`
	Limit   int      `json:"limit,omitempty" jsonschema:"items per list (default 30, at most 100)"`
	For     []string `json:"for,omitempty"`
}

type Related struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (s *Server) thread(_ context.Context, in ThreadIn) (*result, error) {
	if in.ID <= 0 {
		return nil, fmt.Errorf("id: give a thread id")
	}
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	tv, err := s.views().Topic(in.ID)
	if err != nil {
		return nil, err
	}
	if tv == nil {
		return nil, fmt.Errorf("no thread %d", in.ID)
	}
	n := clamp(in.Limit, 30, maxItems)
	body := map[string]any{"id": tv.ID, "name": tv.Label}
	if d := tv.Thread; d != nil {
		if d.Status != nil {
			statuses, err := s.statusLabels()
			if err != nil {
				return nil, err
			}
			body["status"], body["asOf"] = d.Status, d.AsOf
			if lab, ok := statuses[tv.ID]; ok {
				body["statusSensitivity"] = l.mixed(lab)
			}
		}
		body["current"] = l.statements(d.Current, n)
		if in.Retired {
			body["retired"] = l.statements(d.Retired, n)
		}
	}
	var rel []Related
	for _, r := range tv.Related[:min(len(tv.Related), 10)] {
		rel = append(rel, Related{ID: r.ID, Name: r.Label, Kind: r.Kind})
	}
	body["related"] = rel
	people := []string{}
	for _, p := range tv.People[:min(len(tv.People), 10)] {
		people = append(people, p.Name)
	}
	body["people"] = people
	return &result{body: body, summary: fmt.Sprintf("Thread %d.", tv.ID), labels: l}, nil
}

// --- meetings ---

type MeetingsIn struct {
	Limit int      `json:"limit,omitempty" jsonschema:"how many (default 20, at most 100)"`
	For   []string `json:"for,omitempty"`
}

type MeetingRef struct {
	ItemID      string    `json:"itemId"`
	Title       string    `json:"title"`
	Start       time.Time `json:"start"`
	Overview    string    `json:"overview"`
	Sections    int       `json:"sections"`
	ToDos       int       `json:"todos"`
	Sensitivity Sens      `json:"sensitivity"`
}

func (s *Server) meetings(_ context.Context, in MeetingsIn) (*result, error) {
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	ms, err := s.views().MeetingList(clamp(in.Limit, 20, maxMeetings))
	if err != nil {
		return nil, err
	}
	out := []MeetingRef{}
	for _, m := range ms {
		out = append(out, MeetingRef{ItemID: m.ItemID, Title: m.Title, Start: m.Start, Overview: m.Overview, Sections: m.Sections, ToDos: m.Todos,
			Sensitivity: l.item(m.Label, m.ItemID)})
	}
	return &result{body: map[string]any{"meetings": out}, summary: fmt.Sprintf("%d meetings.", len(out)), labels: l}, nil
}

// --- meeting ---

type MeetingIn struct {
	ItemID string   `json:"itemId" jsonschema:"the meeting's item id (from meetings or search)"`
	For    []string `json:"for,omitempty"`
}

type Section struct {
	Subject     string   `json:"subject"`
	Text        string   `json:"text"`
	People      []string `json:"people,omitempty"`
	Sensitivity Sens     `json:"sensitivity"`
}

type MeetingToDo struct {
	ID          int64    `json:"statementId,omitempty"`
	Text        string   `json:"text"`
	Owners      []string `json:"owners,omitempty"`
	Agreed      string   `json:"agreed"`
	Due         string   `json:"due,omitempty"`
	State       string   `json:"state"`
	Thread      string   `json:"thread,omitempty"`
	Sensitivity Sens     `json:"sensitivity"`
}

func (s *Server) meeting(_ context.Context, in MeetingIn) (*result, error) {
	if in.ItemID == "" || len(in.ItemID) > 200 {
		return nil, fmt.Errorf("itemId: give a meeting's item id")
	}
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	cfg := s.e.Cfg
	p, err := s.views().MeetingPage(in.ItemID, cfg.Knowledge.Digest.MinTopicStatements, cfg.Prep.QuietAfter(), cfg.Owner.Name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("no summary for %s (it may not be summarised yet; source reads its text)", in.ItemID)
	}
	il, _, err := s.e.Store.ItemLabel(p.ItemID)
	if err != nil {
		return nil, err
	}
	secs := []Section{}
	for _, x := range p.Sections {
		secs = append(secs, Section{Subject: x.Subject, Text: x.Text, People: x.People, Sensitivity: l.item(sensitivity.Combine(il, x.Own), p.ItemID)})
	}
	todos := []MeetingToDo{}
	for _, t := range p.ToDos {
		due := t.DueDate
		if due == "" {
			due = t.DueText
		}
		if t.Due != "" {
			due = t.Due
		}
		todos = append(todos, MeetingToDo{ID: t.StatementID, Text: t.Text, Owners: t.Owners, Agreed: t.Agreed, Due: due, State: t.State, Thread: t.TopicLabel,
			Sensitivity: l.item(sensitivity.Combine(il, t.Own), p.ItemID)})
	}
	var threads []Related
	for _, t := range p.Topics {
		threads = append(threads, Related{ID: t.ID, Name: t.Label, Kind: "discussed"})
	}
	body := map[string]any{"itemId": p.ItemID, "title": p.Title, "start": p.Start, "overview": p.Overview,
		"overviewSensitivity": l.item(sensitivity.Combine(il, p.OverviewOwn), p.ItemID), "sections": secs, "todos": todos, "threads": threads}
	return &result{body: body, summary: fmt.Sprintf("%d sections, %d to-dos.", len(secs), len(todos)), labels: l}, nil
}

// --- source ---

type SourceIn struct {
	ItemID  string   `json:"itemId" jsonschema:"the item id (from search, gather or meetings)"`
	FromSeq int      `json:"from_seq,omitempty" jsonschema:"start at this passage number (for the next page)"`
	For     []string `json:"for,omitempty"`
}

type Chunk struct {
	Seq         int     `json:"seq"`
	Start       float64 `json:"start,omitempty"`
	End         float64 `json:"end,omitempty"`
	Speakers    string  `json:"speakers,omitempty"`
	Text        string  `json:"text"`
	Sensitivity Sens    `json:"sensitivity"`
}

func (s *Server) source(_ context.Context, in SourceIn) (*result, error) {
	if in.ItemID == "" || len(in.ItemID) > 200 {
		return nil, fmt.Errorf("itemId: give an item id")
	}
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	iv, err := s.e.Store.Item(s.sc, in.ItemID)
	if err != nil {
		return nil, err
	}
	if iv == nil || len(iv.Chunks) == 0 {
		return nil, fmt.Errorf("no item %s", in.ItemID)
	}
	_, party, err := s.e.Store.ItemLabel(iv.ID)
	if err != nil {
		return nil, err
	}
	chunks := []Chunk{}
	size, next := 0, 0
	for _, h := range iv.Chunks {
		if h.Seq < in.FromSeq {
			continue
		}
		if size+len(h.Text) > sourceChars && len(chunks) > 0 {
			next = h.Seq
			break
		}
		size += len(h.Text)
		chunks = append(chunks, Chunk{Seq: h.Seq, Start: h.Start, End: h.End, Speakers: h.Speakers, Text: h.Text, Sensitivity: l.item(h.Label, iv.ID)})
	}
	body := map[string]any{"itemId": iv.ID, "title": iv.Title, "kind": iv.Kind, "when": iv.OccurredAt,
		"sensitivity": l.item(iv.Label, iv.ID), "party": party, "chunks": chunks}
	sum := fmt.Sprintf("%d passages.", len(chunks))
	if next > 0 {
		body["next_seq"] = next
		sum = fmt.Sprintf("%d passages; more from seq %d.", len(chunks), next)
	}
	return &result{body: body, summary: sum, labels: l}, nil
}

// --- todos ---

type ToDosIn struct {
	Mine      bool     `json:"mine,omitempty" jsonschema:"only the owner's own"`
	Closed    bool     `json:"closed,omitempty" jsonschema:"also done, changed and not-a-to-do ones"`
	Proposals bool     `json:"proposals,omitempty" jsonschema:"also things floated but not agreed"`
	Limit     int      `json:"limit,omitempty" jsonschema:"how many (default 50, at most 200)"`
	For       []string `json:"for,omitempty"`
}

type ToDo struct {
	Item
	Owners  []string  `json:"owners,omitempty"`
	Mine    bool      `json:"mine,omitempty"`
	State   string    `json:"state"`
	Due     string    `json:"due,omitempty"`
	Overdue bool      `json:"overdue,omitempty"`
	Thread  string    `json:"thread,omitempty"`
	Raised  int       `json:"raised"`
	Last    time.Time `json:"lastRaised"`
	Marks   int       `json:"marks,omitempty"`
}

func (s *Server) todos(_ context.Context, in ToDosIn) (*result, error) {
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	cfg := s.e.Cfg
	req := knowledge.ToDoRequest{Proposals: in.Proposals, QuietAfter: cfg.Prep.QuietAfter()}
	req.Source = cfg.Knowledge.Digest.ToDoSource()
	ts, err := s.views().ToDos(req, cfg.Owner.Name)
	if err != nil {
		return nil, err
	}
	n := clamp(in.Limit, 50, maxToDos)
	out := []ToDo{}
	for _, t := range ts {
		closed := t.State == "done" || t.State == "changed" || t.State == "not"
		if (closed && !in.Closed) || (in.Mine && !t.Mine) {
			continue
		}
		if len(out) == n {
			break
		}
		out = append(out, ToDo{Item: l.statement(t.Statement), Owners: t.Owners, Mine: t.Mine, State: t.State, Due: t.Due, Overdue: t.Overdue,
			Thread: t.TopicLabel, Raised: t.Raised, Last: t.LastRaised, Marks: len(t.Marks)})
	}
	return &result{body: map[string]any{"todos": out}, summary: fmt.Sprintf("%d to-dos (a negative id isn't matched to a statement yet, so it can't be marked).", len(out)), labels: l}, nil
}

// --- prep ---

type PrepIn struct {
	Event  string   `json:"event,omitempty" jsonschema:"item id of an upcoming event or a past meeting to prepare the next one of"`
	People []string `json:"people,omitempty" jsonschema:"people to prepare to meet (names or email addresses), when there is no event"`
	Since  string   `json:"since,omitempty" jsonschema:"with people: what's new since this date (2006-01-02)"`
	For    []string `json:"for,omitempty"`
}

type PrepSubject struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Why               string    `json:"why"`
	Private           bool      `json:"private,omitempty"`
	AsOf              time.Time `json:"asOf"`
	Status            any       `json:"status,omitempty"`
	StatusSensitivity *Sens     `json:"statusSensitivity,omitempty"`
	NewSince          []Item    `json:"newSince"`
}

type PrepOpen struct {
	Text    string    `json:"text"`
	Who     string    `json:"who,omitempty"`
	Due     string    `json:"due,omitempty"`
	Thread  string    `json:"thread"`
	Raised  time.Time `json:"raised"`
	Quiet   bool      `json:"quiet,omitempty"`
	Overdue bool      `json:"overdue,omitempty"`
	Private bool      `json:"private,omitempty"`
	Items   []Item    `json:"items"`
}

func (s *Server) prep(ctx context.Context, in PrepIn) (*result, error) {
	if err := checkFor(in.For); err != nil {
		return nil, err
	}
	if len(in.People) > maxRecipients || len(in.Event) > 200 {
		return nil, fmt.Errorf("too many people, or an event id that is too long")
	}
	l, err := s.labeller(in.For)
	if err != nil {
		return nil, err
	}
	if in.Event == "" && len(in.People) == 0 {
		ts, err := s.e.PrepTargets()
		if err != nil {
			return nil, err
		}
		ts = ts[:min(len(ts), 40)]
		type target struct {
			ItemID      string    `json:"itemId"`
			Title       string    `json:"title"`
			At          time.Time `json:"at"`
			Upcoming    bool      `json:"upcoming"`
			People      int       `json:"people"`
			Sensitivity Sens      `json:"sensitivity"`
		}
		out := []target{}
		for _, t := range ts {
			if t.Past {
				continue // happened: nothing to prepare
			}
			il, _, err := s.e.Store.ItemLabel(t.ItemID)
			if err != nil {
				// Unlabelled fails closed, as everywhere else.
				il = sensitivity.Label{Level: sensitivity.Confidential}
			}
			if !sensitivity.Visible(il, nil, s.sc) {
				continue
			}
			out = append(out, target{t.ItemID, t.Title, t.At, t.Upcoming, t.People, l.item(il, t.ItemID)})
		}
		return &result{body: map[string]any{"targets": out}, summary: fmt.Sprintf("%d meetings to prepare for: pass one's itemId as event.", len(out)), labels: l}, nil
	}
	since, err := day(in.Since, "since")
	if err != nil {
		return nil, err
	}
	v, err := s.e.Prep(ctx, knowledge.PrepRequest{Template: in.Event, People: in.People, Since: since}, s.sc)
	if err != nil {
		return nil, err
	}
	statuses, err := s.statusLabels()
	if err != nil {
		return nil, err
	}
	subjects := []PrepSubject{}
	for _, x := range v.Subjects {
		ps := PrepSubject{ID: x.ID, Name: x.Name, Why: x.Why, Private: x.Private, AsOf: x.AsOf, NewSince: l.statements(x.NewSince, 12)}
		if x.Status != nil {
			ps.Status = x.Status
			if lab, ok := statuses[x.ID]; ok {
				b := l.mixed(lab)
				ps.StatusSensitivity = &b
			}
		}
		subjects = append(subjects, ps)
	}
	open := []PrepOpen{}
	for _, o := range v.Open {
		open = append(open, PrepOpen{Text: o.Text, Who: o.Who, Due: o.Due, Thread: o.Name, Raised: o.Raised, Quiet: o.Quiet, Overdue: o.Overdue,
			Private: o.Private, Items: l.statements(o.Items, 3)})
	}
	type share struct {
		Thread string `json:"thread"`
		Item   Item   `json:"item"`
	}
	shares := []share{}
	for _, x := range v.Share {
		shares = append(shares, share{x.Name, l.statement(x.Item.Statement)})
	}
	body := map[string]any{"title": v.Title, "people": v.People, "since": v.Since, "met": v.Met, "subjects": subjects, "open": open, "theyMayNotHaveHeard": shares}
	return &result{body: body, summary: fmt.Sprintf("%d subjects, %d open, %d they may not have heard.", len(subjects), len(open), len(shares)), labels: l}, nil
}
