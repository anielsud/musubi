package item

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

type tomoeSession struct {
	Title     string    `json:"title"`
	Platform  string    `json:"platform"`
	CreatedAt time.Time `json:"created_at"`
	EndedAt   time.Time `json:"ended_at"`
	Duration  float64   `json:"duration"`
	Segments  []struct {
		Speaker string  `json:"speaker"`
		Text    string  `json:"text"`
		Start   float64 `json:"start_time"`
		End     float64 `json:"end_time"`
		Status  string  `json:"status"`
	} `json:"segments"`
}

// Tomoe labels speakers "You", "Person 3", or "Person 3 (Real Name)" once a
// name hint has matched the voice.
var tomoeNamed = regexp.MustCompile(`^Person \d+ \((.+)\)$`)

// tomoeSpeaker returns the display name, whether it is the owner, and
// whether the person is identified at all.
func tomoeSpeaker(s string) (name string, owner, known bool) {
	if s == "You" {
		return "You", true, true
	}
	if m := tomoeNamed.FindStringSubmatch(s); m != nil {
		n := strings.TrimSpace(m[1])
		// A meeting-room device stands for an unknown number of people.
		if l := strings.ToLower(n); strings.HasPrefix(l, "room") || strings.HasSuffix(l, " room") || strings.Contains(l, " room ") {
			return n, false, false
		}
		return n, false, true
	}
	return s, false, false
}

func fromTomoe(b []byte) ([]Item, error) {
	var s tomoeSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	it := Item{Kind: KindMeeting, Title: s.Title, OccurredAt: s.CreatedAt, EndedAt: s.EndedAt, Native: map[string]string{}}
	if it.EndedAt.IsZero() {
		it.EndedAt = s.CreatedAt.Add(time.Duration(s.Duration * float64(time.Second)))
	}
	if s.Platform != "" {
		it.Native["platform"] = s.Platform
	}
	seen := map[string]bool{}
	for _, g := range s.Segments {
		// Only final lines; "live" and "pending" are drafts Tomoe replaces.
		if g.Status != "" || strings.TrimSpace(g.Text) == "" {
			continue
		}
		name, owner, known := tomoeSpeaker(g.Speaker)
		it.Segments = append(it.Segments, Segment{Speaker: name, Start: g.Start, End: g.End, Text: strings.TrimSpace(g.Text)})
		if !known {
			it.Native["unknown_participants"] = "true"
			continue
		}
		if !seen[name] {
			seen[name] = true
			it.Participants = append(it.Participants, Participant{Name: name, Role: "speaker", Owner: owner})
		}
	}
	return []Item{it}, nil
}
