package recording

import (
	"sync"

	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// emitSessionSegments puts a coordinator's lines (segments) and their
// pass-2 revisions (updates) into sess as they arrive, and closes the
// returned channel when both channels have been drained.
//
// The session is passed in rather than read from a.currentSess because
// revisions keep coming after StopSession has cleared it: they belong
// to the session being saved (persistSession waits for the channel), and
// once another recording starts its segment IDs begin at seg-1 again, so
// going by the current session would put them in the wrong meeting.
func (a *Recorder) emitSessionSegments(segments, updates <-chan session.Segment, sess *session.Session, md *diarize.SessionDiarizer) <-chan struct{} {
	finished := make(chan struct{})
	var drains sync.WaitGroup
	drain := func(ch <-chan session.Segment) {
		defer drains.Done()
		for seg := range ch {
			a.applySegment(sess, md, seg)
		}
	}
	drains.Add(2)
	go drain(segments)
	go drain(updates) // carries nothing unless two-pass is on
	go func() {
		drains.Wait()
		close(finished)
	}()
	return finished
}

// applySegment adds seg to sess, or replaces the version sess has (a line
// and its revisions come on different channels, in either order), and
// sends the result to the live feed. A session diarized during the
// meeting (md non-nil) relabels seg from its latest timeline first.
func (a *Recorder) applySegment(sess *session.Session, md *diarize.SessionDiarizer, seg session.Segment) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if md != nil {
		md.LabelNewLocked(&seg)
	}
	sess.UpsertSegment(seg)
	// Unlike the frontend, the live feed gets a stopped session's late
	// refinements too: its messages carry the session ID, and a consumer
	// wants the final text of the meeting that just ended.
	a.feedSegmentsLocked(sess, seg)
}
