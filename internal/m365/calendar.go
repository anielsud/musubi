package m365

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CalendarSource is the inbox source calendar events are written under.
const CalendarSource = "m365-calendar"

type graphEvent struct {
	ID          string `json:"id"`
	ICalUID     string `json:"iCalUId"`
	Subject     string `json:"subject"`
	BodyPreview string `json:"bodyPreview"`
	Organizer   struct {
		EmailAddress emailAddress `json:"emailAddress"`
	} `json:"organizer"`
	Attendees []struct {
		Type         string       `json:"type"`
		EmailAddress emailAddress `json:"emailAddress"`
		Status       struct {
			Response string `json:"response"`
		} `json:"status"`
	} `json:"attendees"`
	Start          graphDateTime                `json:"start"`
	End            graphDateTime                `json:"end"`
	IsCancelled    bool                         `json:"isCancelled"`
	IsAllDay       bool                         `json:"isAllDay"`
	IsOrganizer    bool                         `json:"isOrganizer"`
	ShowAs         string                       `json:"showAs"`
	Sensitivity    string                       `json:"sensitivity"`
	Location       struct{ DisplayName string } `json:"location"`
	OnlineMeeting  *struct{ JoinURL string }    `json:"onlineMeeting"`
	ResponseStatus struct {
		Response string `json:"response"`
	} `json:"responseStatus"`
}

type emailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type graphDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// eventDrop is the inbox record for an event, in the shape
// internal/item/drop.go reads (dropEvent).
type eventDrop struct {
	Subject     string        `json:"subject"`
	Summary     string        `json:"summary"`
	Organizer   person        `json:"organizer"`
	Attendees   []person      `json:"attendees"`
	Start       graphDateTime `json:"start"`
	End         graphDateTime `json:"end"`
	IsCancelled bool          `json:"isCancelled"`
	IsAllDay    bool          `json:"isAllDay"`
	ShowAs      string        `json:"showAs"`
	Sensitivity string        `json:"sensitivity"`
	Location    string        `json:"location"`
	JoinURL     string        `json:"joinUrl,omitempty"`
	ICalUID     string        `json:"iCalUId,omitempty"`
	Response    string        `json:"response,omitempty"`
}

type person struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Response string `json:"response,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

type envelope struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Ref    string `json:"ref"`
	Record any    `json:"record"`
}

// CalendarReport counts what a sync wrote.
type CalendarReport struct {
	Seen, Written, Cancelled int
}

// SyncCalendar writes the owner's calendar between from and to as inbox
// drops under dir/m365-calendar, one file per occurrence. A file is only
// rewritten when the event changed, so unchanged events cost Musubi
// nothing. An event previously written for this window that the calendar
// no longer returns (deleted) is rewritten as cancelled.
func SyncCalendar(ctx context.Context, c *Client, dir string, from, to time.Time) (CalendarReport, error) {
	var rep CalendarReport
	out := filepath.Join(dir, CalendarSource)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return rep, err
	}
	q := url.Values{
		"startDateTime": {from.UTC().Format(time.RFC3339)},
		"endDateTime":   {to.UTC().Format(time.RFC3339)},
		"$select":       {"id,iCalUId,subject,bodyPreview,organizer,attendees,start,end,isCancelled,isAllDay,isOrganizer,showAs,sensitivity,location,onlineMeeting,responseStatus"},
		"$top":          {"100"},
	}
	seen := map[string]bool{}
	err := c.Pages(ctx, "/v1.0/me/calendarView?"+q.Encode(), func(raw json.RawMessage) error {
		var e graphEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if e.ID == "" {
			return nil
		}
		rep.Seen++
		name := fileName(e.ID)
		seen[name] = true
		d := toDrop(e)
		wrote, err := WriteJSONIfChanged(filepath.Join(out, name), envelope{Kind: "event", Source: CalendarSource, Ref: e.ID, Record: d})
		if wrote {
			rep.Written++
		}
		return err
	})
	if err != nil {
		return rep, err
	}
	// Deleted occurrences: a file in this window that the calendar no
	// longer lists becomes cancelled, so its recording link is dropped.
	files, _ := filepath.Glob(filepath.Join(out, "*.json"))
	for _, f := range files {
		if seen[filepath.Base(f)] {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var env struct {
			Kind, Source, Ref string
			Record            eventDrop
		}
		if json.Unmarshal(b, &env) != nil || env.Record.IsCancelled {
			continue
		}
		st, err := parseUTC(env.Record.Start.DateTime)
		if err != nil || st.Before(from) || !st.Before(to) {
			continue
		}
		env.Record.IsCancelled = true
		if wrote, err := WriteJSONIfChanged(f, envelope{Kind: env.Kind, Source: env.Source, Ref: env.Ref, Record: env.Record}); err == nil && wrote {
			rep.Cancelled++
		}
	}
	return rep, nil
}

func toDrop(e graphEvent) eventDrop {
	d := eventDrop{
		Subject: e.Subject, Summary: e.BodyPreview,
		Organizer: person{Name: e.Organizer.EmailAddress.Name, Email: strings.ToLower(e.Organizer.EmailAddress.Address)},
		Start:     e.Start, End: e.End, IsCancelled: e.IsCancelled, IsAllDay: e.IsAllDay,
		ShowAs: e.ShowAs, Sensitivity: e.Sensitivity, Location: e.Location.DisplayName,
		ICalUID: e.ICalUID, Response: e.ResponseStatus.Response,
	}
	if e.IsOrganizer {
		d.Response = "organizer"
	}
	if e.OnlineMeeting != nil {
		d.JoinURL = e.OnlineMeeting.JoinURL
	}
	for _, a := range e.Attendees {
		if a.Type == "resource" || a.EmailAddress.Address == "" {
			continue // rooms and equipment
		}
		d.Attendees = append(d.Attendees, person{Name: a.EmailAddress.Name, Email: strings.ToLower(a.EmailAddress.Address), Response: a.Status.Response, Optional: a.Type == "optional"})
	}
	return d
}

func fileName(ref string) string {
	h := sha1.Sum([]byte(ref))
	return hex.EncodeToString(h[:12]) + ".json"
}

// WriteJSONIfChanged writes v as JSON to path unless the file already holds
// exactly that, atomically and readable only by the owner.
func WriteJSONIfChanged(path string, v any) (bool, error) {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return false, err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return false, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

func parseUTC(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05.0000000", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad time %q", s)
}
