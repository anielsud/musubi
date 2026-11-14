package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/source/tomoe"
)

// Speaker relabels (docs/recorder-screens.md, "Renaming a voice profile
// renames its speakers"): the recorder rewrites its session files, then
// Musubi rewrites its copies with no model calls, then the local
// deterministic steps (registry, links, classify, resolve) take in the new
// speakers as on any import.

// RelabelResult is a relabel across meetings: the recorder's part and
// Musubi's, meeting by meeting.
type RelabelResult struct {
	Batch    string                    `json:"batch"`
	Recorder *control.RelabelResult    `json:"recorder,omitempty"`
	Meetings []*pipeline.RelabelReport `json:"meetings"`
	// Skipped are meetings Musubi couldn't relabel cheaply, and why; a
	// normal update takes them in (re-extracting them).
	Skipped map[string]string `json:"skipped,omitempty"`
	// Refresh is the deterministic pass after: what ran, or its error.
	Refresh string `json:"refresh,omitempty"`
}

// recorderCall asks the recorder; relabels read every session, so they
// get longer than the default 30 s.
func (e *Engine) recorderCall(ctx context.Context, op string, args, out any) error {
	c := control.NewClient(e.Cfg.RecorderControlSocket())
	c.Timeout = 2 * time.Minute
	return c.Call(ctx, op, args, out)
}

// recorderSession reads a session file of the recorder's; id must name a
// folder directly in its sessions folder.
func (e *Engine) recorderSession(id string) ([]byte, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("not a session id: %q", id)
	}
	dir := filepath.Join(e.Cfg.RecorderSessions(), id)
	b, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil || !e.Cfg.Phone.Details {
		return b, err
	}
	// As ingest reads it: a phone recording's details come with it.
	return tomoe.WithDetails(b, dir)
}

// RelabelProfile has the recorder put voice profile id's name on the
// labels of the meetings it identified, then relabels Musubi's copies of
// each.
func (e *Engine) RelabelProfile(ctx context.Context, log io.Writer, model, id string) (*RelabelResult, error) {
	var rr control.RelabelResult
	if err := e.recorderCall(ctx, control.OpRelabelProfileMeetings, control.RelabelArgs{Model: model, ID: id}, &rr); err != nil {
		return nil, err
	}
	res := &RelabelResult{Batch: rr.Batch, Recorder: &rr}
	seen := map[string]bool{}
	var reqs []pipeline.RelabelRequest
	for _, c := range rr.Changed {
		if seen[c.Session] {
			continue
		}
		seen[c.Session] = true
		b, err := e.recorderSession(c.Session)
		if err != nil {
			return res, err
		}
		reqs = append(reqs, pipeline.RelabelRequest{ItemID: tomoe.Name + ":" + c.Session, Session: b, Batch: rr.Batch})
	}
	return res, e.relabel(ctx, log, res, reqs)
}

// RelabelMeeting relabels Musubi's copy of one recording from its
// rewritten session file (a speaker renamed on a saved meeting). It
// returns pipeline.ErrNotLabelOnly when only a re-extraction can take the
// change in.
func (e *Engine) RelabelMeeting(ctx context.Context, log io.Writer, sessionID, batch string) (*RelabelResult, error) {
	b, err := e.recorderSession(sessionID)
	if err != nil {
		return nil, err
	}
	res := &RelabelResult{Batch: batch}
	if err := e.relabel(ctx, log, res, []pipeline.RelabelRequest{{ItemID: tomoe.Name + ":" + sessionID, Session: b, Batch: batch}}); err != nil {
		return res, err
	}
	for _, why := range res.Skipped {
		return res, fmt.Errorf("%w: %s", pipeline.ErrNotLabelOnly, why)
	}
	return res, nil
}

