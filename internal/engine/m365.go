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
	"unicode"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/m365"
	"github.com/anielsud/musubi/internal/sensitivity"
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
		if err == nil && e.Cfg.M365.Transcripts {
			var tr m365.TeamsReport
			tr, err = m365.SyncTeams(ctx, c, e.Cfg.InboxDir(), rep.Events, m365.TeamsOptions{
				Unattended: e.Cfg.M365.Unattended, Chat: e.Cfg.M365.MeetingChat,
				OwnerName: e.Cfg.Owner.Name, OwnerEmail: e.Cfg.Owner.Email,
				Recorded: e.tomoeRecorded(),
			}, now)
			parts = append(parts, fmt.Sprintf("teams: %d meetings, %d transcripts, %d chats, %d passed over", tr.Meetings, tr.Transcripts, tr.Chats, tr.Skipped))
		}
	}
	if err == nil && e.Cfg.M365.People {
		var pr m365.PeopleReport
		pr, err = m365.SyncPeople(ctx, c, e.Cfg.DirectoryPath(), e.internalPeople(), 14*24*time.Hour, 300, time.Now())
		parts = append(parts, fmt.Sprintf("directory: %d looked up (%d found, %d not there), %d left for later", pr.Looked, pr.Found, pr.Missing, pr.Left))
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
	// Teams copies by the event they're linked to: the same meeting as a
	// Tomoe recording linked there.
	teamsByEvent := map[string][]*item.Item{}
	for i := range items {
		t := &items[i]
		if t.Source == "teams" && t.Kind == item.KindMeeting && strings.HasPrefix(t.Ref, "teams-") && links[t.ID] != "" {
			teamsByEvent[links[t.ID]] = append(teamsByEvent[links[t.ID]], t)
		}
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
		tpath := filepath.Join(dir, "teams.json")
		t := overlapping(m, teamsByEvent[ev.ID])
		if t != nil && wordAgreement(m, t) < 0.6 {
			t = nil // same slot, different conversation (another meeting that day)
		}
		if t == nil {
			if b, err := os.ReadFile(tpath); err == nil && strings.Contains(string(b), `"writer": "musubi"`) {
				if os.Remove(tpath) == nil {
					changed++
				}
			}
		}
		if t != nil {
			tc := teamsContext{Writer: "musubi", Source: "teams", Subject: t.Title, Start: t.OccurredAt, End: t.EndedAt}
			for _, sg := range t.Segments {
				tc.Turns = append(tc.Turns, teamsTurn{Speaker: sg.Speaker, Offset: sg.Start, Text: sg.Text})
			}
			if wrote, err := m365.WriteJSONIfChanged(tpath, tc); err != nil {
				return changed, err
			} else if wrote {
				changed++
			}
		}
	}
	return changed, nil
}

// teamsContext is the Teams transcript of a Tomoe recording's meeting
// (teams.json in the session folder): Teams names every speaker, which
// Tomoe can line up with its own speakers by time. Offsets are seconds
// from Start.
type teamsContext struct {
	Writer  string      `json:"writer"`
	Source  string      `json:"source"`
	Subject string      `json:"subject"`
	Start   time.Time   `json:"start"`
	End     time.Time   `json:"end"`
	Turns   []teamsTurn `json:"turns"`
}

type teamsTurn struct {
	Speaker string  `json:"speaker"`
	Offset  float64 `json:"offset"`
	Text    string  `json:"text"`
}

// wordAgreement is the share of the Teams copy's distinct longer words
// (four letters or more), said while Tomoe was recording, that the Tomoe
// recording also has. On all-t4 the same meeting agreed 0.89–0.98 and a
// different conversation in the same slot 0.13–0.41 (2026-11-09).
func wordAgreement(m, t *item.Item) float64 {
	words := func(it *item.Item, window bool) map[string]bool {
		w := map[string]bool{}
		for _, sg := range it.Segments {
			if at := it.OccurredAt.Add(time.Duration(sg.Start * float64(time.Second))); window && (at.Before(m.OccurredAt) || at.After(m.EndedAt)) {
				continue
			}
			for _, f := range strings.FieldsFunc(strings.ToLower(sg.Text), func(r rune) bool { return !unicode.IsLetter(r) }) {
				if len(f) >= 4 {
					w[f] = true
				}
			}
		}
		return w
	}
	tw, mw := words(t, true), words(m, false)
	if len(tw) == 0 {
		return 0
	}
	n := 0
	for w := range tw {
		if mw[w] {
			n++
		}
	}
	return float64(n) / float64(len(tw))
}

