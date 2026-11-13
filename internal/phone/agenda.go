package phone

import (
	"sort"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// The agenda is what the phone carries to the meetings: the owner's
// events and the briefs drafted for them, read in the owner's scope (the
// phone is the owner's), each brief with its label so the phone shows it.

const (
	defaultDays = 10
	maxDays     = 14
)

// Agenda is GET /v1/agenda's reply.
type Agenda struct {
	Events   []Event `json:"events"`
	Drafting int     `json:"drafting"`
}

type Event struct {
	ID        string     `json:"id"`
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	Subject   string     `json:"subject"`
	Location  string     `json:"location"`
	Organizer string     `json:"organizer"`
	Attendees []Attendee `json:"attendees"`
	Brief     *Brief     `json:"brief"`
}

type Attendee struct {
	Name string `json:"name"`
}

type Brief struct {
	Text        string      `json:"text"`
	MadeAt      time.Time   `json:"made_at"`
	Sensitivity Sensitivity `json:"sensitivity"`
}

// Sensitivity is a brief's label as the MCP gives it.
type Sensitivity struct {
	Level string            `json:"level"`
	Share sensitivity.Share `json:"share"`
	Why   string            `json:"why"`
}

// AgendaStore is what the agenda reads: *store.Store.
type AgendaStore interface {
	AllItems() ([]item.Item, error)
	ItemLabel(itemID string) (sensitivity.Label, []string, error)
	PrepBriefFor(sc sensitivity.Scope, itemID string) (*store.PrepBrief, error)
}

// clampDays is the agenda window asked for, 1 to maxDays.
func clampDays(days int) int {
	switch {
	case days <= 0:
		return defaultDays
	case days > maxDays:
		return maxDays
	}
	return days
}

// BuildAgenda lists the calendar events that haven't ended and start
// within days of now, in order, with their briefs; only what sc may see.
// An event without a label is treated as confidential (fails closed).
func BuildAgenda(st AgendaStore, sc sensitivity.Scope, now time.Time, days int) ([]Event, error) {
	items, err := st.AllItems()
	if err != nil {
		return nil, err
	}
	until := now.Add(time.Duration(clampDays(days)) * 24 * time.Hour)
	out := []Event{}
	for _, it := range items {
		if it.Kind != item.KindEvent || it.Native["cancelled"] == "true" {
			continue
		}
		end := it.End()
		if !end.After(now) || !it.OccurredAt.Before(until) {
			continue
		}
		lab, aud, err := st.ItemLabel(it.ID)
		if err != nil {
			lab, aud = sensitivity.Label{Level: sensitivity.Confidential}, nil
		}
		if !sensitivity.Visible(lab, aud, sc) {
			continue
		}
		ev := Event{ID: it.ID, Start: it.OccurredAt, End: end, Subject: it.Title, Location: it.Native["location"], Attendees: []Attendee{}}
		for _, p := range it.Participants {
			name := p.Name
			if name == "" {
				name = p.Email
			}
			if p.Role == "organizer" {
				ev.Organizer = name
			}
			if !p.Owner && name != "" {
				ev.Attendees = append(ev.Attendees, Attendee{Name: name})
			}
		}
		if b, err := st.PrepBriefFor(sc, it.ID); err == nil && b != nil && b.Text != "" {
			ev.Brief = &Brief{Text: b.Text, MadeAt: b.MadeAt, Sensitivity: Sensitivity{
				Level: b.Label.Level.String(), Share: sensitivity.ShareOf(b.Label), Why: sensitivity.Why(b.Label, 0)}}
		}
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