// CheckRelabelSession reports whether Musubi could take sessionJSON, a
// rewrite of recording sessionID, in as a pure relabel, writing nothing:
// nil if so; an error wrapping sql.ErrNoRows when Musubi doesn't have the
// meeting yet (its next import takes the file as it is), else one saying
// why not (pipeline.ErrNotLabelOnly: only a re-extraction could).
func (e *Engine) CheckRelabelSession(ctx context.Context, sessionID string, sessionJSON []byte) error {
	if sessionID == "" || sessionID == "." || sessionID == ".." || filepath.Base(sessionID) != sessionID || strings.ContainsAny(sessionID, `/\`) {
		return fmt.Errorf("not a session id: %q", sessionID)
	}
	r, err := e.Runner(nil, io.Discard)
	if err != nil {
		return err
	}
	if e.Cfg.Phone.Details {
		if sessionJSON, err = tomoe.WithDetails(sessionJSON, filepath.Join(e.Cfg.RecorderSessions(), sessionID)); err != nil {
			return err
		}
	}
	_, err = r.RelabelSpeaker(ctx, pipeline.RelabelRequest{ItemID: tomoe.Name + ":" + sessionID, Session: sessionJSON, Check: true})
	return err
}

// RelabelByName relabels Musubi's copy of one item by speaker name (no
// session file: the CLI's way, and for meetings imported from elsewhere).
func (e *Engine) RelabelByName(ctx context.Context, log io.Writer, itemID, old, newName, batch string) (*RelabelResult, error) {
	res := &RelabelResult{Batch: batch}
	return res, e.relabel(ctx, log, res, []pipeline.RelabelRequest{{ItemID: itemID, Old: old, New: newName, Batch: batch}})
}

// RelabelSession relabels Musubi's copy of one item from a given session
// file (the CLI's way, for a file the recorder already rewrote).
func (e *Engine) RelabelSession(ctx context.Context, log io.Writer, itemID string, sessionJSON []byte, batch string) (*RelabelResult, error) {
	res := &RelabelResult{Batch: batch}
	return res, e.relabel(ctx, log, res, []pipeline.RelabelRequest{{ItemID: itemID, Session: sessionJSON, Batch: batch}})
}

func (e *Engine) relabel(ctx context.Context, log io.Writer, res *RelabelResult, reqs []pipeline.RelabelRequest) error {
	res.Meetings = []*pipeline.RelabelReport{}
	if len(reqs) == 0 {
		return nil
	}
	e.updating.Lock()
	defer e.updating.Unlock()
	unlock, err := lockFile(filepath.Join(e.Cfg.DataDir, "knowledge.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	r, err := e.Runner(nil, log)
	if err != nil {
		return err
	}
	for _, q := range reqs {
		rep, err := r.RelabelSpeaker(ctx, q)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res.skip(q.ItemID, "not in Musubi yet: the next update imports it with the new name")
		case err != nil:
			res.skip(q.ItemID, err.Error())
		default:
			res.Meetings = append(res.Meetings, rep)
		}
	}
	res.refresh(ctx, r)
	return nil
}

func (res *RelabelResult) skip(itemID, why string) {
	if res.Skipped == nil {
		res.Skipped = map[string]string{}
	}
	res.Skipped[itemID] = why
}

// refresh runs the deterministic steps after a relabel: the registry
// (speakers are people), links, classify and resolve read the new names.
// No model is called; passages are embedded locally where still needed.
func (res *RelabelResult) refresh(ctx context.Context, r *pipeline.Runner) {
	if len(res.Meetings) == 0 {
		return
	}
	var st pipeline.Stats
	if err := r.Process(ctx, &st); err != nil {
		res.Refresh = "left for the next update: " + err.Error()
		return
	}
	res.Refresh = fmt.Sprint(st.Ran)
}

// UndoRelabel reverses a batch: the recorder's session files first (if it
// relabelled any), then Musubi's copies.
func (e *Engine) UndoRelabel(ctx context.Context, log io.Writer, batch string, recorder bool) (*RelabelResult, error) {
	res := &RelabelResult{Batch: batch, Meetings: []*pipeline.RelabelReport{}}
	if recorder {
		var rr control.RelabelResult
		if err := e.recorderCall(ctx, control.OpUndoRelabel, control.RelabelArgs{Batch: batch}, &rr); err != nil {
			return nil, err
		}
		res.Recorder = &rr
	}
	e.updating.Lock()
	defer e.updating.Unlock()
	unlock, err := lockFile(filepath.Join(e.Cfg.DataDir, "knowledge.lock"))
	if err != nil {
		return res, err
	}
	defer unlock()
	r, err := e.Runner(nil, log)
	if err != nil {
		return res, err
	}
	ids, err := e.Store.RelabelBatchItems(batch)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		rep, err := r.UndoRelabel(ctx, batch, id)
		if err != nil {
			res.skip(id, err.Error())
			continue
		}
		res.Meetings = append(res.Meetings, rep)
	}
	res.refresh(ctx, r)
	return res, nil
}