// overlapping is the Teams copy that shares the most time with m (at least
// a quarter of the recording), or nil.
func overlapping(m *item.Item, ts []*item.Item) *item.Item {
	var best *item.Item
	var bestD time.Duration
	for _, t := range ts {
		lo, hi := m.OccurredAt, m.EndedAt
		if t.OccurredAt.After(lo) {
			lo = t.OccurredAt
		}
		if t.EndedAt.Before(hi) {
			hi = t.EndedAt
		}
		if d := hi.Sub(lo); d > bestD && d >= m.EndedAt.Sub(m.OccurredAt)/4 {
			best, bestD = t, d
		}
	}
	return best
}

// tomoeRecorded reports whether a Tomoe recording covers at least five
// minutes (or half, for a short meeting) of start..end: the owner was in
// that meeting.
func (e *Engine) tomoeRecorded() func(start, end time.Time) bool {
	type span struct{ a, b time.Time }
	var spans []span
	if items, err := e.Store.AllItems(); err == nil {
		for _, it := range items {
			if it.Source == "tomoe" && it.Kind == item.KindMeeting {
				spans = append(spans, span{it.OccurredAt, it.EndedAt})
			}
		}
	}
	return func(start, end time.Time) bool {
		need := min(5*time.Minute, end.Sub(start)/2)
		for _, s := range spans {
			lo, hi := s.a, s.b
			if start.After(lo) {
				lo = start
			}
			if end.Before(hi) {
				hi = end
			}
			if hi.Sub(lo) >= need && need > 0 {
				return true
			}
		}
		return false
	}
}

// internalPeople are the colleagues' addresses among participants: the
// owner's domains, first.last addresses (not lists or rooms).
func (e *Engine) internalPeople() []string {
	items, err := e.Store.AllItems()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		for _, p := range it.Participants {
			em := strings.ToLower(p.Email)
			at := strings.LastIndexByte(em, '@')
			if at < 1 || seen[em] || !strings.Contains(em[:at], ".") || !sensitivity.InternalDomain(em[at+1:], e.Cfg.Owner.InternalDomains) {
				continue
			}
			seen[em] = true
			out = append(out, em)
		}
	}
	return out
}

// PersonProfile is what the person views show from the directory and the
// owner's own records.
type PersonProfile struct {
	*m365.Person
	Chain   []*m365.Person `json:"chain"`   // managers, nearest first
	Reports []*m365.Person `json:"reports"` // direct reports
	// Together counts the meetings and events both the owner and this
	// person were part of; LastTogether is the latest.
	Together     int       `json:"together"`
	LastTogether time.Time `json:"lastTogether"`
}

// PersonProfile looks a person entity ("person:<email>") up in the
// directory file; nil when it isn't there.
func (e *Engine) PersonProfile(entityID string) (*PersonProfile, error) {
	email := strings.TrimPrefix(entityID, "person:")
	if email == entityID || !strings.Contains(email, "@") {
		return nil, nil
	}
	dir, err := m365.LoadDirectory(e.Cfg.DirectoryPath())
	if err != nil {
		return nil, err
	}
	p := dir.People[strings.ToLower(email)]
	if p == nil || p.Missing {
		return nil, nil
	}
	pp := &PersonProfile{Person: p, Chain: dir.Chain(email), Reports: dir.Reports(email)}
	items, err := e.Store.AllItems()
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Kind != item.KindMeeting && it.Kind != item.KindEvent {
			continue
		}
		for _, q := range it.Participants {
			if strings.EqualFold(q.Email, email) {
				pp.Together++
				if it.OccurredAt.After(pp.LastTogether) && it.OccurredAt.Before(time.Now()) {
					pp.LastTogether = it.OccurredAt
				}
				break
			}
		}
	}
	return pp, nil
}
