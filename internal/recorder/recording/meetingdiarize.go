package recording

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// newMeetingDiarizer starts diarizing the session about to record while it
// records (see diarize.SessionDiarizer), sending relabeled lines to the
// transcript, or returns nil (logging why, if it was asked for) so the
// post-meeting pass runs as before.
func newMeetingDiarizer(a *Recorder, cfg *config.Config, status *models.Status, lang string, onSpeakerChange func(at float64)) *diarize.SessionDiarizer {
	var d *diarize.SessionDiarizer
	d, err := diarize.NewSessionDiarizer(cfg.Meeting, status, lang, &a.mu, diarize.SessionOptions{
		OnChanged: func(changed []session.Segment) {
			a.mu.Lock()
			visible := a.meetingDiar == d
			if visible && a.currentSess != nil {
				// Sends each line as the session holds it now, which a
				// revision applied since this recluster may have changed.
				a.feedSegmentsLocked(a.currentSess, changed...)
				a.feedDiagsLocked(a.currentSess, diagRelabel, changed...)
			}
			a.mu.Unlock()
		},
		OnSpeakerChange: onSpeakerChange,
	})
	if err != nil {
		fmt.Printf("diarize during meeting: %v; diarizing after the meeting instead\n", err)
		return nil
	}
	if d != nil {
		fmt.Printf("diarizing during the meeting (%s)\n", d.Describe())
	}
	return d
}

// finishMeetingDiarizer applies d's final labels to sess, reporting
// whether that worked (if not, the post-meeting pass should run).
func finishMeetingDiarizer(d *diarize.SessionDiarizer, sess *session.Session) bool {
	if err := d.Finish(filepath.Join(config.SessionDir(), sess.ID)); err != nil {
		fmt.Printf("session %s: diarizing during the meeting failed: %v\n", sess.ID, err)
		return false
	}
	return true
}

// refineCutsLater moves the saved session's speaker-change cuts to where
// the voice changes (MeetingConfig.RefineSpeakerCuts), on a low-priority
// thread, and saves it again unless it changed meanwhile.
func (a *Recorder) refineCutsLater(id string) {
	a.mu.Lock()
	on := a.cfg != nil && a.cfg.Meeting.RefineSpeakerCuts
	a.mu.Unlock()
	if !on {
		return
	}
	go func() {
		began := time.Now()
		sess, err := a.store.Load(id)
		if err != nil {
			return
		}
		snapshot := sessionSnapshot(sess)
		n, err := diarize.RefineSavedCuts(filepath.Join(config.SessionDir(), id), sess, diarize.DefaultRefineParams())
		if err != nil {
			fmt.Printf("session %s: speaker cuts not refined: %v\n", id, err)
			return
		}
		if cur, err := a.store.Load(id); err != nil || sessionSnapshot(cur) != snapshot {
			fmt.Printf("session %s: changed while refining speaker cuts; left as it was\n", id)
			return
		}
		if n == 0 {
			return
		}
		if err := a.saveSession(sess); err != nil {
			fmt.Printf("session %s: couldn't save refined speaker cuts: %v\n", id, err)
			return
		}
		fmt.Printf("session %s: %d speaker cuts moved to where the voice changes (%.0fs)\n", id, n, time.Since(began).Seconds())
	}()
}

// sessionSnapshot is enough of a session to tell whether the user changed
// it (renamed a speaker, edited the title).
func sessionSnapshot(s *session.Session) string {
	var b strings.Builder
	b.WriteString(s.Title)
	for _, seg := range s.Segments {
		b.WriteString("\x00" + seg.Speaker)
	}
	return b.String()
}
