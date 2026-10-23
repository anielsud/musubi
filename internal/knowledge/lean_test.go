package knowledge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// fakeLean answers lean calls: a brief, then one item per part naming the
// part's first line. failAt fails the part starting at that line.
type fakeLean struct {
	mu     sync.Mutex
	users  []string
	failAt int
}

func (f *fakeLean) Local() bool       { return false }
func (f *fakeLean) ModelName() string { return "fake" }
func (f *fakeLean) Complete(_ context.Context, msgs []provider.Message, opt provider.ChatOptions, _ func(string)) (string, error) {
	u := msgs[len(msgs)-1].Content
	f.mu.Lock()
	f.users = append(f.users, u)
	f.mu.Unlock()
	if opt.Kind == "brief" {
		return `{"purpose":"planning","participants":[{"name":"Ilse Novak","role":"chair"}],"subjects":[],"glossary":[{"term":"NW","means":"Northwind"}]}`, nil
	}
	var a, b int
	fmt.Sscanf(u[strings.Index(u, "unmarked lines ")+len("unmarked lines "):], "%d-%d", &a, &b)
	if a == f.failAt {
		return "", fmt.Errorf("cut off")
	}
	return fmt.Sprintf(`{"items":[{"kind":"fact","text":"part %d","who":[],"lines":[%d],"status":"stated"}]}`, a, a), nil
}

func leanMeeting(n int) *Meeting {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i%2 == 0 {
			fmt.Fprintf(&b, "Ilse Novak: line %d\n", i)
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	return NewMeeting("Planning", time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC), "", "Owner", []store.Chunk{{Text: b.String()}})
}

func TestLeanExtract(t *testing.T) {
	m := leanMeeting(120)
	if l := m.LeanLines(); l[0] != "1 unknown speaker: line 1" || l[1] != "2 Ilse Novak: line 2" {
		t.Fatalf("lines: %q %q", l[0], l[1])
	}
	f := &fakeLean{}
	items, err := LeanExtract(context.Background(), f, m, LeanParams{Segment: 50, Context: 8, Parallel: 4, People: "- Ilse Novak"}, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || len(f.users) != 4 {
		t.Fatalf("%d items from %d calls; want 3 parts after a brief", len(items), len(f.users))
	}
	var second string
	for _, u := range f.users {
		if strings.Contains(u, "unmarked lines 51-100") {
			second = u
		}
	}
	for _, want := range []string{"[context] 43 unknown speaker: line 43", "\n51 unknown speaker: line 51", "[context] 108 Ilse Novak: line 108", "Terms: NW = Northwind", "- Ilse Novak"} {
		if !strings.Contains(second, want) {
			t.Errorf("part 51-100 lacks %q", want)
		}
	}
	if strings.Contains(second, "[context] 42 ") || strings.Contains(second, " 109 ") {
		t.Error("part 51-100 reads more than 8 lines of context")
	}
	// A failed part fails the meeting: it's extracted again, not stored with a hole.
	if _, err := LeanExtract(context.Background(), &fakeLean{failAt: 51}, m, LeanParams{Segment: 50, Context: 8, Parallel: 4}, "s"); err == nil {
		t.Error("a failed part was ignored")
	}
}

func TestLeanLinesUnknownSpeakers(t *testing.T) {
	m := NewMeeting("x", time.Now(), "", "Owner", []store.Chunk{{Text: "Person 3: a\nRoom - Oslo 4: b\nIlse Novak: c\nAna?: d"}})
	got := m.LeanLines()
	want := []string{"1 unknown speaker: a", "2 unknown speaker: b", "3 Ilse Novak: c", "4 unknown speaker: d"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i+1, got, want[i])
		}
	}
}
