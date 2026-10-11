package knowledge

import "testing"

func TestGround(t *testing.T) {
	lines := []Line{
		{Speaker: "Priya", Text: "So we both think it's a fair trade. To be clear we don't think what we hand Contoso gives away the store."},
		{Speaker: "Tomas", Text: "We can accept the forty five days if we choose which items count."},
		{Speaker: "Tomas", Text: "Honestly I bet it renews for three years not two."},
	}
	cases := []struct {
		name string
		e    Extracted
		ok   bool
	}{
		{"faithful", Extracted{Line: 1, Quote: "we don't think what we hand Contoso gives away the store", Text: "Priya does not think what is handed to Contoso gives away the store."}, true},
		{"negation dropped", Extracted{Line: 1, Quote: "it's a fair trade", Text: "Priya thinks it is a fair trade to give Contoso the store."}, true},
		{"negation dropped, quoted", Extracted{Line: 1, Quote: "we don't think what we hand Contoso gives away the store", Text: "Priya is fine giving Contoso the store."}, false},
		{"figure matches words", Extracted{Line: 2, Quote: "We can accept the forty five days", Text: "Tomas accepts 45 days."}, true},
		{"figure changed", Extracted{Line: 2, Quote: "We can accept the forty five days", Text: "Tomas accepts 90 days."}, false},
		{"contrast rewritten", Extracted{Line: 3, Quote: "it renews for three years not two", Text: "Tomas expects it to renew for three years instead of two."}, true},
		{"invented quote", Extracted{Line: 2, Quote: "we will sign the contract tomorrow morning", Text: "The contract is signed tomorrow."}, false},
	}
	for _, c := range cases {
		if ok, issues := Ground(c.e, lines); ok != c.ok {
			t.Errorf("%s: grounded=%v %v, want %v", c.name, ok, issues, c.ok)
		}
	}
}
