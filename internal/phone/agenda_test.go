package phone

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// fakeAgendaStore holds synthetic events, labels and briefs.
type fakeAgendaStore struct {
	items  []item.Item
	labels map[string]sensitivity.Label
	briefs map[string]store.PrepBrief
	chunks map[int64]store.Hit
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

func (f fakeAgendaStore) ChunkHit(sc sensitivity.Scope, id int64) (*store.Hit, error) {
	h, ok := f.chunks[id]
	if !ok || !sensitivity.Visible(h.Label, nil, sc) {
		return nil, nil
	}
	return &h, nil
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
	evs, err := BuildAgenda(f, sensitivity.Owner(), now, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(evs); got != "calendar:now calendar:private calendar:unlabelled calendar:later calendar:far" {
		t.Errorf("owner, 30 days (capped at 14): %s", got)
	}
	if got := ids(must(BuildAgenda(f, sensitivity.Owner(), now, 10, nil))); got != "calendar:now calendar:private calendar:unlabelled calendar:later" {
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
	narrow := must(BuildAgenda(f, sensitivity.UpTo(sensitivity.ShareInternal), now, 14, nil))
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

func TestAgendaBriefSourcesAndOpen(t *testing.T) {
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	internal := sensitivity.Label{Level: sensitivity.Internal}
	conf := sensitivity.Label{Level: sensitivity.Confidential}
	hit := func(id int64, title string, l sensitivity.Label) store.Hit {
		return store.Hit{ChunkID: id, ItemID: "tomoe:m" + strconv.FormatInt(id, 10), Kind: "meeting", Title: title, OccurredAt: now.Add(-48 * time.Hour),
			Speakers: "Robin Vale, Kai Moreno", Text: "Passage " + title, Label: l}
	}
	// Seven sources; the text cites 2, 5 (twice), 7 (restricted for the
	// narrow scope) and 9 (no such source).
	var srcs []store.Hit
	for i := 1; i <= 7; i++ {
		l := internal
		if i == 7 {
			l = conf
		}
		srcs = append(srcs, hit(int64(100+i), "Source "+strconv.Itoa(i), l))
	}
	ev := item.Item{ID: "calendar:sync", Kind: item.KindEvent, Title: "Weekly sync", OccurredAt: now.Add(2 * time.Hour), EndedAt: now.Add(3 * time.Hour),
		Participants: []item.Participant{{Name: "Robin Vale", Role: "organizer"}, {Name: "Sam Owner", Owner: true}}}
	solo := item.Item{ID: "calendar:focus", Kind: item.KindEvent, Title: "Focus time", OccurredAt: now.Add(4 * time.Hour), EndedAt: now.Add(5 * time.Hour),
		Participants: []item.Participant{{Name: "Sam Owner", Owner: true}}}
	f := fakeAgendaStore{
		items:  []item.Item{ev, solo},
		labels: map[string]sensitivity.Label{ev.ID: internal, solo.ID: internal},
		briefs: map[string]store.PrepBrief{ev.ID: {ItemID: ev.ID, Text: "**Pilot**\n- Ask about the date [5] [2]\n  - Budget [5][7] and [9]", Sources: srcs, Label: internal, MadeAt: now}},
		chunks: map[int64]store.Hit{
			1: hit(1, "Long one", internal), 2: hit(2, "Second", internal), 3: hit(3, "Third", conf), 4: hit(4, "Fourth", internal), 5: hit(5, "Fifth", internal),
		},
	}
	long := f.chunks[1]
	long.Text = strings.Repeat("a", 2000) + " the quoted words " + strings.Repeat("b", 2000)
	f.chunks[1] = long
	ti := func(id, chunk int64, quote string) store.ThreadItem {
		return store.ThreadItem{Statement: store.Statement{ID: id, ChunkID: chunk, Quote: quote, Label: internal}}
	}
	var asked []string
	open := func(id string) ([]knowledge.PrepOpen, error) {
		asked = append(asked, id)
		ops := []knowledge.PrepOpen{
			// Cited 12 and 14 lead, then the other statements, one per chunk,
			// at most three; (2026) names no statement and stays.
			{Text: "Send the plan (2026) [14, 12]", Who: "Robin Vale", Due: "2026-11-20", Name: "Rollout", Raised: now.Add(-time.Hour), Overdue: true,
				Items: []store.ThreadItem{ti(10, 4, ""), ti(11, 5, ""), ti(12, 1, "the quoted words"), ti(13, 1, ""), ti(14, 2, ""), ti(15, 3, "")}},
			{Text: "Old question (20-22)", Quiet: true, Private: true, Name: "Hiring", Items: []store.ThreadItem{ti(21, 3, "")}},
		}
		for i := 0; i < 20; i++ {
			ops = append(ops, knowledge.PrepOpen{Text: "More " + strconv.Itoa(i)})
		}
		return ops, nil
	}
	evs := must(BuildAgenda(f, sensitivity.Owner(), now, 10, open))
	if len(evs) != 2 || strings.Join(asked, " ") != "calendar:sync" {
		t.Fatalf("events %d, prep asked for %v (only events with others)", len(evs), asked)
	}
	b := evs[0].Brief
	var ns []int
	for _, s := range b.Sources {
		ns = append(ns, s.N)
	}
	if fmt.Sprint(ns) != "[2 5 7]" {
		t.Errorf("brief sources numbered %v, want the cited ones as the desktop numbers them", ns)
	}
	if s := b.Sources[0]; s.Title != "Source 2" || s.Kind != "meeting" || s.Speakers == "" || s.Text != "Passage Source 2" || s.Sensitivity.Level != "internal" {
		t.Errorf("source %+v", s)
	}
	if b.Sources[2].Sensitivity.Level != "confidential" {
		t.Errorf("each source carries its own label: %+v", b.Sources[2].Sensitivity)
	}
	o := evs[0].Open
	if len(o) != maxOpen {
		t.Fatalf("open items %d, want the cap %d", len(o), maxOpen)
	}
	if o[0].Text != "Send the plan (2026)" || o[0].Thread != "Rollout" || o[0].Who != "Robin Vale" || !o[0].Overdue || o[0].Raised == nil {
		t.Errorf("first open item %+v", o[0])
	}
	var titles []string
	for _, s := range o[0].Sources {
		titles = append(titles, fmt.Sprintf("%d:%s", s.N, s.Title))
	}
	if strings.Join(titles, " ") != "1:Second 2:Long one 3:Fourth" {
		t.Errorf("open sources %v: cited first, one per chunk, at most 3", titles)
	}
	lt := o[0].Sources[1].Text
	if n := len([]rune(lt)); n != maxPassage || !strings.Contains(lt, "the quoted words") || !strings.HasPrefix(lt, "…") || !strings.HasSuffix(lt, "…") {
		t.Errorf("long passage: %d runes, around the quote: %q…", n, lt[:20])
	}
	if o[1].Text != "Old question" || !o[1].Quiet || !o[1].Private || len(o[1].Sources) != 1 || o[1].Sources[0].Sensitivity.Level != "confidential" {
		t.Errorf("second open item %+v", o[1])
	}
	if o[2].Raised != nil || o[2].Sources == nil {
		t.Errorf("no raised date is null, no sources an empty list: %+v", o[2])
	}
	if evs[1].Open == nil || len(evs[1].Open) != 0 || evs[1].Brief != nil {
		t.Errorf("event without others: %+v", evs[1])
	}
	// A narrower scope drops the restricted sources but keeps the numbers.
	narrow := must(BuildAgenda(f, sensitivity.UpTo(sensitivity.ShareInternal), now, 10, open))
	ns = nil
	for _, s := range narrow[0].Brief.Sources {
		ns = append(ns, s.N)
	}
	if fmt.Sprint(ns) != "[2 5]" {
		t.Errorf("internal scope brief sources %v", ns)
	}
	if len(narrow[0].Open[1].Sources) != 0 {
		t.Errorf("internal scope open sources %+v", narrow[0].Open[1].Sources)
	}
	// The JSON shape is the contract's.
	j, _ := json.Marshal(evs[0])
	for _, k := range []string{`"sources":[{"n":2,"title":`, `"kind":`, `"occurred_at":`, `"speakers":`, `"text":`, `"sensitivity":{"level":`,
		`"open":[{"text":"Send the plan (2026)","who":`, `"due":`, `"thread":"Rollout"`, `"raised":`, `"quiet":false`, `"overdue":true`, `"private":false`} {
		if !strings.Contains(string(j), k) {
			t.Errorf("event JSON lacks %s", k)
		}
	}
}

func TestBriefSourcesCap(t *testing.T) {
	var srcs []store.Hit
	var text strings.Builder
	for i := 1; i <= 40; i++ {
		srcs = append(srcs, store.Hit{Title: "S" + strconv.Itoa(i), Label: sensitivity.Label{Level: sensitivity.Internal}})
		fmt.Fprintf(&text, "point [%d] ", 41-i)
	}
	got := briefSources(&store.PrepBrief{Text: text.String(), Sources: srcs}, sensitivity.Owner())
	if len(got) != maxBriefSources || got[0].N != 11 || got[len(got)-1].N != 40 {
		t.Errorf("cap keeps the first %d cited, in number order: %d, %d..%d", maxBriefSources, len(got), got[0].N, got[len(got)-1].N)
	}
}

func TestPassageAndCites(t *testing.T) {
	for _, n := range []int{1200, 1201, 5000} {
		s := strings.Repeat("é", n)
		if got := len([]rune(passage(s, "", maxPassage))); got > maxPassage {
			t.Errorf("%d: %d runes", n, got)
		}
	}
	// Quote near the end: the window ends at the text's end.
	s := strings.Repeat("x", 3000) + "END"
	if p := passage(s, "END", maxPassage); len([]rune(p)) != maxPassage || !strings.HasSuffix(p, "END") || !strings.HasPrefix(p, "…") {
		t.Errorf("tail window %q", p[len(p)-5:])
	}
	by := map[int64]store.ThreadItem{5: {}, 21: {}}
	txt, ids := stripItemCites("A (5). B [3]. C (20–24) and (99, 5)", by)
	if txt != "A. B [3]. C and" || fmt.Sprint(ids) != "[5 21]" {
		t.Errorf("stripped %q %v", txt, ids)
	}
}
