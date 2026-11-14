package transcribe

import "testing"

func TestRedoLanguage(t *testing.T) {
	allowed := []string{"sv", "en"}
	for _, c := range []struct {
		detected, last string
		allowed        []string
		want           string
	}{
		{"sv", "", allowed, ""},       // allowed: kept
		{"<|en|>", "sv", allowed, ""}, // sherpa's token form
		{"is", "", allowed, "sv"},     // nothing allowed seen yet: first
		{"ja", "en", allowed, "en"},   // still the conversation's language
		{"no", "en", allowed, "sv"},   // Swedish heard as Norwegian
		{"is", "en", allowed, "sv"},
		{"da", "en", []string{"en", "de"}, "en"}, // no Swedish allowed: last
		{"ja", "", nil, ""},                      // no list: unchecked
	} {
		if got := redoLanguage(c.detected, c.allowed, c.last); got != c.want {
			t.Errorf("redoLanguage(%q, %v, %q) = %q, want %q", c.detected, c.allowed, c.last, got, c.want)
		}
	}
}
