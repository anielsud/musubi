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
	th := &Threader{Store: f.st, LLM: llm, Chunk: 70, Parallel: 2}
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
