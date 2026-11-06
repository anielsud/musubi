package sensitivity

import "testing"

func TestShareOf(t *testing.T) {
	cases := []struct {
		l    Label
		want Share
	}{
		{Label{Level: Public}, SharePublic},
		{Label{Level: Internal}, ShareInternal},
		{Label{Level: Internal, Categories: Strategy}, ShareParty},
		{Label{Level: Confidential}, ShareParty},
		{Label{Level: Confidential, Categories: HR}, ShareParty},
		{Label{Level: Internal, Categories: Personal}, ShareOwner},
		{Label{Level: Restricted, Categories: HR}, ShareParty},
		{Label{Level: Restricted, Categories: Personal}, ShareOwner},
	}
	for _, c := range cases {
		if got := ShareOf(c.l); got != c.want {
			t.Errorf("ShareOf(%v %v) = %s, want %s", c.l.Level, c.l.Categories, got, c.want)
		}
	}
}

func TestShareAgreesWithReplyTo(t *testing.T) {
	// Whatever a reply to everyone party to it may carry is party-only or
	// wider; what it may not carry is owner-only.
	corp := []string{"corp.example"}
	aud := []string{"owner@corp.example", "dana@corp.example"}
	sc := ReplyTo(corp, aud...)
	for lvl := Public; lvl <= Restricted; lvl++ {
		for _, cats := range []Categories{0, HR, Personal, Compensation | Legal} {
			l := Label{Level: lvl, Categories: cats}
			if fits, owner := Visible(l, aud, sc), ShareOf(l) == ShareOwner; fits == owner {
				t.Errorf("%v %v: fits a reply to its party %v, but share is %s", lvl, cats, fits, ShareOf(l))
			}
		}
	}
}

// UpTo must return exactly what ShareOf rates at or below the cap.
func TestUpToAgreesWithShareOf(t *testing.T) {
	for _, cap := range []Share{SharePublic, ShareInternal, ShareParty, ShareOwner} {
		sc := UpTo(cap)
		for lvl := Public; lvl <= Restricted; lvl++ {
			for _, cats := range []Categories{0, HR, Strategy, Personal, HR | Personal} {
				l := Label{Level: lvl, Categories: cats}
				want := !ShareOf(l).Narrower(cap)
				if got := Visible(l, nil, sc); got != want {
					t.Errorf("cap %s, %v %v: visible %v, want %v", cap, lvl, cats, got, want)
				}
			}
		}
	}
}

func TestForAudience(t *testing.T) {
	corp := []string{"corp.example"}
	aud := []string{"owner@corp.example", "dana@corp.example"}
	cases := []struct {
		name string
		for_ []string
		l    Label
		want bool
	}{
		{"colleague, internal", []string{"internal"}, Label{Level: Internal}, true},
		{"colleague, 1:1 they weren't in", []string{"internal"}, Label{Level: Confidential}, false},
		{"external, internal", []string{"external"}, Label{Level: Internal}, false},
		{"external, public", []string{"external"}, Label{Level: Public}, true},
		{"attendee, 1:1", []string{"dana@corp.example"}, Label{Level: Confidential, Categories: HR}, true},
		{"attendee, personal", []string{"dana@corp.example"}, Label{Level: Confidential, Categories: Personal}, false},
		{"attendee plus colleagues, 1:1", []string{"dana@corp.example", "internal"}, Label{Level: Confidential}, false},
		{"attendee plus colleagues, internal", []string{"dana@corp.example", "internal"}, Label{Level: Internal}, true},
		{"attendee and outsiders, internal", []string{"dana@corp.example", "external"}, Label{Level: Internal}, false},
		{"attendee and outsiders, public", []string{"dana@corp.example", "external"}, Label{Level: Public}, true},
		{"non-attendee and outsiders, internal", []string{"sam@corp.example", "external"}, Label{Level: Internal}, false},
	}
	for _, c := range cases {
		sc, err := ForAudience(corp, c.for_)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := Visible(c.l, aud, sc); got != c.want {
			t.Errorf("%s: fits %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := ForAudience(corp, []string{"Dana"}); err == nil {
		t.Error("a bare name should be refused")
	}
	if _, err := ForAudience(corp, nil); err == nil {
		t.Error("no recipients should be refused")
	}
}
