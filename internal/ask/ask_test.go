package ask

import (
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/knowledge"
)

// A status's ids become source numbers; ids the scope can't see are
// dropped with their brackets.
func TestStatusText(t *testing.T) {
	visible := map[int64]bool{12: true, 14: true}
	n := 0
	num := map[int64]int{}
	cite := func(id int64) string {
		if !visible[id] {
			return ""
		}
		if num[id] == 0 {
			n++
			num[id] = n
		}
		return "[" + string(rune('0'+num[id])) + "]"
	}
	got := statusText(knowledge.Status{Now: "Target is 40 (12), names due 1 Oct [14, 99]; legal (77).", NowIDs: []int64{12}}, cite)
	want := "Now: Target is 40 [1], names due 1 Oct [2]; legal. [1]\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "77") || strings.Contains(got, "99") {
		t.Error("hidden ids leaked")
	}
}
