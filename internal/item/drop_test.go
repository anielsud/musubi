package item

import (
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
