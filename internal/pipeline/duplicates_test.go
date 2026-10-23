package pipeline

import (
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

func TestMeetingDuplicates(t *testing.T) {
	at := time.Date(2026, 10, 21, 12, 5, 0, 0, time.UTC)
	m := func(id, src string, start time.Time, mins int) item.Item {
		return item.Item{ID: id, Source: src, Kind: item.KindMeeting, OccurredAt: start, EndedAt: start.Add(time.Duration(mins) * time.Minute)}
	}
	items := []item.Item{
		m("tomoe:a", "tomoe", at, 55),
		m("teams:a", "teams", at.Add(2*time.Minute), 50), // the same meeting
		m("teams:b", "teams", at.Add(3*time.Hour), 30),   // another
		m("tomoe:c", "tomoe", at.Add(5*time.Hour), 30),
		m("zoom:c", "zoom", at.Add(5*time.Hour+20*time.Minute), 30), // starts too late to be the same
	}
	none := func(string) bool { return false }
	d := meetingDuplicates(items, []string{"tomoe"}, none)
	if len(d) != 1 || d["teams:a"] != "tomoe:a" {
		t.Errorf("duplicates = %v, want the transcript of meeting a standing down for Tomoe's", d)
	}
	// Already extracted wins over the preferred source.
	d = meetingDuplicates(items, []string{"tomoe"}, func(id string) bool { return id == "teams:a" })
	if d["tomoe:a"] != "teams:a" {
		t.Errorf("duplicates = %v, want the extracted transcript kept", d)
	}
}
