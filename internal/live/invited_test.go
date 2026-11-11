package live

import (
	"slices"
	"testing"

	"github.com/anielsud/musubi/internal/item"
)

// Who's in the call leaves the owner out, by name or by email.
func TestInvitedLeavesOwnerOut(t *testing.T) {
	got := others([]item.Participant{
		{Name: "Sam Owner", Email: "sam@acme.example"},
		{Name: "Robin Vale", Email: "robin@acme.example"},
		{Name: "S. Owner", Email: "SAM@acme.example"},
		{Name: ""},
	}, "Sam Owner", "sam@acme.example")
	if !slices.Equal(got, []string{"Robin Vale"}) {
		t.Errorf("invited = %v", got)
	}
}
