package recording

import (
	"testing"
	"time"
)

func TestDefaultTitle(t *testing.T) {
	at := time.Date(2026, 11, 6, 10, 2, 0, 0, time.Local)
	if got := defaultTitle("Teams", at); got != "Teams Meeting 2026-11-06 10:02" {
		t.Errorf("defaultTitle(Teams) = %q", got)
	}
	if got := defaultTitle("", at); got != "Session 2026-11-06 10:02" {
		t.Errorf("defaultTitle() = %q", got)
	}
	for _, title := range []string{defaultTitle("Teams", at), defaultTitle("Zoom", at), defaultTitle("", at)} {
		if !isDefaultTitle(title) {
			t.Errorf("isDefaultTitle(%q) = false, want true", title)
		}
	}
	for _, title := range []string{
		"", "Planning sync", "Budget review", " Meeting 2026-11-06 10:02", "Meeting 2026-11-06 10:02",
		"Teams Meeting 2026-11-06", "Teams Meeting 2026-13-06 10:02", "Planning sync 2026-11-06 10:02",
		"Teams Meeting 2026-11-06 10:02 notes",
	} {
		if isDefaultTitle(title) {
			t.Errorf("isDefaultTitle(%q) = true, want false", title)
		}
	}
}
