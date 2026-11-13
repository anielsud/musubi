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
	// EventID: the calendar event a phone recording was of (the
	// recorder's import); linking takes it over matching by time.
	EventID  string `json:"event_id"`
	Segments []struct {
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
	// A name the user gave a speaker in Tomoe is known; Tomoe's own
	// placeholders ("Person 3", "New speaker", a provisional "Ana?") aren't.
	if tomoePlaceholder.MatchString(s) || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "?)") || isRoomName(s) {
		return s, false, false
	}
	return strings.TrimSpace(s), false, s != ""
}

var tomoePlaceholder = regexp.MustCompile(`^(Person \d+|New speaker|System Audio)( \(.*\))?$`)

func isRoomName(s string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !('a' <= r && r <= 'z') }) {
		if w == "room" || w == "rooms" {
			return true
		}
	}
	return false
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
	if s.EventID != "" {
		it.Native["event_id"] = s.EventID
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

// TomoeContentType is a recorder session file's content type, for
// translating one outside ingest (a relabel reads the rewritten file).
const TomoeContentType = tomoeContentType

// IsPersonName reports whether a recording would count name, as a
// speaker's name, as a known person: not "You", a placeholder, a room or
// a provisional name, whether it comes as "Person 3 (name)" or alone.
func IsPersonName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "You" {
		return false
	}
	_, _, plain := tomoeSpeaker(name)
	_, _, hinted := tomoeSpeaker("Person 1 (" + name + ")")
	return plain && hinted
}

// RenameSpeaker is a recording's item with every line old speaks given to
// newName, its participants worked out again as the translation does
// (in order of first line; known people only). It is how ingest would
// translate the session with those lines relabelled, for when the file
// itself can't be read. ok is false when old speaks no line or newName
// isn't a person's name.
func RenameSpeaker(it Item, old, newName string) (Item, bool) {
	newName = strings.TrimSpace(newName)
	if old == newName || !IsPersonName(newName) {
		return it, false
	}
	out := it
	out.Segments = append([]Segment(nil), it.Segments...)
	n := 0
	for i := range out.Segments {
		if out.Segments[i].Speaker == old {
			out.Segments[i].Speaker = newName
			n++
		}
	}
	if n == 0 {
		return it, false
	}
	known := map[string]Participant{}
	for _, p := range it.Participants {
		if p.Name != old {
			known[p.Name] = p
		}
	}
	if _, ok := known[newName]; !ok {
		known[newName] = Participant{Name: newName, Role: "speaker"}
	}
	out.Participants = nil
	out.Native = map[string]string{}
	for k, v := range it.Native {
		if k != "unknown_participants" {
			out.Native[k] = v
		}
	}
	seen := map[string]bool{}
	for _, s := range out.Segments {
		p, ok := known[s.Speaker]
		if !ok {
			out.Native["unknown_participants"] = "true"
			continue
		}
		if !seen[s.Speaker] {
			seen[s.Speaker] = true
			out.Participants = append(out.Participants, p)
		}
	}
	return out, true
}
