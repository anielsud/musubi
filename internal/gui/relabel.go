package gui

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// Speaker relabels (docs/recorder-screens.md, "Renaming a voice profile
// renames its speakers"): a renamed voice profile's name goes onto the
// labels of the meetings it identified, in the recorder's files and in
// Musubi's copies, with no model calls ([recorder] relabel).

// relabelBatch is a recorder relabel batch (16 hex digits).
var relabelBatch = regexp.MustCompile(`^[0-9a-f]{16}$`)

// beginRelabel refuses while an update or a run is going (it may have
// read a session file before the relabel rewrote it) and holds updates
// off while the relabel runs.
func (a *App) beginRelabel() (func(), error) {
	if err := a.requireEditable(); err != nil {
		return nil, err
	}
	if !a.eng.Cfg.Recorder.Relabel {
		return nil, fmt.Errorf("renaming in meetings is off ([recorder] relabel = false)")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updating || a.running != "" {
		return nil, fmt.Errorf("Musubi is updating; try again once it is done")
	}
	a.updating = true
	return func() {
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
	}, nil
}

func (a *App) afterRelabel() {
	if a.live != nil {
		a.live.Reload()
	}
	a.emit("voices:changed", nil)
	a.emit("meetings:changed", "")
	a.emit("queue:changed", nil)
}

// RelabelProfileMeetings puts voice profile id's current name on the
// speaker labels that still carry one of its old names, in the meetings
// it identified: the recorder's files, then Musubi's copies.
func (a *App) RelabelProfileMeetings(model, id string) (*engine.RelabelResult, error) {
	if model == "" || len(model) > 100 || !validProfileID(id) {
		return nil, fmt.Errorf("a voice profile is needed")
	}
	done, err := a.beginRelabel()
	if err != nil {
		return nil, err
	}
	defer done()
	if err := a.backupBefore("renaming a voice's speakers"); err != nil {
		return nil, err
	}
	res, err := a.eng.RelabelProfile(a.ctx, io.Discard, model, id)
	a.afterRelabel()
	return res, err
}

// UndoRelabel reverses a relabel: the recorder's files, then Musubi's
// copies.
func (a *App) UndoRelabel(batch string) (*engine.RelabelResult, error) {
	if !relabelBatch.MatchString(batch) {
		return nil, fmt.Errorf("not a relabel")
	}
	done, err := a.beginRelabel()
	if err != nil {
		return nil, err
	}
	defer done()
	res, err := a.eng.UndoRelabel(a.ctx, io.Discard, batch, true)
	a.afterRelabel()
	return res, err
}

// relabelRenamedSpeaker takes a speaker renamed on a saved meeting into
// Musubi's copy without re-extracting it, when that's a pure label change
// and nothing is updating; otherwise (or with [recorder] relabel off) the
// meeting is imported again as before. Reports whether it relabelled.
func (a *App) relabelRenamedSpeaker(session string) bool {
	if !a.eng.Cfg.Recorder.Relabel {
		return false
	}
	a.mu.Lock()
	if a.updating || a.running != "" {
		a.mu.Unlock()
		return false
	}
	a.updating = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
	}()
	res, err := a.eng.RelabelMeeting(a.ctx, io.Discard, session, "rename-speaker")
	if err != nil || res == nil || len(res.Meetings) == 0 {
		if err != nil && !errors.Is(err, pipeline.ErrNotLabelOnly) {
			fmt.Printf("relabel %s: %v; importing it again instead\n", session, err)
		}
		return false
	}
	a.afterRelabel()
	return true
}

// ReturnVoiceResult is what "Not them" on a profile's meeting did.
type ReturnVoiceResult struct {
	Recorder *control.ReturnResult `json:"recorder"`
	// Relabelled: the speakers went back to unnamed (Recorder.Changed),
	// in the recorder's file and Musubi's copy. Why, when not, says why
	// the labels were left as they were.
	Relabelled bool                  `json:"relabelled"`
	Why        string                `json:"why,omitempty"`
	Musubi     *engine.RelabelResult `json:"musubi,omitempty"`
}

// ReturnVoiceMeeting is "Not them" on one of a voice profile's meetings
// (docs/recorder-screens.md, "Not them sends the voice back"): the
// recorder takes the meeting out of the profile and, when Musubi can take
// the change in without re-extracting the meeting, puts the speakers the
// profile named there back to unnamed, so they are asked about again;
// Musubi's copy is relabelled to match. When Musubi would have to
// re-extract, the labels stay as they are and Why says so.
func (a *App) ReturnVoiceMeeting(model, id, session string) (*ReturnVoiceResult, error) {
	if err := a.requireEditable(); err != nil {
		return nil, err
	}
	if model == "" || len(model) > 100 || !validProfileID(id) {
		return nil, fmt.Errorf("a voice profile is needed")
	}
	if !validSession(session) {
		return nil, fmt.Errorf("not a session id")
	}
	args := control.ReturnArgs{Model: model, ID: id, Session: session}
	out := &ReturnVoiceResult{}
	if !a.eng.Cfg.Recorder.Relabel {
		var rr control.ReturnResult
		if err := a.recCall(control.OpReturnVoiceMeeting, args, &rr); err != nil {
			return nil, err
		}
		a.emit("voices:changed", nil)
		out.Recorder, out.Why = &rr, "renaming in meetings is off ([recorder] relabel = false)"
		return out, nil
	}
	done, err := a.beginRelabel()
	if err != nil {
		return nil, err
	}
	reimport := false
	defer func() {
		done()
		a.afterRelabel()
		if reimport {
			a.updateSoon()
		}
	}()

	preview := args
	preview.Relabel, preview.Preview = true, true
	var pre control.ReturnResult
	if err := a.recCall(control.OpReturnVoiceMeeting, preview, &pre); err != nil {
		return nil, err
	}
	args.Relabel = len(pre.Changed) > 0
	if args.Relabel {
		// Asked before anything changes: a relabel Musubi refuses would
		// leave the recorder's file and Musubi's copy apart until a
		// re-extraction.
		if err := a.eng.CheckRelabelSession(a.ctx, session, pre.Session); err != nil && !errors.Is(err, sql.ErrNoRows) {
			args.Relabel = false
			out.Why = "Musubi would have to read that meeting again to take the change in, so its labels were left as they were (" + err.Error() + ")"
		}
	}
	var rr control.ReturnResult
	if err := a.recCall(control.OpReturnVoiceMeeting, args, &rr); err != nil {
		return nil, err
	}
	rr.Session = nil
	out.Recorder = &rr
	if !args.Relabel || len(rr.Changed) == 0 {
		return out, nil
	}
	out.Relabelled = true
	res, err := a.eng.RelabelMeeting(a.ctx, io.Discard, session, rr.Batch)
	out.Musubi = res
	if err != nil {
		// Not in Musubi yet, or changed since the check: the next import
		// takes the file in.
		reimport = true
	}
	return out, nil
}
