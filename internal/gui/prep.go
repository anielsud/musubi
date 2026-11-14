package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// PrepTarget is a meeting to prepare for (engine.PrepTarget).
type PrepTarget = engine.PrepTarget

// PrepTargets lists upcoming events in the next two weeks, then the latest
// occurrence of each meeting held in the last 45 days of the record, then
// the events of the last week that have happened (Past).
func (a *App) PrepTargets() ([]PrepTarget, error) {
	if a.eng == nil {
		return nil, a.notReady()
	}
	return a.eng.PrepTargets()
}

// Prep gathers what to bring to a meeting.
func (a *App) Prep(req knowledge.PrepRequest, scope string) (*knowledge.PrepView, error) {
	if a.eng == nil {
		return nil, a.notReady()
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	return a.eng.Prep(a.ctx, req, sc)
}

// PrepBrief writes talking points in the background, streaming
// "prep:token" then "prep:done".
func (a *App) PrepBrief(id string, req knowledge.PrepRequest, scope string) error {
	v, err := a.Prep(req, scope)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	if a.askCancel != nil {
		a.askCancel()
	}
	a.askCancel = cancel
	a.mu.Unlock()
	go func() {
		defer cancel()
		ans, err := ask.PrepBrief(ctx, a.eng, v, func(t string) {
			a.emit("prep:token", map[string]string{"id": id, "text": t})
		})
		res := map[string]any{"id": id}
		switch {
		case ctx.Err() != nil:
			res["stopped"] = true
		case err != nil:
			res["error"] = err.Error()
		}
		if ans != nil {
			res["text"], res["sources"], res["threads"] = ans.Text, ans.Sources, ans.Threads
		}
		a.emit("prep:done", res)
	}()
	return nil
}

// SavedPrepBrief is the talking points drafted ahead of an event
// (prep.auto_brief), nil when there are none or the scope may not see them.
func (a *App) SavedPrepBrief(itemID, scope string) (*store.PrepBrief, error) {
	if a.eng == nil {
		return nil, nil
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	return a.eng.Store.PrepBriefFor(sc, itemID)
}

// briefEvery is how often drafted talking points are checked against what
// they rest on; a change is drafted again at most this often.
const briefEvery = 15 * time.Minute

// draftBriefs keeps talking points drafted for the owner's meetings in the
// rest of the week: every briefEvery, each event whose prep has changed
// since its draft (or has none) is drafted again, one at a time, and
// "prep:brief" names it. Nothing is drafted while a meeting is live, so no
// model runs then (a hosted one can fall back to a local one).
func (a *App) draftBriefs(ctx context.Context) {
	first := time.NewTimer(2 * time.Minute)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	t := time.NewTicker(briefEvery)
	defer t.Stop()
	for {
		a.draftBriefsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) draftBriefsOnce(ctx context.Context) {
	now := time.Now()
	// The rest of the week: to the end of Sunday.
	y, m, d := now.Date()
	end := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 8-int(now.Weekday()+6)%7-1)
	a.draftBriefsUntil(ctx, end, nil)
}

// draftBriefsUntil drafts (or redrafts, when what they rest on changed)
// the talking points of the owner's upcoming events that start before
// end, one at a time, calling visited after each event it has looked at.
// One pass runs at a time (the weekly one, the phone's); nothing is
// drafted while a meeting is live.
func (a *App) draftBriefsUntil(ctx context.Context, end time.Time, visited func(itemID string)) {
	a.briefMu.Lock()
	defer a.briefMu.Unlock()
	e := a.eng
	if e == nil || a.live == nil || a.live.Status().Mode == "live" {
		return
	}
	targets, err := a.PrepTargets()
	if err != nil {
		return
	}
	now := time.Now()
	owner := sensitivity.Owner()
	for _, t := range targets {
		if ctx.Err() != nil || a.live.Status().Mode == "live" {
			return
		}
		if !t.Upcoming || !t.At.After(now) || !t.At.Before(end) {
			continue
		}
		if !a.draftBrief(ctx, e, owner, t) {
			return
		}
		if visited != nil {
			visited(t.ItemID)
		}
	}
}

// draftBrief drafts one event's talking points if they are missing or
// stale; false when the store failed and the pass should stop.
func (a *App) draftBrief(ctx context.Context, e *engine.Engine, owner sensitivity.Scope, t PrepTarget) bool {
	v, err := a.Prep(knowledge.PrepRequest{Template: t.ItemID}, owner.Name)
	if err != nil || v == nil || len(v.Subjects)+len(v.Open)+len(v.Share) == 0 {
		return true
	}
	vb, _ := json.Marshal(v)
	h := sha256.Sum256(append([]byte(e.AskLLM.ModelName()+"\n"), vb...))
	input := hex.EncodeToString(h[:12])
	if have, err := e.Store.PrepBriefFor(owner, t.ItemID); err != nil || have != nil && have.Input == input {
		return true
	}
	ans, err := ask.PrepBrief(ctx, e, v, nil)
	if err != nil || ans == nil || ans.Text == "" {
		if err != nil {
			fmt.Fprintf(os.Stderr, "prep brief %s: %v\n", t.ItemID, err)
		}
		return true
	}
	th, _ := json.Marshal(ans.Threads)
	if err := e.Store.SavePrepBrief(store.PrepBrief{ItemID: t.ItemID, Input: input, Model: e.AskLLM.ModelName(), Text: ans.Text,
		Sources: ans.Sources, Threads: th, MadeAt: time.Now()}); err != nil {
		return false
	}
	a.emit("prep:brief", map[string]string{"id": t.ItemID})
	return true
}
