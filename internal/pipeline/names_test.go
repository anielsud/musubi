package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/provider"
)

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Pricing review with Candor Labs": "Pricing review with Candor Labs",
		"\"Roadmap planning\".\n":         "Roadmap planning",
		"Name: Hiring sync":               "Hiring sync",
		"**Budget check-in**":             "Budget check-in",
		"":                                "",
		strings.Repeat("word ", 12):       "",
		"Q4 roadmap\nThis meeting covered stuff.": "Q4 roadmap",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnnamedTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"Teams Meeting 2026-11-06 10:02":   true,
		"Zoom Meeting 2026-11-06 13:04":    true,
		"Session 2026-11-06 10:02":         true,
		"Ad-hoc meeting Feb 9th 2026":      true,
		"Microsoft Teams — Kai Moreno":     false,
		"Planning sync":                    false,
		"Meeting with Robin on 2026-11-06": false,
	} {
		if unnamedTitle.MatchString(title) != want {
			t.Errorf("%q: %v", title, !want)
		}
	}
}

type nameLLM struct{ calls int }

func (n *nameLLM) Complete(_ context.Context, msgs []provider.Message, _ provider.ChatOptions, _ func(string)) (string, error) {
	n.calls++
	return "\"Plan follow-up with Kai\"", nil
}
func (n *nameLLM) Local() bool       { return true }
func (n *nameLLM) ModelName() string { return "stub" }

// A Tomoe recording with a generic title and no calendar event is named
// from its passages; the name survives and isn't redone without a change.
func TestNameMeetings(t *testing.T) {
	r, st := build(t)
	llm := &nameLLM{}
	o := KnowledgeOptions{DigestLLM: llm}
	before, _ := st.GivenTitles()
	n, err := r.NameMeetings(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || llm.calls != n {
		t.Fatalf("named %d with %d calls", n, llm.calls)
	}
	after, _ := st.GivenTitles()
	if len(after) != len(before)+n {
		t.Fatalf("titles %d → %d", len(before), len(after))
	}
	for _, v := range after {
		if v.Title != "Plan follow-up with Kai" || v.Source != "generated" {
			t.Errorf("title %+v", v)
		}
	}
	if n2, _ := r.NameMeetings(context.Background(), o); n2 != 0 {
		t.Errorf("renamed %d without a change", n2)
	}
}
