package knowledge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

func testMeeting() *Meeting {
	chunks := []store.Chunk{
		{ID: 10, Seq: 0, Start: 0, Text: "Raj Patel: There's nobody in the TAM group we should keep.\nYou: Is Chris aligned on that?"},
		{ID: 11, Seq: 1, Start: 60, Text: "Raj Patel: He is. We can't notify anyone before December.\nRaj Patel: The Platinum team found about two hundred and fifty thousand in ARR."},
		{ID: 12, Seq: 2, Start: 120, Text: "Grace Holm: I'm out Friday, my daughter's dance competition.\nRaj Patel: Have fun."},
	}
	known := KnownFacts("Sam Owner", "sam@larkspur.example", []string{"larkspur.example"},
		[]Invitee{{Email: "sam@larkspur.example"}, {Email: "raj.patel@larkspur.example"}, {Email: "dana@meridian.example"}})
	return NewMeeting("Headcount review", time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC), known, "Sam Owner", chunks)
}

func TestKnownFacts(t *testing.T) {
	m := testMeeting()
	want := "The user is Sam Owner of Larkspur (shown as \"You\").\nInvited: Raj Patel, Larkspur.\nInvited: someone with address dana@meridian.example, Meridian (external)."
	if m.Known != want {
		t.Errorf("known facts:\n%s\nwant\n%s", m.Known, want)
	}
}

func TestMeetingTranscript(t *testing.T) {
	m := testMeeting()
	got := m.transcript(0, 2, 1, 2)
	want := "1 [context] Raj Patel: There's nobody in the TAM group we should keep.\n2 [context] Sam Owner (You): Is Chris aligned on that?\n3 Raj Patel: He is. We can't notify anyone before December.\n4 Raj Patel: The Platinum team found about two hundred and fifty thousand in ARR."
	if got != want {
		t.Errorf("transcript:\n%s\nwant\n%s", got, want)
	}
}

func TestCheckPoints(t *testing.T) {
	m := testMeeting()
	ps := m.Check([]Point{
		{Text: "The Platinum team found about $250k in ARR.", Evidence: []PointEvidence{{Line: 4, Quote: "found about two hundred and fifty thousand in ARR"}}},
		// Quoted from the line before the one cited: models misnumber by one.
		{Text: "Raj Patel says no one can be notified before December.", Evidence: []PointEvidence{{Line: 4, Quote: "We can't notify anyone before December"}}},
		{Text: "All Technical Account Manager roles go.", Evidence: []PointEvidence{{Line: 1, Quote: "nobody in the TAM group we should keep"}}},
		{Text: "The Platinum team found $400k in ARR.", Evidence: []PointEvidence{{Line: 4, Quote: "found about two hundred and fifty thousand"}}},
		{Text: "Raj Patel will interview the TAM group.", Evidence: []PointEvidence{{Line: 1, Quote: "we will interview everyone next week"}}},
		{Text: "Raj Patel is aligned.", Evidence: nil},
	})
	want := []string{"", "", "name not in meeting: Technical", "figure not in meeting: 400", "quote not at line 1", "no evidence"}
	for i, p := range ps {
		got := strings.Join(p.Issues, "; ")
		if want[i] == "" && got != "" || want[i] != "" && !strings.Contains(got, want[i]) {
			t.Errorf("point %d %q: issues %q, want %q", i, p.Text, got, want[i])
		}
	}
}

func TestDedupe(t *testing.T) {
	ps := dedupe([]Point{
		{Text: "No one is notified before December because of peak season scaling.", Evidence: []PointEvidence{{Line: 3}}},
		{Text: "No one is notified before December, because of peak season scaling.", Evidence: []PointEvidence{{Line: 3}, {Line: 4}}},
		{Text: "The Platinum team found about $250k in ARR."},
	})
	if len(ps) != 2 || len(ps[0].Evidence) != 2 {
		t.Errorf("dedupe: %+v", ps)
	}
}

