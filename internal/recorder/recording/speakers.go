package recording

import (
	"fmt"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// RenameSpeaker names a speaker in a session's transcript ("" for the
// current recording). While recording with diarization during the
// meeting, the name sticks to that voice, beating any name the meeting
// window suggests; otherwise every line labeled label is renamed.
func (a *Recorder) RenameSpeaker(id, label, name string) error {
	if name == "" || label == "" {
		return fmt.Errorf("a speaker and a name are needed")
	}
	a.unforgetVoice(name)
	if label == "You" {
		return fmt.Errorf("the microphone is always \"You\"")
	}
	a.mu.Lock()
	sess := a.currentSess
	if sess != nil && (id == "" || id == sess.ID) {
		var changed []session.Segment
		if md := a.meetingDiar; md != nil {
			if c, ok := md.RenameLocked(label, name); ok {
				changed = c
			}
		}
		if changed == nil {
			changed = renameLines(sess, label, name)
		}
		a.feedSegmentsLocked(sess, changed...)
		a.feedDiagsLocked(sess, diagRename, changed...)
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	saved, err := a.editable(id)
	if err != nil {
		return err
	}
	if len(renameLines(saved, label, name)) == 0 {
		return fmt.Errorf("no lines labeled %q", label)
	}
	a.correctVoiceName(saved, label, name)
	dropVoiceDoubt(saved, label)
	dropReturned(saved, label)
	if err := a.saveSession(saved); err != nil {
		return err
	}
	a.learnNamedLater(saved)
	return nil
}

// renameLines relabels sess's lines labeled label, keeping their live
// label, and returns them.
func renameLines(sess *session.Session, label, name string) []session.Segment {
	var changed []session.Segment
	for i := range sess.Segments {
		seg := &sess.Segments[i]
		if seg.Speaker == label {
			seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), name
			seg.VoiceMatch = nil
			changed = append(changed, *seg)
		}
	}
	return changed
}

// editVoice carries out one voice-profile edit from the control socket.
func (a *Recorder) editVoice(op string, p control.VoiceArgs) error {
	switch op {
	case control.OpRenameVoice:
		return a.RenameVoice(p.Model, p.ID, p.Name)
	case control.OpMergeVoices:
		return a.MergeVoices(p.Model, p.ID, p.Other)
	case control.OpKeepApart:
		return a.KeepVoicesApart(p.Model, p.ID, p.Other)
	case control.OpForgetVoice:
		return a.ForgetVoice(p.Model, p.ID)
	case control.OpDeleteVoice:
		return a.DeleteVoice(p.Model, p.ID)
	case control.OpAllowName:
		return a.AllowName(p.Model, p.Name)
	case control.OpStartOver:
		return a.StartVoiceOver(p.Model, p.ID)
	case control.OpConfirmVoice:
		return a.ConfirmVoice(p.Model, p.ID)
	case control.OpRemoveVoiceMeeting:
		return a.RemoveVoiceMeeting(p.Model, p.ID, p.Session)
	case control.OpConfirmVoiceMeeting:
		return a.ConfirmVoiceMeeting(p.Model, p.ID, p.Session)
	case control.OpMoveVoiceMeeting:
		return a.MoveVoiceMeeting(p.Model, p.ID, p.Session, p.To, p.Name)
	}
	return fmt.Errorf("%w %q", control.ErrUnknownOp, op)
}

// learnSaved is LearnVoicesFromSavedSessions with Status.Busy set while
// it runs (it reads every saved meeting's fingerprints), one at a time.
func (a *Recorder) learnSaved() (int, error) {
	const what = "Learning voices from saved meetings"
	if !a.learnMu.TryLock() {
		return 0, fmt.Errorf("already learning voices")
	}
	defer a.learnMu.Unlock()
	a.setBusy(what)
	defer a.clearBusy(what)
	return a.LearnVoicesFromSavedSessions()
}
