package gui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
)

// PrepTarget is a meeting to prepare for: an upcoming calendar event, or
// the latest of a recent meeting (prepare for its next occurrence).
type PrepTarget struct {
	ItemID   string    `json:"itemId"`
	Title    string    `json:"title"`
	At       time.Time `json:"at"`
	Upcoming bool      `json:"upcoming"`
	People   int       `json:"people"`
}

// PrepTargets lists upcoming events in the next two weeks, then the latest
// occurrence of each meeting held in the last 45 days of the record.
func (a *App) PrepTargets() ([]PrepTarget, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	items, err := a.eng.Store.AllItems()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var latest time.Time
	for _, it := range items {
		if it.Kind != item.KindEvent && it.OccurredAt.After(latest) && !it.OccurredAt.After(now) {
			latest = it.OccurredAt
		}
	}
	var up, past []PrepTarget
	seen := map[string]bool{}
	sort.Slice(items, func(i, j int) bool { return items[i].OccurredAt.After(items[j].OccurredAt) })
	for _, it := range items {
		others := 0
		for _, p := range it.Participants {
			if !p.Owner {
				others++
			}
		}
		if others == 0 {
			continue
		}
		t := PrepTarget{ItemID: it.ID, Title: it.Title, At: it.OccurredAt, People: others}
		switch {
		case it.Kind == item.KindEvent && it.OccurredAt.After(now) && it.OccurredAt.Before(now.Add(14*24*time.Hour)):
			t.Upcoming = true
			up = append(up, t)
		case it.Kind == item.KindMeeting && latest.Sub(it.OccurredAt) < 45*24*time.Hour:
			k := strings.ToLower(strings.TrimSpace(it.Title))
			if !seen[k] {
				seen[k] = true
				past = append(past, t)
			}
		}
	}
	sort.Slice(up, func(i, j int) bool { return up[i].At.Before(up[j].At) })
	return append(up, past...), nil
}

// Prep gathers what to bring to a meeting.
func (a *App) Prep(req knowledge.PrepRequest, scope string) (*knowledge.PrepView, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	items, err := a.eng.Store.AllItems()
	if err != nil {
		return nil, err
	}
	if req.Template != "" {
		// An upcoming event prepares from the last meeting with its title,
		// if there was one, so "since" is when you last met.
		for _, it := range items {
			if it.ID == req.Template && it.Kind == item.KindEvent {
				for _, p := range items {
					if p.Kind != item.KindEvent && strings.EqualFold(p.Title, it.Title) && p.OccurredAt.Before(it.OccurredAt) && p.OccurredAt.After(req.Since) {
						req.Since = p.OccurredAt
					}
				}
			}
		}
	}
	return knowledge.Prep(a.eng.Store, sc, req, items, a.eng.Cfg.Owner.Name)
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
		if err != nil {
			res["error"] = err.Error()
		}
		if ans != nil {
			res["text"], res["sources"], res["threads"] = ans.Text, ans.Sources, ans.Threads
		}
		a.emit("prep:done", res)
	}()
	return nil
}
