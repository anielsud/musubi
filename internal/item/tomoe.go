package item

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/phonedetails"
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

// tomoeWithDetails is a phone recording's session with the details the
// owner added on the phone (source/tomoe wraps the two files when the
// session folder has phone-details.json); a plain session file has
// neither key.
type tomoeWithDetails struct {
	Session json.RawMessage `json:"tomoe_session"`
	Details json.RawMessage `json:"phone_details"`
}

// NotesFrom marks the owner's notes among a meeting's segments.
const NotesFrom = "notes"

// maxNoteWords splits a long notes paragraph so a passage stays a
// passage (the chunker cuts only between segments).
const maxNoteWords = 150

func fromTomoe(b []byte) ([]Item, error) {
	var w tomoeWithDetails
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	var details *phonedetails.File
	if len(w.Session) > 0 {
		b = w.Session
		// Details that don't hold up are left out; the meeting is not.
		details, _ = phonedetails.Parse(w.Details)
	}
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
	if details != nil {
		addDetails(&it, &details.Details)
	}
	return []Item{it}, nil
}

// addDetails puts the owner's phone details on a recording's item: the
// people picked as present become its attendees (after its speakers),
// the notes its last segments, the owner's, marked NotesFrom, so they are
// passages with the meeting's label, searchable like the rest. Photos
// stay out of the item: nothing derived from them reaches a stage or a
// model.
func addDetails(it *Item, d *phonedetails.Details) {
	have := map[string]bool{}
	for _, p := range it.Participants {
		have[strings.ToLower(p.Name)] = true
	}
	for _, a := range d.Attendees {
		if have[strings.ToLower(a.Name)] {
			continue
		}
		have[strings.ToLower(a.Name)] = true
		it.Participants = append(it.Participants, Participant{Name: a.Name, Email: a.Email, Role: "attendee"})
	}
	at := it.EndedAt.Sub(it.OccurredAt).Seconds()
	if at < 0 {
		at = 0
	}
	for _, para := range noteParagraphs(d.Notes.Text) {
		it.Segments = append(it.Segments, Segment{Speaker: "You", Start: at, End: at, Text: para, From: NotesFrom})
	}
}

// noteParagraphs splits notes into their non-empty lines, each cut to at
// most maxNoteWords words.
func noteParagraphs(text string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		words := strings.Fields(line)
		for len(words) > 0 {
			n := min(len(words), maxNoteWords)
			out = append(out, strings.Join(words[:n], " "))
			words = words[n:]
		}
	}
	return out
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
		if out.Segments[i].Speaker == old && out.Segments[i].From != NotesFrom {
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
		if s.From == NotesFrom {
			continue // the owner's notes name no speaker
		}
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
	// People the owner listed as present stay, after the speakers, as
	// the translation puts them.
	for _, p := range it.Participants {
		if p.Role == "attendee" && !seen[p.Name] && p.Name != old {
			out.Participants = append(out.Participants, p)
		}
	}
	return out, true
}
