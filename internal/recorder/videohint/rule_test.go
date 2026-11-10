package videohint

import (
	"testing"

	"github.com/anielsud/musubi/internal/recorder/meeting"
)

// Teams is calibrated end to end: every stage of its rule must be set,
// and its chrome marker must be the one chrome_test.go checks frames
// against, or those tests would pass on a calibration nobody runs.
func TestTeamsRuleCoversEveryStage(t *testing.T) {
	r, ok := ruleFor(meeting.PlatformTeams)
	switch {
	case !ok:
		t.Fatal("no rule for Teams")
	case !r.Chrome.configured():
		t.Error("Teams rule has no chrome marker")
	case !r.Ring.configured():
		t.Error("Teams rule has no ring")
	case !r.Label.configured():
		t.Error("Teams rule has no label region")
	}
	if want := teamsChromeMarker(); r.Chrome != want {
		t.Errorf("Teams chrome marker %+v differs from chrome_test.go's %+v; change both together", r.Chrome, want)
	}
}

// Platforms without measured calibration get no rule at all, so a guess
// never names anyone; the zero rule parts must read as unset.
func TestUncalibratedPlatformsHaveNoRule(t *testing.T) {
	for _, p := range []meeting.Platform{meeting.PlatformZoom, meeting.PlatformMeet, meeting.PlatformUnknown} {
		if _, ok := ruleFor(p); ok {
			t.Errorf("%s has a rule but no calibration data", p)
		}
	}
	var zero Rule
	if zero.Ring.configured() || zero.Label.configured() {
		t.Error("zero RingConfig/LabelRegion should read as unconfigured")
	}
}
