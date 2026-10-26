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

// A preferred copy that missed most of the meeting gives way to the other.
func TestMissedCopyGivesWay(t *testing.T) {
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	words := func(n int) []item.Segment {
		var s []item.Segment
		for i := 0; i < n; i++ {
			s = append(s, item.Segment{Text: "one two three four five", Start: float64(i * 10), End: float64(i*10 + 9)})
		}
		return s
	}
	items := []item.Item{
		{ID: "tomoe:x", Source: "tomoe", Kind: item.KindMeeting, OccurredAt: at, EndedAt: at.Add(30 * time.Minute), Segments: words(10)},
		{ID: "bluedot:x", Source: "bluedot", Kind: item.KindMeeting, OccurredAt: at, EndedAt: at.Add(30 * time.Minute), Segments: words(100)},
		{ID: "tomoe:y", Source: "tomoe", Kind: item.KindMeeting, OccurredAt: at.Add(2 * time.Hour), EndedAt: at.Add(150 * time.Minute), Segments: words(95)},
		{ID: "bluedot:y", Source: "bluedot", Kind: item.KindMeeting, OccurredAt: at.Add(2 * time.Hour), EndedAt: at.Add(150 * time.Minute), Segments: words(100)},
	}
	d := meetingDuplicates(items, []string{"tomoe", "bluedot"}, func(string) bool { return false })
	if d["tomoe:x"] != "bluedot:x" || d["bluedot:y"] != "tomoe:y" {
		t.Errorf("duplicates = %v: want Tomoe's broken copy of x to give way, and Tomoe kept for y", d)
	}
}
