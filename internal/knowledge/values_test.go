package knowledge

import (
	"testing"
	"time"
)

var said = time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

func TestDatesCompare(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"September 22", "September 29", "differs"},
		{"November", "Q4", "within"},
		{"Q4", "mid November", "contains"},
		{"Sept 22", "22 September", "same"},
		{"October 20", "Q4 2026", "within"},
		{"next week", "September 22", "contains"},
	}
	for _, c := range cases {
		got := Compare(Normalize("date", c.a, said), Normalize("date", c.b, said))
		if got != c.want {
			t.Errorf("%q vs %q: %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestNumbers(t *testing.T) {
	for raw, want := range map[string]float64{"15%": 15, "$40M": 40e6, "forty million": 40e6, "152k": 152e3, "1,200": 1200, "nine hundred": 900} {
		v := Normalize("number", raw, said)
		if !v.Normalized || v.Lo != want {
			t.Errorf("%q → %v (%v), want %v", raw, v.Lo, v.Normalized, want)
		}
	}
	if Compare(Normalize("duration", "two years", said), Normalize("duration", "three years", said)) != "differs" {
		t.Error("two years vs three years should differ")
	}
}
