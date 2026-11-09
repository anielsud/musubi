package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/m365"
)

// M365State is what Settings shows about the Microsoft 365 connection.
type M365State struct {
	Enabled     bool          `json:"enabled"`
	Connected   bool          `json:"connected"`
	Account     *m365.Account `json:"account,omitempty"`
	NeedsSignIn bool          `json:"needsSignIn"`
	LastSync    time.Time     `json:"lastSync"`
	LastError   string        `json:"lastError,omitempty"`
	LastReport  string        `json:"lastReport,omitempty"`
}

type m365Engine struct {
	once   sync.Once
	client *m365.Client
	mu     sync.Mutex
	state  M365State
}

func (e *Engine) m365() *m365.Client {
	e.ms.once.Do(func() { e.ms.client = m365.NewClient(m365.NewAuth()) })
	return e.ms.client
}

// M365State reports the connection and the last sync.
func (e *Engine) M365State() M365State {
	acc, err := e.m365().Auth.Account()
	e.ms.mu.Lock()
	defer e.ms.mu.Unlock()
	st := e.ms.state
	st.Enabled = e.Cfg.M365.Enabled
	st.Account, st.Connected = acc, acc != nil
	if err != nil {
		st.LastError = err.Error()
	}
	return st
}

// M365Connect runs the browser sign-in.
func (e *Engine) M365Connect(ctx context.Context) (*m365.Account, error) {
	acc, err := e.m365().Auth.Connect(ctx)
	e.ms.mu.Lock()
	if err == nil {
		e.ms.state.NeedsSignIn, e.ms.state.LastError = false, ""
	}
	e.ms.mu.Unlock()
	return acc, err
}

// M365SignOut forgets the sign-in.
func (e *Engine) M365SignOut() error {
	e.ms.mu.Lock()
	e.ms.state = M365State{}
	e.ms.mu.Unlock()
	return e.m365().Auth.SignOut()
}

// SyncM365 writes what the connection reads into the inbox, for the next
// ingest to pick up. Not connected (or disabled) is not an error: it does
// nothing. A sign-in that no longer works returns m365.ErrAuthRequired.
func (e *Engine) SyncM365(ctx context.Context) (string, error) {
	if !e.Cfg.M365.Enabled {
		return "", nil
	}
	c := e.m365()
	if acc, err := c.Auth.Account(); err != nil || acc == nil {
		return "", err
	}
	var parts []string
	var err error
	if e.Cfg.M365.Calendar {
		now := time.Now()
		var rep m365.CalendarReport
		rep, err = m365.SyncCalendar(ctx, c, e.Cfg.InboxDir(), now.AddDate(0, 0, -e.Cfg.M365.PastDays), now.AddDate(0, 0, e.Cfg.M365.FutureDays))
		parts = append(parts, fmt.Sprintf("calendar: %d events, %d new or changed, %d cancelled", rep.Seen, rep.Written, rep.Cancelled))
	}
	report := strings.Join(parts, "; ")
	e.ms.mu.Lock()
	defer e.ms.mu.Unlock()
	e.ms.state.LastReport = report
	if err != nil {
		e.ms.state.LastError = err.Error()
		e.ms.state.NeedsSignIn = errors.Is(err, m365.ErrAuthRequired)
		return report, err
	}
	e.ms.state.LastSync, e.ms.state.LastError, e.ms.state.NeedsSignIn = time.Now(), "", false
	return report, nil
}

// calendarContext is what Musubi tells Tomoe about a recording's meeting
// (calendar.json in the session folder). Tomoe reads it if present; Musubi
// never touches Tomoe's own files.
type calendarContext struct {
	Writer    string          `json:"writer"`
	Subject   string          `json:"subject"`
	Start     time.Time       `json:"start"`
	End       time.Time       `json:"end"`
	JoinURL   string          `json:"joinUrl,omitempty"`
	Organizer *contextPerson  `json:"organizer,omitempty"`
	Invitees  []contextPerson `json:"invitees"`
}

type contextPerson struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// WriteTomoeContext writes calendar.json next to every Tomoe recording
// linked to a calendar event, and removes it from one that no longer is.
// It returns how many files changed.
func (e *Engine) WriteTomoeContext() (int, error) {
	if !e.Cfg.M365.TomoeContext || e.Cfg.TomoeSessions == "" {
		return 0, nil
	}
	links, err := e.Store.LinksFrom("occurred_as")
	if err != nil {
		return 0, err
	}
	items, err := e.Store.AllItems()
	if err != nil {
		return 0, err
	}
	byID := make(map[string]*item.Item, len(items))
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	changed := 0
	for i := range items {
		m := &items[i]
		if m.Source != "tomoe" || m.Kind != item.KindMeeting {
			continue
		}
		dir := filepath.Join(e.Cfg.TomoeSessions, filepath.Base(m.Ref))
		if _, err := os.Stat(filepath.Join(dir, "session.json")); err != nil {
			continue
		}
		path := filepath.Join(dir, "calendar.json")
		ev := byID[links[m.ID]]
		if ev == nil {
			// Only remove a file Musubi wrote.
			if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), `"writer": "musubi"`) {
				if os.Remove(path) == nil {
					changed++
				}
			}
			continue
		}
		cc := calendarContext{Writer: "musubi", Subject: ev.Title, Start: ev.OccurredAt, End: ev.EndedAt, JoinURL: ev.Native["joinUrl"]}
		for _, p := range ev.Participants {
			cp := contextPerson{Name: p.Name, Email: p.Email}
			if p.Role == "organizer" {
				o := cp
				cc.Organizer = &o
			}
			cc.Invitees = append(cc.Invitees, cp)
		}
		sort.Slice(cc.Invitees, func(a, b int) bool { return cc.Invitees[a].Email < cc.Invitees[b].Email })
		if wrote, err := m365.WriteJSONIfChanged(path, cc); err != nil {
			return changed, err
		} else if wrote {
			changed++
		}
	}
	return changed, nil
}
