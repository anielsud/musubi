package gui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A meeting row on a voice profile offers "Right, it's them" and "Not
// them" only: "Someone else…" (moving the meeting to another profile)
// left the UI on 2026-11-12; the recorder keeps move_voice_meeting.
func TestNoMoveMeetingInVoicesUI(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "components", "voices.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"move_voice_meeting", "MoveForm", "setMoving"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("voices.tsx still has %q", bad)
		}
	}
}

// Every voice action button says what happens and what is kept
// (title=).
func TestVoiceButtonsHaveTooltips(t *testing.T) {
	labels := regexp.MustCompile(`^(Confirm|Rename|Right|Right, it&apos;s them|Not them|Junk|Put back|Can&apos;t tell|Same person|Different people|Same: .*|It&apos;s .*|Someone else…|\{button\}|Split|Split by voice…|Undo)$`)
	seen := map[string]bool{}
	for _, f := range []string{"voices.tsx", "voicesamples.tsx", "MeetingsView.tsx"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "components", f))
		if err != nil {
			t.Fatal(err)
		}
		for _, btn := range buttons(string(b)) {
			text := strings.TrimSpace(btn[1])
			if !labels.MatchString(text) {
				continue
			}
			seen[text] = true
			if !strings.Contains(btn[0], "title=") {
				t.Errorf("%s: %q has no tooltip", f, text)
			}
		}
	}
	for _, want := range []string{"Confirm", "Rename", "Right, it&apos;s them", "Not them", "Junk", "Put back", "Can&apos;t tell", "Same person", "Different people", "{button}"} {
		if !seen[want] {
			t.Errorf("no %q button found", want)
		}
	}
}

// buttons are the JSX <button> elements in src: their attributes (braces
// skipped over, so an arrow's ">" doesn't end them) and their text.
func buttons(src string) [][2]string {
	var out [][2]string
	for i := 0; ; {
		k := strings.Index(src[i:], "<button")
		if k < 0 {
			return out
		}
		i += k + len("<button")
		depth, j := 0, i
		for ; j < len(src); j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if src[j] == '>' && depth == 0 {
				break
			}
		}
		if j >= len(src) {
			return out
		}
		end := strings.Index(src[j:], "</button>")
		if end < 0 {
			return out
		}
		out = append(out, [2]string{src[i:j], src[j+1 : j+end]})
		i = j + end
	}
}
