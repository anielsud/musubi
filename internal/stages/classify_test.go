package stages

import (
	"slices"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/registry"
)

// A recording that runs on after most invitees have left must not be
// opened to them by the invite list.
func TestClassifyAudienceFromSpeakers(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Owner = config.Owner{Email: "me@northwind.example", Name: "Sam Owner", InternalDomains: []string{"northwind.example"}}
	event := item.Item{ID: "ev", Kind: item.KindEvent, Title: "Partner checkpoint", Participants: []item.Participant{
		{Name: "Sam Owner", Email: "me@northwind.example", Role: "organizer", Owner: true},
		{Name: "Rita Vance", Email: "rita@northwind.example", Role: "attendee"},
		{Name: "Omar Diaz", Email: "omar@northwind.example", Role: "attendee"},
		{Name: "Lena Hart", Email: "lena@northwind.example", Role: "attendee"},
	}}
	rec := item.Item{ID: "rec", Kind: item.KindMeeting, Participants: []item.Participant{
		{Name: "You", Role: "speaker", Owner: true},
		{Name: "Rita Vance", Role: "speaker"},
	}}
	reg, err := registry.Build(cfg, "", []item.Item{event, rec})
	if err != nil {
		t.Fatal(err)
	}
	internal := cfg.Owner.InternalDomains

	lblOn, _, on := Classify(rec, &event, nil, reg, internal, 15, true)
	if slices.Contains(on, "omar@northwind.example") || slices.Contains(on, "lena@northwind.example") {
		t.Errorf("silent invitees in the audience: %v", on)
	}
	if !slices.Contains(on, "rita@northwind.example") || !slices.Contains(on, "me@northwind.example") {
		t.Errorf("speaker or owner missing from the audience: %v", on)
	}

	lblOff, _, off := Classify(rec, &event, nil, reg, internal, 15, false)
	if !slices.Contains(off, "lena@northwind.example") {
		t.Errorf("switch off should restore invitees in the audience: %v", off)
	}
	// The level still reflects the meeting as held, so the switch only
	// changes who may receive it.
	if lblOn.Level != lblOff.Level {
		t.Errorf("level changed with the switch: %v vs %v", lblOn.Level, lblOff.Level)
	}
}
