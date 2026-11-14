package stages

import (
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/item"
)

// The owner's notes are passages of their own, marked as notes.
func TestChunkNotes(t *testing.T) {
	it := item.Item{ID: "tomoe:s1", Segments: []item.Segment{
		{Speaker: "You", Text: "Shall we start?"},
		{Speaker: "You", Text: "Pricing for the badge scanners", From: item.NotesFrom},
		{Speaker: "You", Text: "Floor plan follow-up", From: item.NotesFrom},
	}}
	cs := Chunk(it, "Booth")
	if len(cs) != 2 {
		t.Fatalf("%d passages: %+v", len(cs), cs)
	}
	if strings.Contains(cs[0].Text, "notes") || cs[1].Text != "You: [your notes] Pricing for the badge scanners\nYou: [your notes] Floor plan follow-up" {
		t.Errorf("passages %q / %q", cs[0].Text, cs[1].Text)
	}
}
