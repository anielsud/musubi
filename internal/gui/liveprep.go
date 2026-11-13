package gui

import (
	"context"
	"fmt"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/live"
	"github.com/anielsud/musubi/internal/store"
)

// LivePrep is the prep for the meeting under way, for the Live screen's
// prep card (live.prep_card): the talking points drafted ahead for its
// calendar event, if any, and the prep view the Prep tab shows.
type LivePrep struct {
	SessionID string              `json:"sessionId"`
	EventID   string              `json:"eventId"`
	Scope     string              `json:"scope"`
	View      *knowledge.PrepView `json:"view"`
	Brief     *store.PrepBrief    `json:"brief,omitempty"`
}

// LivePrep loads the prep for the live session's calendar event, read in
// the panel's scope (the screen-safe toggle) rather than one the screen
// passes, so a shared screen never gets what the Prep tab would hide in
// that scope. nil without a meeting, without a matched event, or with
// live.prep_card off. No model writes anything here.
func (a *App) LivePrep() (*LivePrep, error) {
	if a.eng == nil || a.live == nil || !a.eng.Cfg.Live.PrepCard {
		return nil, nil
	}
	st := a.live.Status()
	if st.Session == nil || st.Mode == "idle" {
		return nil, nil
	}
	return livePrep(a.ctx, a.eng, *st.Session, st.Scope)
}

// LivePrepBrief drafts talking points for the live session's event on
// the Prep tab's path (PrepBrief: it streams "prep:token" then
// "prep:done" under id, and a hosted model needs knowledge.allow_hosted),
// in the panel's scope. Only ever started by the owner's click.
func (a *App) LivePrepBrief(id string) error {
	if a.eng == nil || a.live == nil {
		return a.notReady()
	}
	if !a.eng.Cfg.Live.PrepCard {
		return fmt.Errorf("the prep card is off (live.prep_card)")
	}
	st := a.live.Status()
	if st.Session == nil || st.Session.EventID == "" {
		return fmt.Errorf("no calendar event matched to this meeting")
	}
	return a.PrepBrief(id, knowledge.PrepRequest{Template: st.Session.EventID}, st.Scope)
}

func livePrep(ctx context.Context, e *engine.Engine, s live.Session, scope string) (*LivePrep, error) {
	if s.EventID == "" {
		return nil, nil
	}
	sc, err := e.Scope(scope)
	if err != nil {
		return nil, err
	}
	v, err := e.Prep(ctx, knowledge.PrepRequest{Template: s.EventID}, sc)
	if err != nil {
		return nil, err
	}
	b, err := e.Store.PrepBriefFor(sc, s.EventID)
	if err != nil {
		return nil, err
	}
	return &LivePrep{SessionID: s.ID, EventID: s.EventID, Scope: scope, View: v, Brief: b}, nil
}
