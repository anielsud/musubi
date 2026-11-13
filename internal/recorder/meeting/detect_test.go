package meeting

import (
	"testing"
	"time"
)

func TestTitlePlatforms(t *testing.T) {
	cases := map[string]Platform{
		"Weekly sync | Microsoft Teams - Google Chrome": PlatformTeams,
		"MICROSOFT TEAMS":                     PlatformTeams,
		"Meet - abc-defg-hij - Firefox":       PlatformMeet,
		"https://meet.google.com/xyz - Brave": PlatformMeet,
		"Zoom Workplace":                      PlatformZoom,
		"Webex":                               PlatformWebex,
		"Slack | huddle":                      PlatformSlack,
		"Inbox - Google Chrome":               PlatformUnknown,
		"Meeting notes":                       PlatformUnknown,
		"":                                    PlatformUnknown,
	}
	for title, want := range cases {
		if got := matchPlatformFromTitle(title); got != want {
			t.Errorf("matchPlatformFromTitle(%q) = %s, want %s", title, got, want)
		}
		if got := PlatformFromTitle(title); got != want {
			t.Errorf("PlatformFromTitle(%q) = %s, want %s", title, got, want)
		}
	}
}

func TestNativeAppsNeedNoTitle(t *testing.T) {
	// PID 0 owns no windows, so only the app-name table can answer.
	for name, want := range map[string]Platform{
		"zoom": PlatformZoom, "ZOOM VoiceEngine": PlatformZoom,
		"Slack": PlatformSlack, "slack": PlatformSlack,
		"Music": PlatformUnknown,
	} {
		if got := identifyPlatform(name, 0); got != want {
			t.Errorf("identifyPlatform(%q, 0) = %s, want %s", name, got, want)
		}
	}
}

func TestBrowserTable(t *testing.T) {
	for _, b := range []string{"Google Chrome", "Chromium input", "Firefox", "Brave Browser input", "Microsoft Edge"} {
		if !knownBrowserBinaries[b] {
			t.Errorf("%q not treated as a browser", b)
		}
	}
	for _, n := range []string{"zoom", "Music", "Terminal"} {
		if knownBrowserBinaries[n] {
			t.Errorf("%q treated as a browser", n)
		}
	}
}

func TestFirstCaller(t *testing.T) {
	mics := []streamInfo{
		{AppName: "Recorder", PID: 5},
		{AppName: "Google Chrome input", PID: 0},
		{AppName: "Google Chrome input", PID: 7},
		{AppName: "zoom", PID: 9},
	}
	speakers := []streamInfo{
		{AppName: "Google Chrome", PID: 0},
		{AppName: "zoom", PID: 9},
		{AppName: "Google Chrome", PID: 7},
	}
	got, ok := firstCaller(mics, speakers)
	if !ok || got.PID != 7 || got.AppName != "Google Chrome input" {
		t.Errorf("firstCaller = %+v, %v; want the mic stream of PID 7", got, ok)
	}

	// Mic in one app and playback in another is not a call, and PID 0
	// (unknown owner) never pairs.
	if got, ok := firstCaller(mics[:2], speakers[:2]); ok {
		t.Errorf("firstCaller found %+v, want none", got)
	}
	if _, ok := firstCaller(nil, speakers); ok {
		t.Error("firstCaller with no mic streams found a call")
	}
}

func TestDetectorBasics(t *testing.T) {
	d := NewDetector()
	if cap(d.Events()) != 4 {
		t.Errorf("event buffer = %d, want 4", cap(d.Events()))
	}
	if debounceDelay != 2*time.Second || healthCheckInterval != 30*time.Second {
		t.Errorf("timings = %v / %v, want 2s / 30s", debounceDelay, healthCheckInterval)
	}
	if MeetingStarted != 0 || MeetingStopped != 1 {
		t.Error("event type values changed")
	}
}

func TestChecksReturnEarly(t *testing.T) {
	// With a call already tracked, a start check must not touch the
	// candidate; with none, an end check must not emit anything.
	d := NewDetector()
	d.current = &callInProgress{pid: 42, platform: PlatformZoom}
	d.checkForMeeting()
	if d.candidatePID != 0 || d.current.pid != 42 {
		t.Errorf("start check with a call tracked changed state: candidate %d, current %+v", d.candidatePID, d.current)
	}

	d = NewDetector()
	d.checkForMeetingEnd()
	select {
	case ev := <-d.Events():
		t.Errorf("end check with no call emitted %+v", ev)
	default:
	}

	d = NewDetector()
	d.stopped = true
	d.candidatePID = 3
	d.checkForMeeting()
	if d.candidatePID != 3 {
		t.Error("start check ran on a stopped detector")
	}
}

func TestEmitDropsWhenFull(t *testing.T) {
	d := NewDetector()
	for i := 0; i < cap(d.events)+2; i++ {
		d.emit(MeetingEvent{Type: MeetingStarted, Platform: PlatformMeet})
	}
	if len(d.events) != cap(d.events) {
		t.Errorf("buffered %d events, want %d", len(d.events), cap(d.events))
	}
}
