package gui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// exportFixture is a recorded meeting of three lines in two passages,
// the second confidential, and an email no scope may see (unlabelled).
func exportFixture(t *testing.T) *App {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.DefaultConfig()
	cfg.Owner = config.Owner{Name: "Sam Owner", Email: "sam@corp.example", InternalDomains: []string{"corp.example"}}
	at := time.Date(2026, 3, 4, 14, 5, 0, 0, time.Local)
	m := item.Item{ID: "tomoe:s1", Source: "tomoe", Ref: "s1", Kind: item.KindMeeting, Title: "Juniper Weekly", OccurredAt: at,
		Segments: []item.Segment{
			{Speaker: "Person 2 (Robin Vale)", Start: 5, Text: "Shall we start\nwith the roadmap?"},
			{Speaker: "You", Start: 65.4, Text: "Yes, the order first."},
			{Speaker: "Kim New", Start: 3725, Text: "The offer for the new hire is ninety."},
		}}
	if _, err := st.UpsertItem(m); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceChunks(m.ID, "v", "h", []store.Chunk{
		{Seq: 0, SegFrom: 0, SegTo: 2, Start: 5, Text: "x"},
		{Seq: 1, SegFrom: 2, SegTo: 3, Start: 3725, Text: "y"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceLabels(m.ID, "v", "h", sensitivity.Label{Level: sensitivity.Internal},
		map[int]sensitivity.Label{1: {Level: sensitivity.Confidential}}, []string{"sam@corp.example"}); err != nil {
		t.Fatal(err)
	}
	hidden := item.Item{ID: "m365-mail:h1", Source: "m365-mail", Ref: "h1", Kind: item.KindEmail, Title: "x", OccurredAt: at, Segments: []item.Segment{{Text: "probe"}}}
	if _, err := st.UpsertItem(hidden); err != nil {
		t.Fatal(err)
	}
	return &App{eng: &engine.Engine{Cfg: cfg, Store: st}}
}

func TestTranscriptOwnerScope(t *testing.T) {
	a := exportFixture(t)
	tr, err := a.transcript("tomoe:s1", "owner")
	if err != nil {
		t.Fatal(err)
	}
	got := transcriptText(tr, "")
	want := "Juniper Weekly · Wed 4 Mar 2026, 14:05\n\n" +
		"[00:05] Robin Vale: Shall we start with the roadmap?\n" +
		"[01:05] You: Yes, the order first.\n" +
		"[62:05] Kim New: The offer for the new hire is ninety.\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// Under shared-screen, the confidential passage stays out of the file.
func TestTranscriptSharedScreenLeavesOutConfidential(t *testing.T) {
	a := exportFixture(t)
	tr, err := a.transcript("tomoe:s1", "shared-screen")
	if err != nil {
		t.Fatal(err)
	}
	got := transcriptText(tr, "")
	if strings.Contains(got, "offer") || strings.Contains(got, "Kim New") {
		t.Fatalf("confidential passage exported under shared-screen:\n%s", got)
	}
	if !strings.Contains(got, "[01:05] You: Yes, the order first.") {
		t.Fatalf("internal passage missing:\n%s", got)
	}
}

func TestTranscriptNothingVisible(t *testing.T) {
	a := exportFixture(t)
	for _, id := range []string{"m365-mail:h1", "tomoe:missing"} {
		if _, err := a.transcript(id, "owner"); err == nil {
			t.Fatalf("%s: want an error, nothing is visible", id)
		}
	}
}

func TestTranscriptTextMarksOtherCopy(t *testing.T) {
	got := transcriptText(&store.Transcript{Title: "", Lines: []item.Segment{
		{Speaker: "Robin Vale", Start: -3, Text: "Before you joined.", From: "teams"},
		{Speaker: "", Start: 10, Text: "  "},
		{Start: 12, Text: "No speaker."},
	}}, "")
	want := "Meeting\n\n[00:00] Robin Vale: [teams transcript, not heard by you] Before you joined.\n[00:12] No speaker.\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTranscriptTextNamesOwner(t *testing.T) {
	tr := &store.Transcript{Title: "Sync", Lines: []item.Segment{
		{Speaker: "You", Start: 1, Text: "Shall we start?"},
		{Speaker: "Person 2 (Ana Example)", Start: 3, Text: "Yes."},
		{Speaker: "Youssef Example", Start: 5, Text: "Me too."},
	}}
	got := transcriptText(tr, " Robin   Vale ")
	want := "Sync\n\n[00:01] Robin Vale: Shall we start?\n[00:03] Ana Example: Yes.\n[00:05] Youssef Example: Me too.\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// No owner name set: "You", as before.
	if got := transcriptText(tr, ""); !strings.Contains(got, "[00:01] You: Shall we start?") {
		t.Fatalf("no owner: got %q", got)
	}
}

func TestTranscriptFileName(t *testing.T) {
	at := time.Date(2026, 3, 4, 14, 5, 0, 0, time.UTC)
	for _, c := range []struct{ title, want string }{
		{"Juniper Weekly", "Juniper Weekly 2026-03-04.txt"},
		{"../../etc/passwd", "etc passwd 2026-03-04.txt"},
		{"Q1: plan / review\x00\n", "Q1 plan review 2026-03-04.txt"},
		{"...", "Meeting 2026-03-04.txt"},
		{"Café 1:1 (Kai & Ana)", "Café 1 1 (Kai & Ana) 2026-03-04.txt"},
		{strings.Repeat("a", 200), strings.Repeat("a", 80) + " 2026-03-04.txt"},
	} {
		if got := transcriptFileName(c.title, at); got != c.want {
			t.Errorf("%q: got %q want %q", c.title, got, c.want)
		}
	}
	if got := transcriptFileName("x", time.Time{}); got != "x.txt" {
		t.Errorf("no date: got %q", got)
	}
}

func TestItemsExist(t *testing.T) {
	a := exportFixture(t)
	got, err := a.ItemsExist([]string{"tomoe:s1", "tomoe:missing", "m365-mail:h1", "tomoe:s1", ""}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"tomoe:s1"}) {
		t.Fatalf("got %v", got)
	}
	if got, err := a.ItemsExist(nil, "owner"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	if _, err := a.ItemsExist(make([]string, maxExistIDs+1), "owner"); err == nil {
		t.Fatal("want an error over the limit")
	}
}
