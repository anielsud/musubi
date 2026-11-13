package stages

import (
	"testing"

	"github.com/anielsud/musubi/internal/item"
)

func TestTranscriptSpeakers(t *testing.T) {
	teams := item.Item{Kind: item.KindMeeting, Source: "teams",
		Participants: []item.Participant{{Name: "Dana Reyes", Email: "dana@northwind.example", Role: "attendee"}},
		Segments: []item.Segment{{Speaker: "Dana Reyes"}, {Speaker: "Sam Okafor", Email: "sam@northwind.example"},
			{Speaker: "dana reyes"}, {Speaker: "Speaker 2"}, {Speaker: "Person 3"}, {Speaker: ""}, {Speaker: "You"}, {Speaker: "Lee Chen"}}}
	event := &item.Item{Participants: []item.Participant{{Name: "Lee Chen", Email: "lee@northwind.example", Role: "attendee"}}}
	got := transcriptSpeakers(teams, event)
	want := []item.Participant{
		{Name: "Dana Reyes", Email: "dana@northwind.example", Role: "speaker"}, // the invitee's email
		{Name: "Sam Okafor", Email: "sam@northwind.example", Role: "speaker"},  // the turn's
		{Name: "Lee Chen", Email: "lee@northwind.example", Role: "speaker"},    // the calendar event's
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	// A recording names its speakers itself: nothing is added.
	rec := teams
	rec.Participants = append([]item.Participant{{Name: "Sam Okafor", Role: "speaker"}}, rec.Participants...)
	if got := transcriptSpeakers(rec, event); got != nil {
		t.Errorf("recording: %+v", got)
	}
	// Only meetings.
	if got := transcriptSpeakers(item.Item{Kind: item.KindEmail, Segments: teams.Segments}, nil); got != nil {
		t.Errorf("email: %+v", got)
	}
}
