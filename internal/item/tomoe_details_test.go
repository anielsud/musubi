package item

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/source"
)

const detailsSession = `{"title":"Phone Meeting 2026-11-17 10:02","platform":"phone","created_at":"2026-11-17T10:02:00+01:00","ended_at":"2026-11-17T10:12:00+01:00","segments":[{"speaker":"You","text":"Shall we start?","start_time":1,"end_time":2},{"speaker":"Person 1 (Ana Example)","text":"Yes, the booth layout first.","start_time":3,"end_time":5}]}`

func detailsJSON(rev int, notes string) string {
	return `{"writer":"musubi","recording":"1b4e28ba-2fa1-41d2-883f-0016d3cca427","rev":` + strconv.Itoa(rev) +
		`,"attendees":[{"name":"Ana Example"},{"name":"Bo Sample","email":"Bo@Example.test"}],"notes":{"text":` + quote(notes) +
		`,"updated":"2026-11-17T10:20:00+01:00"},"photos":[{"name":"photo-0001.jpg","size":1234,"sha256":"` + strings.Repeat("ab", 32) + `","taken":"2026-11-17T10:05:00+01:00"}]}`
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func wrapped(session, details string) []byte {
	return []byte(`{"tomoe_session":` + session + `,"phone_details":` + details + `}`)
}

// A phone recording's details: the people listed join its participants
// (once, after the speakers), the notes become the owner's segments
// marked as notes, after what was said; the photos leave no trace.
func TestTomoeDetails(t *testing.T) {
	its, err := Translate(source.Record{Source: "tomoe", Ref: "s1", ContentType: tomoeContentType,
		Data: wrapped(detailsSession, detailsJSON(2, "Pricing for the badge scanners\n\nFollow up on the floor plan"))}, "h")
	if err != nil {
		t.Fatal(err)
	}
	it := its[0]
	var parts []string
	for _, p := range it.Participants {
		parts = append(parts, p.Name+"/"+p.Role+"/"+p.Email)
	}
	if got := strings.Join(parts, ","); got != "You/speaker/,Ana Example/speaker/,Bo Sample/attendee/bo@example.test" {
		t.Errorf("participants %s", got)
	}
	if len(it.Segments) != 4 {
		t.Fatalf("segments %+v", it.Segments)
	}
	n := it.Segments[2]
	if n.From != NotesFrom || n.Speaker != "You" || n.Text != "Pricing for the badge scanners" || n.Start != 600 {
		t.Errorf("notes segment %+v", n)
	}
	b, _ := json.Marshal(it)
	if strings.Contains(string(b), "photo-0001") || strings.Contains(string(b), strings.Repeat("ab", 32)) {
		t.Error("a photo reached the item")
	}

	// A later rev is a different item; the session alone is as before.
	its2, _ := Translate(source.Record{Source: "tomoe", Ref: "s1", ContentType: tomoeContentType,
		Data: wrapped(detailsSession, detailsJSON(3, "Other notes"))}, "h2")
	if its2[0].ContentHash() == it.ContentHash() || its2[0].Segments[2].Text != "Other notes" {
		t.Error("new notes not taken in")
	}
	plain, _ := fromTomoe([]byte(detailsSession))
	if len(plain[0].Segments) != 2 || len(plain[0].Participants) != 2 {
		t.Errorf("plain session %+v", plain[0])
	}
	// Details that don't hold up are left out, the meeting is not.
	bad, err := fromTomoe(wrapped(detailsSession, `{"writer":"musubi","rev":1,"attendees":[],"notes":{"text":"x"},"photos":[{"name":"../x.jpg","size":1,"sha256":"`+strings.Repeat("ab", 32)+`"}]}`))
	if err != nil || len(bad[0].Segments) != 2 {
		t.Errorf("bad details: %v %+v", err, bad)
	}
}

// Renaming a speaker keeps the notes and the listed people.
func TestRenameKeepsDetails(t *testing.T) {
	its, _ := fromTomoe(wrapped(detailsSession, detailsJSON(1, "note")))
	out, ok := RenameSpeaker(its[0], "Ana Example", "Cy Person")
	if !ok || out.Segments[2].From != NotesFrom || out.Segments[2].Speaker != "You" {
		t.Fatalf("%v %+v", ok, out.Segments)
	}
	last := out.Participants[len(out.Participants)-1]
	if last.Name != "Bo Sample" || last.Role != "attendee" {
		t.Errorf("participants %+v", out.Participants)
	}
}
