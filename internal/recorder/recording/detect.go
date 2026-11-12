package recording

import (
	"fmt"

	"github.com/anielsud/musubi/internal/recorder/meeting"
)

// onMeetingEvent turns the detector's reports into recordings: a call
// that starts is recorded from the configured mic and system audio in the
// engines' default language, and the recording ends with the call. A
// start while recording and a stop while idle are ignored, since the
// detector may report a call the recorder didn't see begin.
func (a *Recorder) onMeetingEvent(ev meeting.MeetingEvent) {
	switch ev.Type {
	case meeting.MeetingStarted:
		if a.Recording() {
			return
		}
		app := string(ev.Platform)
		mic, system := a.nextSources()
		if err := a.StartSession(mic, system, "", app); err != nil {
			fmt.Printf("meeting: %s call detected, but recording didn't start: %v\n", app, err)
			return
		}
		fmt.Printf("meeting: %s call detected, recording\n", app)
	case meeting.MeetingStopped:
		if !a.Recording() {
			return
		}
		if _, err := a.StopSession(); err != nil {
			fmt.Printf("meeting: call ended, but stopping failed: %v\n", err)
			return
		}
		fmt.Println("meeting: call ended, recording stopped")
	}
}
