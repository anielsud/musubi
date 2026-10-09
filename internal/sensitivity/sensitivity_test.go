package sensitivity

import "testing"

func TestVisible(t *testing.T) {
	open := Label{Level: Internal}
	oneToOne := Label{Level: Confidential}
	comp := Label{Level: Restricted, Categories: Compensation}
	aud := []string{"owner@corp.example", "dana@corp.example"}
	corp := []string{"corp.example"}

	cases := []struct {
		name string
		l    Label
		s    Scope
		want bool
	}{
		{"owner sees restricted", comp, Owner(), true},
		{"shared screen hides confidential", oneToOne, SharedScreen(), false},
		{"shared screen shows internal", open, SharedScreen(), true},
		{"shared screen hides categorised internal", Label{Level: Internal, Categories: DealTerms}, SharedScreen(), false},
		{"reply to attendee sees 1:1", oneToOne, ReplyTo(corp, "Dana@corp.example"), true},
		{"reply to colleague hides 1:1", oneToOne, ReplyTo(corp, "sam@corp.example"), false},
		{"reply to colleague sees internal", open, ReplyTo(corp, "sam@corp.example"), true},
		{"reply to customer hides internal", open, ReplyTo(corp, "tom@customer.example"), false},
		{"reply to customer sees public", Label{Level: Public}, ReplyTo(corp, "tom@customer.example"), true},
		{"reply to customer hides what they did not attend", oneToOne, ReplyTo(corp, "tom@customer.example"), false},
		{"reply to both needs both in audience", comp, ReplyTo(corp, "dana@corp.example", "sam@corp.example"), false},
		{"cloud hides confidential", oneToOne, Cloud(), false},
	}
	for _, c := range cases {
		if got := Visible(c.l, aud, c.s); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestClassifyItem(t *testing.T) {
	internal := []string{"corp.example"}
	cases := []struct {
		name string
		f    ItemFacts
		want Level
	}{
		{"unknown participants fail closed", ItemFacts{Audience: []string{"owner@corp.example", "a@corp.example", "b@corp.example"}, UnknownParticipants: true}, Confidential},
		{"small internal group", ItemFacts{Audience: []string{"owner@corp.example", "a@corp.example", "b@corp.example"}}, Confidential},
		{"internal group over threshold", ItemFacts{Audience: []string{"owner@corp.example", "a@corp.example", "b@corp.example"}, InternalMinAudience: 3}, Internal},
		{"broadcast", ItemFacts{Audience: []string{"owner@corp.example", "all@corp.example"}, Broadcast: true}, Internal},
		{"one to one", ItemFacts{Audience: []string{"owner@corp.example", "a@corp.example"}}, Confidential},
		{"external", ItemFacts{Audience: []string{"owner@corp.example", "a@corp.example", "x@customer.example"}}, Confidential},
		{"native private", ItemFacts{Audience: []string{"owner@corp.example", "a@corp.example", "b@corp.example"}, Native: map[string]string{"sensitivity": "private"}}, Confidential},
		{"title escalates", ItemFacts{Title: "Performance review: Q3", Audience: []string{"owner@corp.example", "a@corp.example", "b@corp.example"}}, Restricted},
	}
	for _, c := range cases {
		c.f.InternalDomains = internal
		if got := ClassifyItem(c.f).Level; got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestClassifyText(t *testing.T) {
	hit := map[string]Categories{
		"We agreed her raise to 145k starts in January":           Compensation,
		"They want a 15% discount on a two-year renewal":          DealTerms,
		"The diligence on the acquisition of the startup is done": Strategy,
		"He's going on medical leave next week":                   Health,
	}
	for text, want := range hit {
		l, ok := ClassifyText(text)
		if !ok || l.Categories&want == 0 {
			t.Errorf("%q: got %v, want %v", text, l.Categories, want)
		}
	}
	miss := []string{
		"Customer acquisition costs went down last quarter",
		"Let's review the search latency dashboard",
	}
	for _, text := range miss {
		if l, ok := ClassifyText(text); ok {
			t.Errorf("%q: unexpected %v (%s)", text, l.Categories, l.Reason)
		}
	}
}

func TestInternalDomain(t *testing.T) {
	in := []string{"corp.com"}
	for dom, want := range map[string]bool{
		"corp.com": true, "eu.corp.com": true, "corp.no": true, "corp.co.uk": true,
		"corp.agency.com": false, "corpx.com": false, "customer.com": false,
	} {
		if got := InternalDomain(dom, in); got != want {
			t.Errorf("%s: got %v want %v", dom, got, want)
		}
	}
}
