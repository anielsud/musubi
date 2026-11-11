package item

import (
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/mcpdrop"
)

func TestDropMeeting(t *testing.T) {
	data := []byte(`{"kind":"meeting","source":"teams","ref":"m1","record":{
		"title":"Weekly sync","start":"2026-03-09T15:00:00Z",
		"attendees":[{"name":"Dana Reyes","email":"dana@northwind.example"}],
		"turns":[{"speaker":"Dana Reyes","offset":3,"text":" Northwind renewal is stuck. "},
		         {"speaker":"","offset":9,"text":"It's not true."}]}}`)
	items, err := Translate(source.Record{Source: "teams", Ref: "m1", ContentType: mcpdrop.ContentType, Data: data}, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items: %d", len(items))
	}
	it := items[0]
	if it.Kind != KindMeeting || it.Title != "Weekly sync" || len(it.Segments) != 2 || len(it.Participants) != 1 {
		t.Fatalf("item: %+v", it)
	}
	if it.Segments[0].Text != "Northwind renewal is stuck." || it.Segments[0].Start != 3 || it.Segments[1].Speaker != "" {
		t.Errorf("segments: %+v", it.Segments)
	}
	if _, err := Translate(source.Record{Source: "teams", Ref: "m2", ContentType: mcpdrop.ContentType,
		Data: []byte(`{"kind":"meeting","source":"teams","ref":"m2","record":{"title":"x","turns":[]}}`)}, "h"); err == nil {
		t.Error("a meeting without a start time must be rejected")
	}
}

// The m365 calendar sync adds the join link, iCalendar id and the owner's
// response; they're kept as native fields.
func TestDropEventKeepsSyncFields(t *testing.T) {
	b := []byte(`{"kind":"event","source":"m365-calendar","ref":"e1","record":{"subject":"Planning","organizer":{"name":"Robin Vale","email":"robin.vale@corp.example"},
		"attendees":[{"name":"Kai Moreno","email":"kai.moreno@corp.example","response":"accepted"}],
		"start":{"dateTime":"2026-03-02T15:00:00.0000000","timeZone":"UTC"},"end":{"dateTime":"2026-03-02T16:00:00.0000000","timeZone":"UTC"},
		"joinUrl":"https://teams.example/l/meetup-join/x","iCalUId":"ical-1","response":"organizer"}}`)
	its, err := fromDrop(b)
	if err != nil || len(its) != 1 {
		t.Fatal(err)
	}
	n := its[0].Native
	if n["joinUrl"] == "" || n["iCalUId"] != "ical-1" || n["response"] != "organizer" {
		t.Errorf("native %v", n)
	}
	if len(its[0].Participants) != 2 || its[0].Participants[1].Email != "kai.moreno@corp.example" {
		t.Errorf("participants %+v", its[0].Participants)
	}
}

// An event's description loses the join details Teams and Zoom append,
// with line breaks (the body) or without (a preview).
func TestEventText(t *testing.T) {
	for in, want := range map[string]string{
		"Agenda:\r\n- budget\r\n\r\n\r\n\r\n________________________________\r\nMicrosoft Teams meeting\r\nJoin: https://x": "Agenda:\n- budget",
		"Settings this up as agreed. Microsoft Teams meeting Join: https://x Meeting ID: 1":                                 "Settings this up as agreed.",
		"Quarterly review.\nJoin Zoom Meeting\nhttps://zoom.example/j/1":                                                    "Quarterly review.",
		"Just a note": "Just a note",
	} {
		if got := EventText(in); got != want {
			t.Errorf("EventText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := []rune(EventText(strings.Repeat("a", eventTextMax+10))); len(got) != eventTextMax+1 {
		t.Errorf("long description: %d runes", len(got))
	}
}
