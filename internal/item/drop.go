package item

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// Record shapes accepted in inbox envelopes.
//
// event (Graph calendar event as the M365 connector returns it):
//
//	{subject, summary, organizer, attendees: ["email", ...], start: {dateTime, timeZone},
//	 end: {...}, isCancelled, showAs, sensitivity}
//
// email:
//
//	{subject, from, to: [...], cc: [...], sentAt (RFC 3339), body, threadId, sensitivity}
//
// chat:
//
//	{chatId, topic, members: [...], messages: [{from, sentAt, text}]}
//
// A person is either "email", "Name <email>", or {"name": ..., "email": ...}.

type person struct{ Name, Email string }

func (p *person) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		if a, err := mail.ParseAddress(s); err == nil {
			p.Name, p.Email = a.Name, strings.ToLower(a.Address)
		} else if strings.Contains(s, "@") {
			p.Email = strings.ToLower(strings.TrimSpace(s))
		} else {
			p.Name = strings.TrimSpace(s)
		}
		return nil
	}
	var o struct{ Name, Email string }
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	p.Name, p.Email = strings.TrimSpace(o.Name), strings.ToLower(strings.TrimSpace(o.Email))
	return nil
}

type graphTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

func (g graphTime) parse() (time.Time, error) {
	loc := time.UTC
	if g.TimeZone != "" {
		if l, err := time.LoadLocation(g.TimeZone); err == nil {
			loc = l
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.0000000", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, g.DateTime, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad time %q", g.DateTime)
}

func fromDrop(b []byte) ([]Item, error) {
	var env struct {
		Kind   string          `json:"kind"`
		Record json.RawMessage `json:"record"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	switch env.Kind {
	case KindEvent:
		return dropEvent(env.Record)
	case KindEmail:
		return dropEmail(env.Record)
	case KindChat:
		return dropChat(env.Record)
	case KindMeeting:
		return dropMeeting(env.Record)
	}
	return nil, fmt.Errorf("unknown drop kind %q", env.Kind)
}

func dropEvent(b []byte) ([]Item, error) {
	var e struct {
		Subject     string    `json:"subject"`
		Summary     string    `json:"summary"`
		Organizer   person    `json:"organizer"`
		Attendees   []person  `json:"attendees"`
		RawStart    graphTime `json:"start"`
		RawEnd      graphTime `json:"end"`
		IsCancelled bool      `json:"isCancelled"`
		IsAllDay    bool      `json:"isAllDay"`
		ShowAs      string    `json:"showAs"`
		Sensitivity string    `json:"sensitivity"`
		Location    string    `json:"location"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	start, err := e.RawStart.parse()
	if err != nil {
		return nil, err
	}
	end, err := e.RawEnd.parse()
	if err != nil {
		return nil, err
	}
	it := Item{Kind: KindEvent, Title: e.Subject, OccurredAt: start, EndedAt: end, Native: map[string]string{
		"showAs": e.ShowAs, "sensitivity": e.Sensitivity, "location": e.Location,
	}}
	if e.IsCancelled {
		it.Native["cancelled"] = "true"
	}
	if e.IsAllDay {
		it.Native["allDay"] = "true"
	}
	if txt := strings.TrimSpace(e.Summary); txt != "" || e.Subject != "" {
		it.Segments = []Segment{{Text: strings.TrimSpace(e.Subject + "\n" + txt)}}
	}
	seen := map[string]bool{}
	add := func(p person, role string) {
		if p.Email == "" || seen[p.Email] {
			return
		}
		seen[p.Email] = true
		it.Participants = append(it.Participants, Participant{Name: p.Name, Email: p.Email, Role: role})
	}
	add(e.Organizer, "organizer")
	for _, a := range e.Attendees {
		add(a, "attendee")
	}
	return []Item{it}, nil
}

func dropEmail(b []byte) ([]Item, error) {
	var m struct {
		Subject     string    `json:"subject"`
		From        person    `json:"from"`
		RawTo       []person  `json:"to"`
		RawCc       []person  `json:"cc"`
		SentAt      time.Time `json:"sentAt"`
		Body        string    `json:"body"`
		ThreadID    string    `json:"threadId"`
		Sensitivity string    `json:"sensitivity"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	it := Item{Kind: KindEmail, Title: m.Subject, OccurredAt: m.SentAt, EndedAt: m.SentAt, Thread: m.ThreadID,
		Native: map[string]string{"sensitivity": m.Sensitivity}}
	it.Segments = []Segment{{Speaker: m.From.Name, Email: m.From.Email, Text: stripQuoted(m.Body)}}
	it.Participants = append(it.Participants, Participant{Name: m.From.Name, Email: m.From.Email, Role: "from"})
	for _, p := range m.RawTo {
		it.Participants = append(it.Participants, Participant{Name: p.Name, Email: p.Email, Role: "to"})
	}
	for _, p := range m.RawCc {
		it.Participants = append(it.Participants, Participant{Name: p.Name, Email: p.Email, Role: "cc"})
	}
	return []Item{it}, nil
}

func dropChat(b []byte) ([]Item, error) {
	var c struct {
		ChatID   string   `json:"chatId"`
		Topic    string   `json:"topic"`
		Members  []person `json:"members"`
		Messages []struct {
			From   person    `json:"from"`
			SentAt time.Time `json:"sentAt"`
			Text   string    `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	it := Item{Kind: KindChat, Title: c.Topic, Thread: c.ChatID, Native: map[string]string{}}
	for _, p := range c.Members {
		it.Participants = append(it.Participants, Participant{Name: p.Name, Email: p.Email, Role: "member"})
	}
	for i, m := range c.Messages {
		if i == 0 {
			it.OccurredAt = m.SentAt
		}
		it.EndedAt = m.SentAt
		it.Segments = append(it.Segments, Segment{Speaker: m.From.Name, Email: m.From.Email,
			Start: m.SentAt.Sub(it.OccurredAt).Seconds(), Text: strings.TrimSpace(m.Text)})
	}
	return []Item{it}, nil
}

// dropMeeting reads a transcript from another recorder (e.g. a Teams
// export): one turn per segment, speakers as the source gives them; an
// empty speaker stays unknown.
func dropMeeting(b []byte) ([]Item, error) {
	var m struct {
		Title     string    `json:"title"`
		Start     time.Time `json:"start"`
		End       time.Time `json:"end"`
		Attendees []person  `json:"attendees"`
		Turns     []struct {
			Speaker string  `json:"speaker"`
			Email   string  `json:"email"`
			Offset  float64 `json:"offset"` // seconds from start
			Text    string  `json:"text"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Start.IsZero() {
		return nil, fmt.Errorf("meeting drop needs a start time")
	}
	it := Item{Kind: KindMeeting, Title: m.Title, OccurredAt: m.Start, EndedAt: m.End, Native: map[string]string{}}
	for _, p := range m.Attendees {
		it.Participants = append(it.Participants, Participant{Name: p.Name, Email: p.Email, Role: "attendee"})
	}
	for _, t := range m.Turns {
		it.Segments = append(it.Segments, Segment{Speaker: t.Speaker, Email: t.Email, Start: t.Offset, Text: strings.TrimSpace(t.Text)})
	}
	return []Item{it}, nil
}

// stripQuoted drops quoted history from a reply so each message's text is
// only what it added; the earlier messages are their own items.
func stripQuoted(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, ">") {
			continue
		}
		if (strings.HasPrefix(t, "On ") && strings.HasSuffix(t, "wrote:")) || strings.HasPrefix(t, "-----Original Message") || strings.HasPrefix(t, "From: ") {
			break
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
