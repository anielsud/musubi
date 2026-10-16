package knowledge

import (
	"strings"
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

func TestDatesIn(t *testing.T) {
	said := time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)
	ds := DatesIn("A list of names by October 1st, rationale two weeks after, counsel briefed by November 1st; the law changes January 1st.", said)
	var got []string
	for _, d := range ds {
		got = append(got, time.Unix(int64(d.Lo), 0).UTC().Format("2006-01-02"))
	}
	want := []string{"2026-10-01", "2026-10-15", "2026-11-01", "2027-01-01"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dates %v, want %v", got, want)
	}
	if ds := DatesIn("You may march on", said); len(ds) != 0 {
		t.Fatalf("verbs read as months: %v", ds)
	}
	if v := ValueFromText("metric", "Targets for 2026 are 85 reductions", said); v.Lo != 85 {
		t.Fatalf("metric value %+v", v)
	}
	if v := ValueFromText("commitment", "Will send the deck by end of the year", said); time.Unix(int64(v.Lo), 0).UTC().Month() != time.December {
		t.Fatalf("year end %+v", v)
	}
}
