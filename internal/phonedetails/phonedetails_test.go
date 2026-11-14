package phonedetails

import (
	"strings"
	"testing"
)

func TestMetaDetailsCheck(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	ok := MetaDetails{Rev: 2, Attendees: []Attendee{{Name: " Ana Example ", Email: "Ana@Example.test"}}, Notes: NotesRef{Size: 10, SHA256: strings.ToUpper(sha)},
		Photos: []Photo{{Name: "photo-0001.jpg", Size: 5, SHA256: sha}}}
	if err := ok.Check(); err != nil || ok.Attendees[0].Name != "Ana Example" || ok.Notes.SHA256 != sha {
		t.Fatalf("%v %+v", err, ok)
	}
	for name, m := range map[string]MetaDetails{
		"notes too big":   {Notes: NotesRef{Size: MaxNotesBytes + 1, SHA256: sha}},
		"notes no hash":   {Notes: NotesRef{Size: 3}},
		"photo traversal": {Photos: []Photo{{Name: "../x.jpg", Size: 1, SHA256: sha}}},
		"too many people": {Attendees: make([]Attendee, MaxAttendees+1)},
	} {
		if err := m.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if CheckNotes([]byte(strings.Repeat("語", MaxNotes))) != nil || CheckNotes([]byte(strings.Repeat("x", MaxNotes+1))) == nil || CheckNotes([]byte{0xff, 0xfe}) == nil {
		t.Error("notes text bounds")
	}
}
