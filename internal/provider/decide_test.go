package provider

import (
	"math"
	"testing"
)

func TestMatchChoice(t *testing.T) {
	ch := []string{"same", "supports", "unrelated"}
	for tok, want := range map[string]string{
		"Same": "same", " same": "same", "un": "unrelated", "unrelated": "unrelated",
		"s": "", "maybe": "", "": "",
	} {
		if got := matchChoice(tok, ch); got != want {
			t.Errorf("matchChoice(%q) = %q, want %q", tok, got, want)
		}
	}
}

func TestDistinctChoices(t *testing.T) {
	if distinctChoices([]string{"yes", "no"}) != nil {
		t.Error("yes/no should be distinct")
	}
	if distinctChoices([]string{"no", "none"}) == nil {
		t.Error("no/none share a prefix")
	}
}

func TestNormalizeFailsClosed(t *testing.T) {
	p := shares(map[string]float64{}, []string{"yes", "no"})
	if p["yes"] != 0 || p["no"] != 0 {
		t.Errorf("no mass on any choice should give zeros, got %v", p)
	}
	p = shares(map[string]float64{"yes": 0.6, "no": 0.2}, []string{"yes", "no"})
	if math.Abs(p["yes"]-0.75) > 1e-9 {
		t.Errorf("yes = %v, want 0.75", p["yes"])
	}
}
