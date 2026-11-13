package gui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/session"
)

// A meeting where the recorder named one speaker from the window, left two
// unnamed, and the owner is "You"; the Teams transcript has everyone.
func teamsFixture() (*session.Session, *session.TeamsTranscript) {
	created := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	who := []struct{ label, teams string }{
		{"Person 1", "Sam Okafor"},
		{"Person 2", "Lee Chen"},
		{"Person 3 (Dana Reyes)", "Dana Reyes"},
		{"You", "Jordan Pike"},
		{"Person 4", "Dana Reyes"}, // a second label for someone already named
	}
	sess := &session.Session{ID: "s-1", CreatedAt: created}
	tt := &session.TeamsTranscript{Writer: "musubi", Source: "teams", Start: created.Add(5 * time.Second)}
	at := 0.0
	for turn := 0; turn < 60; turn++ {
		w := who[turn%len(who)]
		var words []string
		for i := 0; i < 20; i++ {
			words = append(words, fmt.Sprintf("w%dx%d", turn, i))
		}
		text := strings.Join(words, " ")
		sess.Segments = append(sess.Segments, session.Segment{Speaker: w.label, Text: text, StartTime: at, EndTime: at + 9})
		// Teams' clock starts 5 s after the recording's.
		tt.Turns = append(tt.Turns, session.TeamsTurn{Speaker: w.teams, Offset: at - 5, Text: text})
		at += 10
	}
	return sess, tt
}

func TestTeamsRenames(t *testing.T) {
	sess, tt := teamsFixture()
	got := teamsRenames(sess, tt, "Jordan Pike")
	want := map[string]string{"Person 1": "Sam Okafor", "Person 2": "Lee Chen"}
	if len(got) != len(want) {
		t.Fatalf("renames %+v, want %v", got, want)
	}
	for _, r := range got {
		if want[r.Label] != r.Name {
			t.Errorf("%s → %q, want %q", r.Label, r.Name, want[r.Label])
		}
	}

	// A transcript of another meeting (text that doesn't line up) names no one.
	_, other := teamsFixture()
	for i := range other.Turns {
		other.Turns[i].Text = fmt.Sprintf("unrelated words %d about something else entirely", i)
	}
	if got := teamsRenames(sess, other, "Jordan Pike"); len(got) != 0 {
		t.Errorf("another meeting's transcript named %+v", got)
	}
	if teamsRenames(sess, nil, "") != nil {
		t.Error("no transcript named someone")
	}
}
