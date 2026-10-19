package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// scriptedThreads plays a threading model: items naming Juniper or Ontario
// go to "Juniper programme", Atlas items to "Atlas pricing", and the first
// assign reply drops every other item, as real replies sometimes do. The
// reconcile reply says the promise to get the count is pending on the
// question, and keeps everything else current.
type scriptedThreads struct{ assigns, reconciles int }

func (s *scriptedThreads) Local() bool       { return true }
func (s *scriptedThreads) ModelName() string { return "scripted" }

var reItem = regexp.MustCompile(`\[(\d+)\] \([^)]*\) (.*)`)

func (s *scriptedThreads) Complete(_ context.Context, msgs []provider.Message, _ provider.ChatOptions, _ func(string)) (string, error) {
	user := msgs[len(msgs)-1].Content
	items := reItem.FindAllStringSubmatch(user, -1)
	if msgs[0].Content == assignSystem {
		s.assigns++
		// The candidate list names existing threads by code; reuse them.
		code := map[string]string{"Juniper programme": "n1", "Atlas pricing": "n2"}
		for _, m := range regexp.MustCompile(`(T\d+): (Juniper programme|Atlas pricing) —`).FindAllStringSubmatch(user, -1) {
			code[m[2]] = m[1]
		}
		groups := map[string][]int64{}
		// Items from the batch only (the candidate list's "latest" lines
		// are not in brackets).
		for i, m := range items {
			if s.assigns == 1 && i%2 == 1 {
				continue // a collapsed reply
			}
			var id int64
			fmt.Sscan(m[1], &id)
			switch {
			case strings.Contains(m[2], "Atlas"):
				groups[code["Atlas pricing"]] = append(groups[code["Atlas pricing"]], id)
			case strings.Contains(m[2], "agenda"):
				groups["none"] = append(groups["none"], id)
			default:
				groups[code["Juniper programme"]] = append(groups[code["Juniper programme"]], id)
			}
		}
		var out struct {
			NewThreads []map[string]string `json:"new_threads"`
			Groups     []map[string]any    `json:"groups"`
		}
		for name, c := range code {
			if strings.HasPrefix(c, "n") {
				out.NewThreads = append(out.NewThreads, map[string]string{"code": c, "name": name, "scope": "the " + name})
			}
		}
		for c, ids := range groups {
			out.Groups = append(out.Groups, map[string]any{"thread": c, "ids": ids})
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}
	s.reconciles++
	var ask, count int64
	for _, m := range items {
		var id int64
		fmt.Sscan(m[1], &id)
		if strings.HasPrefix(m[2], "How many") {
			ask = id
		}
		if strings.Contains(m[2], "get the exact") {
			count = id
		}
	}
	out := map[string]any{"effects": []map[string]any{}}
	if ask != 0 && count != 0 {
		out["effects"] = []map[string]any{{"later": count, "earlier": ask, "relation": "pending"}}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func TestThreader(t *testing.T) {
	f := newCardFixture(t)
	if err := f.st.ClearThreads(); err != nil {
		t.Fatal(err)
	}
	llm := &scriptedThreads{}
	th := &Threader{Store: f.st, LLM: llm, Chunk: 70, Parallel: 2, Version: "v-test"}
	items := []ThreadItem{
		{ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)},
		{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)},
		{ID: "m:3", Start: time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)},
	}
	ThreadOrder(items)
	if items[0].ID != "m:1" || items[2].ID != "m:2" {
		t.Fatalf("order %v", items)
	}
	for _, it := range items {
		st, err := th.Thread(context.Background(), it)
		if err != nil {
			t.Fatal(err)
		}
		if it.ID == "m:1" && st.Calls < 3 {
			t.Errorf("collapsed assign reply not retried: %d calls", st.Calls)
		}
	}
	// Every membership says when and during which item it was made, and
	// every retirement why.
	rows, _ := f.st.ThreadItemRows()
	retired := 0
	for _, r := range rows {
		if r.AddedIn == "" || r.AddedAt.Unix() == 0 || r.Version != "v-test" {
			t.Errorf("membership without provenance: %+v", r)
		}
		if !r.Current {
			retired++
			if r.RetiredIn == "" || r.RetiredWhy == "" {
				t.Errorf("retirement without provenance: %+v", r)
			}
		}
	}
	_ = retired
	if err := f.st.PlaceClaimsInThreads(); err != nil {
		t.Fatal(err)
	}
	threads, _ := f.st.Threads()
	names := map[string]int{}
	for _, x := range threads {
		names[x.Name] = len(x.Current)
	}
	if len(threads) != 2 || names["Juniper programme"] < 8 || names["Atlas pricing"] != 3 {
		t.Fatalf("threads %v", names)
	}
	for _, x := range threads {
		if x.Name == "Juniper programme" && x.LabelLevel != sensitivity.Restricted {
			t.Errorf("thread named from a restricted line has level %s", x.LabelLevel)
		}
	}
	x, err := BuildCardIndex(f.st)
	if err != nil {
		t.Fatal(err)
	}
	var jun int64
	for _, th := range threads {
		if th.Name == "Juniper programme" {
			jun = th.ID
		}
	}
	v := Views{Store: f.st, Scope: sensitivity.Owner(), Now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	c, err := v.Card(x, Anchor{Topic: jun}, "", "")
	if err != nil || c == nil {
		t.Fatalf("card %v %v", c, err)
	}
	if c.Title != "Juniper programme" {
		t.Errorf("title %q", c.Title)
	}
	found := false
	for _, s := range c.Sections {
		for _, l := range s.Lines {
			if l.ClaimID == f.claims["ask"] {
				found = l.Note != ""
			}
		}
	}
	if !found {
		t.Error("open question on the thread's card doesn't say someone is finding out")
	}
}

func TestThreadRule(t *testing.T) {
	cases := []struct {
		rel, kind  string
		same       bool
		text, want string
	}{
		{"completes", "position", false, "It shipped.", ""},
		{"completes", "commitment", true, "Done.", ""},
		{"completes", "commitment", false, "The list was sent.", "completes"},
		{"supersedes", "metric", true, "Churn is 12.9.", ""},
		{"supersedes", "metric", true, "Correcting that: churn is 12.9.", "supersedes"},
		{"supersedes", "metric", false, "Churn is 12.9.", "supersedes"},
		{"corrects", "metric", true, "It was 12.9.", "supersedes"},
		{"answers", "position", true, "Yes, 40 are done.", "answers"},
	}
	for _, c := range cases {
		if got := threadRule(c.rel, c.kind, c.same, c.text); got != c.want {
			t.Errorf("%s on %s (same item %v, %q): %q, want %q", c.rel, c.kind, c.same, c.text, got, c.want)
		}
	}
}

func TestAuditFixSelection(t *testing.T) {
	v := func(x, y float32) []float32 { return []float32{x, y} }
	th := &threadState{previous: []int64{1, 2, 3, 4, 5, 6, 7, 8}, members: []int64{100}}
	tr := &Threader{AuditFixes: true, Vecs: map[int64][]float32{
		1: v(1, 0), 2: v(0, 1), 3: v(0, 1), 4: v(0, 1), 5: v(0, 1), 6: v(0, 1), 7: v(0, 1), 8: v(0, 1), 100: v(1, 0.05)}}
	// Cap 6: the five latest, plus the oldest, which is nearest the new item.
	if got := tr.shownFor(th, 6); len(got) != 6 || got[0] != 1 || got[5] != 8 {
		t.Errorf("shown %v", got)
	}
	if got := (&Threader{Vecs: tr.Vecs}).shownFor(th, 6); got[0] != 3 {
		t.Errorf("t3 shows the latest: %v", got)
	}
	other := &threadState{previous: []int64{50, 51}}
	tr.Vecs[50], tr.Vecs[51] = v(1, 0.1), v(0, 1)
	ids, of := tr.crossFor(9, map[int64]*threadState{9: th, 10: other})
	if len(ids) != 1 || ids[0] != 50 || of[50] != 10 {
		t.Errorf("cross %v %v", ids, of)
	}
	if got := threadRuleV4("completes", "position", "", false, "x"); got != "completes" {
		t.Errorf("a position can be completed: %q", got)
	}
	if got := threadRuleV4("completes", "fact", "proposed", false, "x"); got != "completes" {
		t.Errorf("a proposal can be completed: %q", got)
	}
	if got := threadRuleV4("completes", "fact", "stated", false, "x"); got != "" {
		t.Errorf("a stated fact isn't completed: %q", got)
	}
}

// fragmentingThreads starts a fresh thread for the programme in every
// meeting (the fragmentation the backfill audits found) and, asked to merge,
// merges each new thread into the first similar older one.
type fragmentingThreads struct {
	n            int
	consolidated bool
}

func (f *fragmentingThreads) Local() bool       { return true }
func (f *fragmentingThreads) ModelName() string { return "fragmenting" }
func (f *fragmentingThreads) Complete(_ context.Context, msgs []provider.Message, _ provider.ChatOptions, _ func(string)) (string, error) {
	user := msgs[len(msgs)-1].Content
	switch {
	case strings.HasPrefix(msgs[0].Content, assignSystem):
		f.n++
		var ids, rest []string
		for _, m := range reItem.FindAllStringSubmatch(user, -1) {
			if strings.Contains(m[2], "Juniper") {
				ids = append(ids, m[1])
			} else {
				rest = append(rest, m[1])
			}
		}
		return fmt.Sprintf(`{"new_threads":[{"code":"n1","name":"Juniper part %d","scope":"the programme"}],"groups":[{"thread":"n1","ids":[%s]},{"thread":"none","ids":[%s]}]}`,
			f.n, strings.Join(ids, ","), strings.Join(rest, ",")), nil
	case msgs[0].Content == mergeSystem:
		born := regexp.MustCompile(`New thread (T\d+)`).FindStringSubmatch(user)
		older := regexp.MustCompile(`Similar older threads:\n(T\d+)`).FindStringSubmatch(user)
		if born == nil || older == nil {
			return `{"merges":[]}`, nil
		}
		return fmt.Sprintf(`{"merges":[{"new":%q,"into":%q}]}`, born[1], older[1]), nil
	case msgs[0].Content == consolidateSystem:
		// The first listed item is replaced by the last.
		ids := regexp.MustCompile(`(?m)^\[(\d+)\]`).FindAllStringSubmatch(user, -1)
		if len(ids) < 2 {
			return `{"outdated":[]}`, nil
		}
		f.consolidated = true
		return fmt.Sprintf(`{"outdated":[{"id":%s,"by":%s,"why":"superseded"}]}`, ids[0][1], ids[len(ids)-1][1]), nil
	default:
		return `{"effects":[]}`, nil
	}
}

func TestThreaderConsolidatesAndLapses(t *testing.T) {
	f := newCardFixture(t)
	if err := f.st.ClearThreads(); err != nil {
		t.Fatal(err)
	}
	sts, _ := f.st.AllStatements()
	vecs := map[int64][]float32{}
	for _, s := range sts {
		vecs[s.ID] = []float32{1, 0, 0}
	}
	llm := &fragmentingThreads{}
	th := &Threader{Store: f.st, LLM: llm, Vecs: vecs, AuditFixes: true}
	if _, err := th.Thread(context.Background(), ThreadItem{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	// Three months on: the first meeting's open question and commitment
	// lapse; consolidation retires the oldest item by the newest.
	if _, err := th.Thread(context.Background(), ThreadItem{ID: "m:2", Start: time.Date(2026, 12, 30, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if !llm.consolidated {
		t.Fatal("no consolidation call")
	}
	rows, _ := f.st.Threads()
	cur := map[int64]bool{}
	for _, r := range rows {
		for _, id := range r.Current {
			cur[id] = true
		}
	}
	for _, s := range sts {
		if s.ItemID == "m:1" && (s.Kind == "question" || s.Kind == "commitment") && cur[s.ID] {
			t.Errorf("%s from three months earlier still current: %q", s.Kind, s.Text)
		}
	}
	// Each retirement says during which item and why.
	ir, _ := f.st.ThreadItemRows()
	why := map[string]int{}
	for _, r := range ir {
		if !r.Current {
			why[r.RetiredWhy]++
			if r.RetiredIn != "m:2" || r.RetiredAt.Unix() == 0 {
				t.Errorf("retirement without provenance: %+v", r)
			}
		}
	}
	if why["lapse"] == 0 || why["consolidate"] == 0 {
		t.Errorf("retirements by reason: %v", why)
	}
}

func TestThreaderMergesFragments(t *testing.T) {
	f := newCardFixture(t)
	if err := f.st.ClearThreads(); err != nil {
		t.Fatal(err)
	}
	sts, _ := f.st.AllStatements()
	vecs := map[int64][]float32{}
	for _, s := range sts {
		if strings.Contains(s.Text, "Juniper") {
			vecs[s.ID] = []float32{1, 0, 0}
		} else {
			vecs[s.ID] = []float32{0, 0, 1}
		}
	}
	th := &Threader{Store: f.st, LLM: &fragmentingThreads{}, Vecs: vecs, AuditFixes: true}
	for _, it := range []ThreadItem{{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}, {ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)}} {
		if _, err := th.Thread(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	threads, _ := f.st.Threads()
	if len(threads) != 1 {
		var names []string
		for _, x := range threads {
			names = append(names, x.Name)
		}
		t.Fatalf("threads %v: the second meeting's fragment wasn't merged", names)
	}
	members, _ := f.st.ThreadMembers()
	juniper := 0
	for _, s := range sts {
		if strings.Contains(s.Text, "Juniper") {
			juniper++
		}
	}
	if len(members) != juniper {
		t.Errorf("thread holds %d statements, want all %d Juniper ones", len(members), juniper)
	}
}

func TestResolveThread(t *testing.T) {
	threads := map[int64]*threadState{12: {}, 40: {}}
	byName := map[string]int64{"atlas pricing": 12}
	local := map[string]int64{"n1": 40, "juniper programme": 40}
	for ref, want := range map[string]int64{"n1": 40, "T12": 12, "[T12]": 12, "T12: Atlas pricing": 12, "Atlas pricing": 12, "Juniper programme": 40, "T99": 0, "n7": 0, "n1: Juniper programme": 40, "n1 + n7": 40, "T12 — Atlas pricing": 12, "none": 0} {
		got, ok := resolveThread(ref, local, threads, byName)
		if got != want || ok != (want != 0) {
			t.Errorf("resolveThread(%q) = %d, %v; want %d", ref, got, ok, want)
		}
	}
}

func TestLeftOut(t *testing.T) {
	for ref, want := range map[string]bool{"none": true, "None — meeting logistics": true, "none: small talk": true, "nonexistent": false, "n1": false} {
		if leftOut(ref) != want {
			t.Errorf("leftOut(%q) = %v", ref, !want)
		}
	}
}