func TestPointKind(t *testing.T) {
	cases := [][3]string{
		{"decision", "decided", "decision"}, {"decision", "proposed", "opinion"}, {"decision", "rejected", "fact"},
		{"commitment", "agreed", "commitment"}, {"position", "stated", "opinion"},
		{"question", "open", "question"}, {"question", "answered", "fact"}, {"issue", "open", "question"}, {"fact", "stated", "fact"},
	}
	for _, c := range cases {
		if got := pointKind(c[0], c[1]); got != c[2] {
			t.Errorf("pointKind(%s, %s) = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestPointStatementLabels(t *testing.T) {
	m := testMeeting()
	labels := map[int]sensitivity.Label{
		-1: {Level: sensitivity.Confidential},
		1:  {Level: sensitivity.Confidential, Categories: sensitivity.Financial},
	}
	holder := func(n string) string { return "person:" + strings.ToLower(n) }
	// Evidence across passages 0 and 1: anchored at the first, labelled
	// with the most sensitive.
	st := m.PointStatement(Point{Kind: "decision", Status: "decided", Text: "No one is notified before December.", Who: []string{"Raj Patel"},
		Evidence: []PointEvidence{{Line: 1, Quote: "x"}, {Line: 3, Quote: "y"}}}, m.Date, holder, labels)
	if st.ChunkID != 10 || st.Line != 1 || st.Kind != "decision" || st.HolderID != "person:raj patel" || len(st.Evidence) != 2 || st.Evidence[1].ChunkID != 11 || st.Evidence[1].Line != 1 {
		t.Errorf("statement: %+v", st)
	}
	if st.Own.Categories&sensitivity.Financial == 0 || st.Own.Level != sensitivity.Confidential {
		t.Errorf("own label %+v, want the second passage's", st.Own)
	}
	// Personal: owner only, whatever the passage.
	st = m.PointStatement(Point{Kind: "fact", Status: "stated", Text: "Grace Holm is out Friday for her daughter's dance competition.", Who: []string{"Grace Holm"}, Personal: true,
		Evidence: []PointEvidence{{Line: 5, Quote: "I'm out Friday"}}}, m.Date, holder, labels)
	if st.Own.Categories&sensitivity.Personal == 0 || st.Own.Level != sensitivity.Restricted || st.SaidAt != m.Date.Add(2*time.Minute) {
		t.Errorf("personal point: %+v", st)
	}
	if sensitivity.Visible(st.Own, nil, sensitivity.SharedScreen()) || sensitivity.Visible(st.Own, nil, sensitivity.ReplyTo(nil, "grace@larkspur.example")) {
		t.Error("personal point visible outside the owner's scope")
	}
}

// fakeLLM answers the brief and each segment, recording what it was asked.
type fakeLLM struct {
	mu    sync.Mutex
	users []string
	opts  []provider.ChatOptions
}

func (f *fakeLLM) Local() bool       { return true }
func (f *fakeLLM) ModelName() string { return "fake" }
func (f *fakeLLM) Complete(_ context.Context, msgs []provider.Message, opt provider.ChatOptions, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := msgs[1].Content
	f.users, f.opts = append(f.users, u), append(f.opts, opt)
	switch {
	case strings.HasPrefix(msgs[0].Content, "You read a whole meeting"):
		return `{"purpose":"Headcount review","participants":[{"name":"Raj Patel","role":"support lead"}],"subjects":[],"glossary":[{"term":"TAM","means":"a role"}]}`, nil
	case strings.Contains(u, "\n1 Raj Patel"):
		return `<think>…</think>{"points":[{"kind":"decision","status":"decided","text":"Every TAM position is eliminated.","who":["Raj Patel"],"personal":false,"evidence":[{"line":1,"quote":"nobody in the TAM group we should keep"}]},
			{"kind":"fact","status":"stated","text":"No one is notified before December.","who":["Raj Patel"],"personal":false,"evidence":[{"line":3,"quote":"We can't notify anyone before December"}]}]}`, nil
	default:
		return `{"points":[{"kind":"fact","status":"stated","text":"No one is notified before December.","who":["Raj Patel"],"personal":false,"evidence":[{"line":3,"quote":"We can't notify anyone before December"},{"line":99,"quote":"out of range"}]},
			{"kind":"fact","status":"stated","text":"Grace Holm is out Friday for her daughter's dance competition.","who":["Grace Holm"],"personal":true,"evidence":[{"line":5,"quote":"I'm out Friday, my daughter's dance competition"}]}]}`, nil
	}
}

func TestPointerExtract(t *testing.T) {
	m := testMeeting()
	f := &fakeLLM{}
	ps, err := (&Pointer{LLM: f, Think: true, Segment: 2, Parallel: 2}).Extract(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.users) != 3 {
		t.Fatalf("calls: %d, want brief + 2 segments", len(f.users))
	}
	if f.opts[0].Think || !f.opts[1].Think || f.opts[1].TopK != 20 {
		t.Errorf("brief must not think, segments must, with sampling: %+v / %+v", f.opts[0], f.opts[1])
	}
	for _, u := range f.users[1:] {
		if !strings.Contains(u, "Purpose: Headcount review") || !strings.Contains(u, "Invited: Raj Patel, Larkspur.") {
			t.Errorf("segment prompt lacks the brief or known facts:\n%s", u)
		}
	}
	// The second segment (passage 2) sees passage 1 as context.
	var second string
	for _, u := range f.users[1:] {
		if !strings.Contains(u, "\n1 Raj Patel") {
			second = u
		}
	}
	if !strings.Contains(second, "3 [context] Raj Patel: He is.") || !strings.Contains(second, "5 Grace Holm") {
		t.Errorf("second segment's excerpt:\n%s", second)
	}
	var texts []string
	for _, p := range ps {
		texts = append(texts, p.Text)
		if len(p.Issues) > 0 {
			t.Errorf("%q flagged: %v", p.Text, p.Issues)
		}
	}
	// The December point came from both segments: kept once, with the
	// out-of-range evidence dropped.
	want := "Every TAM position is eliminated. | No one is notified before December. | Grace Holm is out Friday for her daughter's dance competition."
	if got := strings.Join(texts, " | "); got != want {
		t.Errorf("points: %s\nwant %s", got, want)
	}
}
