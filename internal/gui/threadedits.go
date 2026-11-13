package gui

import (
	"fmt"
	"io"
	"time"

	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/pipeline"
)

// ThreadEditView is a recorded merge or split, for the list with undo.
type ThreadEditView struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	OldName string    `json:"oldName"`
	Moved   int       `json:"moved"`
	At      time.Time `json:"at"`
	Undone  bool      `json:"undone"`
}

// MergeThreads joins from into into; the merged thread's status is
// written again in the background.
func (a *App) MergeThreads(into, from int64) (int64, error) {
	if a.eng == nil {
		return 0, a.notReady()
	}
	id, err := knowledge.MergeThreads(a.eng.Store, into, from, "owner")
	if err == nil {
		a.refreshKnowledge()
	}
	return id, err
}

// SplitThread moves statements out of a thread to a new one with name; it
// returns the edit's id.
func (a *App) SplitThread(from int64, ids []int64, name string) (int64, error) {
	if a.eng == nil {
		return 0, a.notReady()
	}
	_, id, err := knowledge.SplitThread(a.eng.Store, from, ids, name, "owner")
	if err == nil {
		a.refreshKnowledge()
	}
	return id, err
}

// UndoThreadEdit reverses a merge or split.
func (a *App) UndoThreadEdit(id int64) error {
	if a.eng == nil {
		return a.notReady()
	}
	err := knowledge.UndoThreadEdit(a.eng.Store, id)
	if err == nil {
		a.refreshKnowledge()
	}
	return err
}

// ThreadEdits lists the owner's merges and splits, newest first.
func (a *App) ThreadEdits() ([]ThreadEditView, error) {
	if a.eng == nil {
		return nil, a.notReady()
	}
	es, err := a.eng.Store.ThreadEdits(true)
	if err != nil {
		return nil, err
	}
	out := make([]ThreadEditView, 0, len(es))
	for i := len(es) - 1; i >= 0; i-- {
		e := es[i]
		out = append(out, ThreadEditView{ID: e.ID, Kind: e.Kind, Name: e.Name, OldName: e.OldName, Moved: len(e.Moved), At: e.At, Undone: e.Undone})
	}
	return out, nil
}

// refreshKnowledge brings the knowledge up to date in the background
// after an edit: the edited threads' statuses are written again (they
// were dropped). Skipped while an update runs; that one does it.
func (a *App) refreshKnowledge() {
	a.mu.Lock()
	if a.updating {
		a.mu.Unlock()
		return
	}
	a.updating = true
	a.mu.Unlock()
	go func() {
		msg := "thread edited"
		if ks, err := a.eng.Knowledge(a.ctx, io.Discard, pipeline.KnowledgeOptions{}); err != nil {
			msg += "; statuses: " + err.Error()
		} else if ks.Statuses > 0 {
			msg += fmt.Sprintf("; %d statuses rewritten", ks.Statuses)
		}
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
		a.emit("update:done", msg)
	}()
}
