package engine

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
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
func (e *Engine) PrepTargets() ([]PrepTarget, error) {
	items, err := e.Store.AllItems()
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
	if links, err := e.Store.LinksFrom("occurred_as"); err == nil {
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
// The context bounds the search for the threads an event is about.
func (e *Engine) Prep(ctx context.Context, req knowledge.PrepRequest, sc sensitivity.Scope) (*knowledge.PrepView, error) {
	items, err := e.Store.AllItems()
	if err != nil {
		return nil, err
	}
	if req.Template != "" {
		// An upcoming event prepares from the last meeting of its series, so
		// "since" is when you last met and that meeting's recap anchors the
		// prep: the latest earlier event with the same subject that has a
		// recording (found through the calendar, as a recording's own title
		// is often Tomoe's default), else a recording titled the same.
		for _, it := range items {
			if it.ID == req.Template && it.Kind == item.KindEvent {
				if last, at := knowledge.LastOfSeries(e.Store, it, items); last != "" {
					req.Since, req.Last = at, last
					break
				}
				for _, p := range items {
					if p.Kind != item.KindEvent && strings.EqualFold(p.Title, it.Title) && p.OccurredAt.Before(it.OccurredAt) && p.OccurredAt.After(req.Since) {
						req.Since = p.OccurredAt
					}
				}
			}
		}
	}
	req.QuietAfter, req.MarkPrivate = e.Cfg.Prep.QuietAfter(), e.Cfg.Prep.MarkPrivate
	if req.About, err = e.prepAbout(ctx, sc, req.Template, items); err != nil {
		return nil, err
	}
	return knowledge.Prep(e.Store, sc, req, items, e.Cfg.Owner.Name)
}

// prepAbout finds the threads an event's title and description are about.
func (e *Engine) prepAbout(ctx context.Context, sc sensitivity.Scope, id string, items []item.Item) ([]int64, error) {
	for _, it := range items {
		if it.ID != id || it.Kind != item.KindEvent {
			continue
		}
		ms, err := e.ThreadsAbout(ctx, sc, knowledge.MeetingQuery(it, e.Cfg.Owner.Name, e.Cfg.Owner.InternalDomains))
		var ids []int64
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
		return ids, err
	}
	return nil, nil
}
