package transcribe

import "testing"

func TestCandidateLangs(t *testing.T) {
	for _, c := range []struct{ in, fixed, whisper string }{
		{"", "en", "en"},   // unset: English, as before the setting
		{"auto", "en", ""}, // Whisper detects per utterance
		{"sv", "sv", "sv"}, // a fixed language for every family
	} {
		fixed, whisper := candidateLangs(c.in)
		if fixed != c.fixed || whisper != c.whisper {
			t.Errorf("candidateLangs(%q) = %q, %q; want %q, %q", c.in, fixed, whisper, c.fixed, c.whisper)
		}
	}
}
