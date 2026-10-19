package sensitivity

import "testing"

// An interview is about a candidate throughout; a customer interview isn't.
func TestInterviewTitles(t *testing.T) {
	for title, want := range map[string]bool{
		"Interview- Dana Ruiz- VP, Software Engineering": true,
		"Candidate debrief: staff engineer":              true,
		"Customer interviews: onboarding research":       false,
		"Weekly ELT Meeting":                             false,
	} {
		l := ClassifyItem(ItemFacts{Kind: "meeting", Title: title, Audience: []string{"a@corp.example", "b@corp.example", "c@corp.example"}, InternalDomains: []string{"corp.example"}})
		if got := l.Categories&HR != 0; got != want {
			t.Errorf("%q: HR=%v, want %v (%+v)", title, got, want, l)
		}
	}
}
