package knowledge

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// cardFixture: a programme ("Juniper") reviewed in one meeting, with a
// legal deadline raised in the middle of the review (no programme name in
// it), a later status update, and an unrelated meeting about "Atlas".
type cardFixture struct {
	st     *store.Store
	topics map[string]int64
	claims map[string]int64
}

func newCardFixture(t *testing.T) *cardFixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := &cardFixture{st: s, topics: map[string]int64{}, claims: map[string]int64{}}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 15, 0, 0, 0, time.UTC) }
	type stmt struct {
		key, topic, kind, status, text, who string
		min                                 int // minutes into the meeting
		label                               sensitivity.Label
	}
	hr := sensitivity.Label{Level: sensitivity.Restricted, Categories: sensitivity.HR}
	meetings := []struct {
		id    string
		at    time.Time
		stmts []stmt
	}{
		{"m:1", day(9, 16), []stmt{
			{"agenda", "juniper", "fact", "stated", "Next agenda item is Juniper, with Dana Ruiz dialing in.", "Lee Chen", 0, sensitivity.Label{}},
			{"target", "juniper", "metric", "stated", "Juniper target: 40 role changes in 2026, committed to the board.", "Dana Ruiz", 1, hr},
			{"ask", "juniper", "question", "open", "How many of the 40 Juniper changes are done so far?", "Lee Chen", 2, sensitivity.Label{}},
			{"count", "juniper", "commitment", "stated", "Dana Ruiz will get the exact Juniper count from finance.", "Dana Ruiz", 3, sensitivity.Label{}},
			{"deadline", "ontario", "rule", "stated", "Because of the Ontario notice change, finish marginal cases before January 1st.", "Dana Ruiz", 4, sensitivity.Label{}},
			{"milestones", "juniper", "commitment", "proposed", "Juniper milestones: names by October 1st, rationale two weeks after that, counsel briefed by November 1st.", "Dana Ruiz", 5, sensitivity.Label{}},
			{"mine", "juniper", "fact", "stated", "Sam Owner says the platform share of Juniper is 12, landing late in the year.", "Sam Owner", 6, sensitivity.Label{}},
			{"batch", "juniper", "rule", "stated", "Batch Juniper changes together so review happens once.", "Dana Ruiz", 7, sensitivity.Label{}},
			{"cheer", "juniper", "fact", "stated", "Lee Chen thinks Juniper is going to be great for the team.", "Lee Chen", 8, sensitivity.Label{}},
		}},
		{"m:2", day(9, 30), []stmt{
			{"names", "juniper", "metric", "stated", "Juniper: 25 names received; platform the only area still to send names.", "Lee Chen", 1, sensitivity.Label{}},
		}},
		{"m:3", day(9, 23), []stmt{
			{"atlas1", "atlas", "metric", "stated", "Atlas pipeline is up 14% this quarter.", "Lee Chen", 1, sensitivity.Label{}},
			{"atlas2", "atlas", "decision", "agreed", "Atlas pricing stays as it is until Q1.", "Lee Chen", 2, sensitivity.Label{}},
			{"atlas3", "atlas", "fact", "stated", "Atlas has three pilots live.", "Lee Chen", 3, sensitivity.Label{}},
		}},
	}
	vec := map[string][]float32{"juniper": {1, 0, 0}, "ontario": {0.8, 0.6, 0}, "atlas": {0, 0, 1}}
	byTopic := map[string][]int64{}
	for _, m := range meetings {
		it := item.Item{ID: m.id, Source: "t", Ref: m.id, Kind: item.KindMeeting, Title: "Weekly review", OccurredAt: m.at,
			Segments: []item.Segment{{Text: "x"}}}
		if _, err := s.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
		var chunks []store.Chunk
		for i := range m.stmts {
			chunks = append(chunks, store.Chunk{Seq: i, SegFrom: 0, SegTo: 1, Start: float64(60 * m.stmts[i].min), Text: m.stmts[i].text})
		}
		if err := s.ReplaceChunks(m.id, "v", "h", chunks); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceLabels(m.id, "v", "h", sensitivity.Label{Level: sensitivity.Confidential}, nil, []string{"sam.owner@corp.example", "lee.chen@corp.example"}); err != nil {
			t.Fatal(err)
		}
		cs, _ := s.Chunks(m.id)
		var sts []store.Statement
		for i, x := range m.stmts {
			sts = append(sts, store.Statement{ChunkID: cs[i].ID, Line: 1, Kind: x.kind, Status: x.status, Text: x.text, Holder: x.who,
				Who: []string{x.who}, SaidAt: m.at.Add(time.Duration(x.min) * time.Minute), Certainty: 0.9, Grounded: true, Own: x.label,
				Evidence: []store.Evidence{{ChunkID: cs[i].ID, Line: 1}}})
		}
		if err := s.ReplaceStatements(m.id, "v", "h", sts); err != nil {
			t.Fatal(err)
		}
		un, _ := s.UnlinkedStatements()
		for _, u := range un {
			for _, x := range m.stmts {
				if x.text == u.Text {
					c, err := s.NewClaim(u, vec[x.topic])
					if err != nil {
						t.Fatal(err)
					}
					f.claims[x.key] = c
					byTopic[x.topic] = append(byTopic[x.topic], c)
				}
			}
		}
	}
	// The promise to find out leaves the question open.
	all, _ := s.AllStatements()
	var countID int64
	for _, st := range all {
		if strings.HasPrefix(st.Text, "Dana Ruiz will get") {
			countID = st.ID
		}
	}
	if err := s.RelateClaims(f.claims["count"], f.claims["ask"], "pending", countID); err != nil {
		t.Fatal(err)
	}
	var rows []store.TopicRow
	n := int64(0)
	for _, name := range []string{"juniper", "ontario", "atlas"} {
		n++
		f.topics[name] = n
		rows = append(rows, store.TopicRow{ID: n, Label: strings.ToUpper(name[:1]) + name[1:], Claims: byTopic[name]})
	}
	if err := s.ReplaceTopics(rows, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCardIndexTermsAndCompanions(t *testing.T) {
	f := newCardFixture(t)
	x, err := BuildCardIndex(f.st)
	if err != nil {
		t.Fatal(err)
	}
	hs := x.TermsIn("so how is juniper going")
	if len(hs) != 1 || hs[0].Topic != f.topics["juniper"] {
		t.Fatalf("terms in line: %+v (topics %v)", hs, f.topics)
	}
	for _, w := range []string{"Dana", "Ruiz", "Lee", "Sam", "Owner", "Next"} {
		if len(x.TermsIn(w)) > 0 {
			t.Errorf("%q is a trigger term", w)
		}
	}
	if got := x.Host(f.topics["ontario"]); got != f.topics["juniper"] {
		t.Errorf("deadline raised during the review belongs on its card: host %d", got)
	}
	if got := x.Host(f.topics["atlas"]); got != f.topics["atlas"] {
		t.Errorf("unrelated topic hosted by %d", got)
	}
	if a, how, _ := x.Find("the Ontario notice", nil); a.Topic != f.topics["juniper"] {
		t.Errorf("exact phrase found %s (%s)", a.Key(), how)
	}
	a, _, _ := x.Find("notice period deadline", func(string) ([]float32, error) { return []float32{0.8, 0.6, 0}, nil })
	if a.Topic != f.topics["juniper"] {
		t.Errorf("paraphrase found %s", a.Key())
	}
}

func TestCard(t *testing.T) {
	f := newCardFixture(t)
	x, _ := BuildCardIndex(f.st)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	v := Views{Store: f.st, Scope: sensitivity.Owner(), Now: now}
	c, err := v.Card(x, Anchor{Topic: f.topics["juniper"]}, "person:sam.owner@corp.example", "Sam Owner")
	if err != nil || c == nil {
		t.Fatalf("card: %v %v", c, err)
	}
	lines := map[string]CardLine{}
	section := map[string]string{}
	for _, s := range c.Sections {
		for _, l := range s.Lines {
			for k, id := range f.claims {
				if id == l.ClaimID {
					lines[k], section[k] = l, s.Name
				}
			}
		}
	}
	want := map[string]string{"target": "Now", "names": "Now", "milestones": "Due", "count": "Due", "ask": "Open", "mine": "Yours", "batch": "Rules", "deadline": "Rules"}
	for k, sec := range want {
		if section[k] != sec {
			t.Errorf("%s in section %q, want %q", k, section[k], sec)
		}
	}
	if _, ok := lines["agenda"]; ok {
		t.Error("meeting logistics on the card")
	}
	if l := lines["milestones"]; l.Due != "due tomorrow" {
		t.Errorf("milestones due %q", l.Due)
	}
	if l := lines["ask"]; l.Note == "" {
		t.Error("open question doesn't say someone is finding out")
	}
	if !c.Collapsed {
		t.Error("card with an HR line not collapsed")
	}
	if c.Meetings != 2 {
		t.Errorf("meetings %d", c.Meetings)
	}
	// The rationale is due "two weeks after" October 1st: tomorrow, as of
	// the 14th. As of 20 September, the names come first.
	v.Now = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	early, _ := v.Card(x, Anchor{Topic: f.topics["juniper"]}, "", "")
	for _, s := range early.Sections {
		for _, l := range s.Lines {
			if l.ClaimID == f.claims["names"] {
				t.Error("a replay as of 20 Sep sees the 30 Sep update")
			}
			if l.ClaimID == f.claims["milestones"] && l.Due != "due in 11 days (Thu 1 Oct)" {
				t.Errorf("as of 20 Sep milestones %q", l.Due)
			}
		}
	}
	for _, sc := range []sensitivity.Scope{sensitivity.SharedScreen(), sensitivity.Cloud(), sensitivity.ReplyTo([]string{"corp.example"}, "pat@corp.example")} {
		v := Views{Store: f.st, Scope: sc, Now: now}
		if c, _ := v.Card(x, Anchor{Topic: f.topics["juniper"]}, "", ""); c != nil {
			t.Errorf("card visible under %s: %d lines", sc.Name, c.Lines())
		}
	}
}

// A bare "'s" (a transcript's stray possessive) once became an empty word
// and panicked, which stopped the app from opening the dataset.
func TestWordUseStrayPossessive(t *testing.T) {
	u := wordUse([]store.CardRow{{Text: "the team's 's plan ' and Jo said Ana 's"}})
	if u.total["plan"] != 1 || u.speaker["jo"] != 1 {
		t.Fatalf("usage = %+v", u)
	}
}
