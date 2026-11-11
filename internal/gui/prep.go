package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// PrepTarget is a meeting to prepare for: an upcoming calendar event, or
// the latest of a recent meeting (prepare for its next occurrence).
type PrepTarget struct {
	ItemID   string    `json:"itemId"`
	Title    string    `json:"title"`
	At       time.Time `json:"at"`
	Upcoming bool      `json:"upcoming"`
	People   int       `json:"people"`
	// Past marks a calendar event of the last week that has happened, and
	// RecordingID is the recording matched to it, if any: the Meetings tab
	// shows it as happened (its recap once written), not as one to prep.
	Past        bool   `json:"past,omitempty"`
	RecordingID string `json:"recordingId,omitempty"`
}

// PrepTargets lists upcoming events in the next two weeks, then the latest
// occurrence of each meeting held in the last 45 days of the record, then
// the events of the last week that have happened (Past).
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
	recordedAs := map[string]string{}
	if links, err := a.eng.Store.LinksFrom("occurred_as"); err == nil {
		for m, ev := range links {
			if recordedAs[ev] == "" || m < recordedAs[ev] {
				recordedAs[ev] = m
			}
		}
	}
	var up, past, happened []PrepTarget
	seen := map[string]bool{}
	sort.Slice(items, func(i, j int) bool { return items[i].OccurredAt.After(items[j].OccurredAt) })
	for _, it := range items {
		others := 0
		for _, p := range it.Participants {
			if !p.Owner {
				others++
			}
		}
		// Cancelled: nothing to prepare, and it didn't happen.
		if others == 0 || it.Native["cancelled"] == "true" {
			continue
		}
		t := PrepTarget{ItemID: it.ID, Title: it.Title, At: it.OccurredAt, People: others}
		switch {
		case it.Kind == item.KindEvent && it.OccurredAt.After(now) && it.OccurredAt.Before(now.Add(14*24*time.Hour)):
			t.Upcoming = true
			up = append(up, t)
		case it.Kind == item.KindEvent && !it.OccurredAt.After(now) && it.OccurredAt.After(now.Add(-7*24*time.Hour)):
			t.Past, t.RecordingID = true, recordedAs[it.ID]
			happened = append(happened, t)
		case it.Kind == item.KindMeeting && latest.Sub(it.OccurredAt) < 45*24*time.Hour:
			k := strings.ToLower(strings.TrimSpace(it.Title))
			if !seen[k] {
				seen[k] = true
				past = append(past, t)
			}
		}
	}
	sort.Slice(up, func(i, j int) bool { return up[i].At.Before(up[j].At) })
	return append(append(up, past...), happened...), nil
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
	req.QuietAfter, req.MarkPrivate = a.eng.Cfg.Prep.QuietAfter(), a.eng.Cfg.Prep.MarkPrivate
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
	e := a.eng
	if e == nil || a.live == nil || a.live.Status().Mode == "live" {
		return
	}
	targets, err := a.PrepTargets()
	if err != nil {
		return
	}
	now := time.Now()
	// The rest of the week: to the end of Sunday.
	y, m, d := now.Date()
	end := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 8-int(now.Weekday()+6)%7-1)
	owner := sensitivity.Owner()
	for _, t := range targets {
		if ctx.Err() != nil || a.live.Status().Mode == "live" {
			return
		}
		if !t.Upcoming || !t.At.After(now) || !t.At.Before(end) {
			continue
		}
		v, err := a.Prep(knowledge.PrepRequest{Template: t.ItemID}, owner.Name)
		if err != nil || v == nil || len(v.Subjects)+len(v.Open)+len(v.Share) == 0 {
			continue
		}
		vb, _ := json.Marshal(v)
		h := sha256.Sum256(append([]byte(e.AskLLM.ModelName()+"\n"), vb...))
		input := hex.EncodeToString(h[:12])
		if have, err := e.Store.PrepBriefFor(owner, t.ItemID); err != nil || have != nil && have.Input == input {
			continue
		}
		ans, err := ask.PrepBrief(ctx, e, v, nil)
		if err != nil || ans == nil || ans.Text == "" {
			if err != nil {
				fmt.Fprintf(os.Stderr, "prep brief %s: %v\n", t.ItemID, err)
			}
			continue
		}
		th, _ := json.Marshal(ans.Threads)
		if err := e.Store.SavePrepBrief(store.PrepBrief{ItemID: t.ItemID, Input: input, Model: e.AskLLM.ModelName(), Text: ans.Text,
			Sources: ans.Sources, Threads: th, MadeAt: time.Now()}); err != nil {
			return
		}
		a.emit("prep:brief", map[string]string{"id": t.ItemID})
	}
}
