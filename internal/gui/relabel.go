package gui

import (
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/pipeline"
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
