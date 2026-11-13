package item

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/anielsud/musubi/internal/source"
)

func translateSession(t *testing.T, speakers []string) Item {
	t.Helper()
	type seg struct {
		Speaker string  `json:"speaker"`
		Text    string  `json:"text"`
		Start   float64 `json:"start_time"`
		End     float64 `json:"end_time"`
	}
	s := struct {
		Title     string `json:"title"`
		CreatedAt string `json:"created_at"`
		Duration  float64
		Segments  []seg `json:"segments"`
	}{Title: "Sync", CreatedAt: "2026-03-04T10:00:00Z", Duration: 60}
	for i, sp := range speakers {
		s.Segments = append(s.Segments, seg{sp, fmt.Sprintf("words %d", i), float64(i), float64(i) + 1})
	}
	b, _ := json.Marshal(s)
	its, err := Translate(source.Record{Source: "tomoe", Ref: "r", ContentType: TomoeContentType, Data: b}, "h")
	if err != nil {
		t.Fatal(err)
	}
	return its[0]
}

// Renaming by name gives what translating the relabelled file gives.
func TestRenameSpeakerMatchesTranslation(t *testing.T) {
	for _, c := range []struct {
		before, after []string
		old, new      string
	}{
		{[]string{"You", "Person 3 (Corner Tile)", "Person 4 (Bo)", "Person 3 (Corner Tile)"}, []string{"You", "Person 3 (Ana Example)", "Person 4 (Bo)", "Person 3 (Ana Example)"}, "Corner Tile", "Ana Example"},
		{[]string{"Person 10", "You", "Person 3 (Bo)"}, []string{"Jack Example", "You", "Person 3 (Bo)"}, "Person 10", "Jack Example"},
		{[]string{"Person 1 (Corner Tile)", "Person 2 (Bo)", "Person 3 (Corner Tile)"}, []string{"Person 1 (Bo)", "Person 2 (Bo)", "Person 3 (Bo)"}, "Corner Tile", "Bo"},
	} {
		got, ok := RenameSpeaker(translateSession(t, c.before), c.old, c.new)
		want := translateSession(t, c.after)
		if !ok || got.ContentHash() != want.ContentHash() || !reflect.DeepEqual(got.Participants, want.Participants) {
			t.Errorf("%q → %q:\n got %+v %v\nwant %+v %v", c.old, c.new, got.Participants, got.Native, want.Participants, want.Native)
		}
	}
	if _, ok := RenameSpeaker(translateSession(t, []string{"Person 1 (Ana)"}), "Ana", "Board Room"); ok {
		t.Error("renaming to a room was allowed")
	}
	if _, ok := RenameSpeaker(translateSession(t, []string{"Person 1 (Ana)"}), "Nobody", "Ana Example"); ok {
		t.Error("renaming a speaker with no lines was allowed")
	}
}
