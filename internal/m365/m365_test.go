package m365

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEntra issues tokens: an authorization code for the PKCE flow, and
// refresh tokens that rotate. revoked makes every refresh fail as Entra
// does when a sign-in lapses.
type fakeEntra struct {
	mu        sync.Mutex
	refreshes int
	revoked   bool
	lastScope string
}

func idToken(user string) string {
	b, _ := json.Marshal(map[string]string{"preferred_username": user, "name": "Owner Name", "tid": "tenant-1"})
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func (f *fakeEntra) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Form.Get("client_id") != ClientID {
			t.Errorf("client_id %q", r.Form.Get("client_id"))
		}
		f.lastScope = r.Form.Get("scope")
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"bad code"}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":"at-0","refresh_token":"rt-0","id_token":%q,"expires_in":3600}`, idToken("owner@corp.example"))
		case "refresh_token":
			if f.revoked {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"AADSTS70043: The refresh token has expired\nTrace"}`)
				return
			}
			f.refreshes++
			fmt.Fprintf(w, `{"access_token":"at-%d","refresh_token":"rt-%d","expires_in":3600}`, f.refreshes, f.refreshes)
		}
	}
}

// connect runs Connect against the fake, playing the browser: it follows
// the authorize URL straight to the loopback redirect with a code.
func connect(t *testing.T, a *Auth, entra *httptest.Server) *Account {
	t.Helper()
	a.Authority = entra.URL
	a.OpenBrowser = func(u string) error {
		pu, err := url.Parse(u)
		if err != nil {
			return err
		}
		q := pu.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			t.Errorf("no PKCE challenge in %s", u)
		}
		if !strings.Contains(q.Get("scope"), "offline_access") || !strings.Contains(q.Get("scope"), "Calendars.Read") {
			t.Errorf("scope %q", q.Get("scope"))
		}
		redirect := q.Get("redirect_uri")
		if !strings.HasPrefix(redirect, "http://localhost:") {
			t.Errorf("redirect %q", redirect)
		}
		go func() {
			// The redirect names localhost; reach it on 127.0.0.1, where it listens.
			resp, err := http.Get(strings.Replace(redirect, "localhost", "127.0.0.1", 1) + "?code=good-code&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acc, err := a.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func TestConnectRefreshAndSignOut(t *testing.T) {
	f := &fakeEntra{}
	entra := httptest.NewServer(f.handler(t))
	defer entra.Close()
	sec := &Memory{}
	a := &Auth{Secrets: sec, HTTP: entra.Client()}
	acc := connect(t, a, entra)
	if acc.Username != "owner@corp.example" || acc.TenantID != "tenant-1" {
		t.Fatalf("account %+v", acc)
	}
	// The sign-in survives a restart: a new Auth reads it from the secrets.
	b := &Auth{Authority: entra.URL, Secrets: sec, HTTP: entra.Client()}
	tok, err := b.Token(context.Background(), graphResource)
	if err != nil || tok != "at-1" {
		t.Fatalf("token after restart: %q %v", tok, err)
	}
	// The rotated refresh token was stored.
	raw, _ := sec.Get()
	if !strings.Contains(string(raw), `"refresh":"rt-1"`) {
		t.Errorf("stored %s", raw)
	}
	// Cached until near expiry: no second refresh.
	if tok, _ := b.Token(context.Background(), graphResource); tok != "at-1" || f.refreshes != 1 {
		t.Errorf("cached token %q after %d refreshes", tok, f.refreshes)
	}
	// SharePoint gets its own token from the same sign-in.
	if tok, err := b.Token(context.Background(), "https://corp.sharepoint.com"); err != nil || tok != "at-2" || !strings.HasPrefix(f.lastScope, "https://corp.sharepoint.com/.default") {
		t.Errorf("sharepoint token %q %v scope %q", tok, err, f.lastScope)
	}
	if err := b.SignOut(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Token(context.Background(), graphResource); !errors.Is(err, ErrNotConnected) {
		t.Errorf("after sign-out: %v", err)
	}
	if acc, _ := b.Account(); acc != nil {
		t.Error("account after sign-out")
	}
}

func TestLapsedSignInAsksForAuth(t *testing.T) {
	f := &fakeEntra{}
	entra := httptest.NewServer(f.handler(t))
	defer entra.Close()
	a := &Auth{Secrets: &Memory{}, HTTP: entra.Client()}
	connect(t, a, entra)
	a.forget(graphResource)
	f.revoked = true
	_, err := a.Token(context.Background(), graphResource)
	if !errors.Is(err, ErrAuthRequired) || strings.Contains(err.Error(), "Trace") {
		t.Fatalf("lapsed sign-in: %v", err)
	}
}

func TestOnlyAllowListedGets(t *testing.T) {
	g := "https://graph.microsoft.com"
	for u, want := range map[string]bool{
		g + "/v1.0/me/calendarView?startDateTime=x":                                                   true,
		g + "/v1.0/me/onlineMeetings/abc/transcripts":                                                 true,
		g + "/v1.0/me/onlineMeetings/abc/transcripts/t1/content?$format=x":                            true,
		g + "/v1.0/chats/19:x@thread.v2/messages?$top=50":                                             true,
		g + "/v1.0/users/someone@corp.example?$select=jobTitle":                                       true,
		g + "/v1.0/users/abc/manager":                                                                 true,
		"https://corp-my.sharepoint.com/_api/v2.1/drives/d/items/i/media/transcripts":                 true,
		"https://corp-my.sharepoint.com/_api/v2.1/drives/d/items/i/media/transcripts/t/streamContent": true,
		g + "/v1.0/me/sendMail":                                                                       false,
		g + "/v1.0/users":                                                                             false, // the whole directory
		g + "/v1.0/me/messages":                                                                       false, // mail: not this design
		g + "/beta/me/calendarView":                                                                   false,
		g + "/v1.0/me/calendarView/../sendMail":                                                       false,
		"https://evil.example/v1.0/me":                                                                false,
		"http://graph.microsoft.com/v1.0/me":                                                          false,
		"https://corp.sharepoint.com/v1.0/me":                                                         false,
		"https://corp.sharepoint.com/_api/web":                                                        false,
	} {
		if got := Allowed(u, g); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", u, got, want)
		}
	}
}

// fakeGraph serves calendarView in two pages.
func fakeGraph(t *testing.T, events []map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/v1.0/me/calendarView" {
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		half := len(events) / 2
		page, next := events[:half], "http://"+r.Host+"/v1.0/me/calendarView?page=2"
		if r.URL.Query().Get("page") == "2" {
			page, next = events[half:], ""
		}
		json.NewEncoder(w).Encode(map[string]any{"value": page, "@odata.nextLink": next})
	}))
}

func event(id, subject, start string, extra map[string]any) map[string]any {
	e := map[string]any{
		"id": id, "subject": subject, "bodyPreview": "agenda",
		"organizer": map[string]any{"emailAddress": map[string]any{"name": "Robin Vale", "address": "Robin.Vale@corp.example"}},
		"attendees": []any{
			map[string]any{"type": "required", "emailAddress": map[string]any{"name": "Kai Moreno", "address": "kai.moreno@corp.example"}, "status": map[string]any{"response": "accepted"}},
			map[string]any{"type": "resource", "emailAddress": map[string]any{"name": "Room 4", "address": "room4@corp.example"}},
		},
		"start": map[string]any{"dateTime": start, "timeZone": "UTC"}, "end": map[string]any{"dateTime": start[:11] + "23:00:00.0000000", "timeZone": "UTC"},
		"showAs": "busy", "sensitivity": "normal",
		"onlineMeeting":  map[string]any{"joinUrl": "https://teams.example/l/meetup-join/19%3ameeting_x%40thread.v2/0"},
		"responseStatus": map[string]any{"response": "accepted"},
	}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func TestSyncCalendar(t *testing.T) {
	f := &fakeEntra{}
	entra := httptest.NewServer(f.handler(t))
	defer entra.Close()
	a := &Auth{Secrets: &Memory{}, HTTP: entra.Client()}
	connect(t, a, entra)
	events := []map[string]any{
		event("e1", "Planning sync", "2026-03-02T10:00:00.0000000", nil),
		event("e2", "Pay review", "2026-03-03T10:00:00.0000000", map[string]any{"sensitivity": "private"}),
		event("e3", "Offsite", "2026-03-04T10:00:00.0000000", map[string]any{"isCancelled": true}),
		event("e4", "One to one", "2026-03-05T10:00:00.0000000", map[string]any{"isOrganizer": true}),
	}
	g := fakeGraph(t, events)
	defer g.Close()
	c := &Client{Auth: a, Graph: "http://" + strings.TrimPrefix(g.URL, "http://"), HTTP: g.Client()}
	dir := t.TempDir()
	from, to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	rep, err := SyncCalendar(context.Background(), c, dir, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Seen != 4 || rep.Written != 4 {
		t.Fatalf("first sync %+v", rep)
	}
	files, _ := filepath.Glob(filepath.Join(dir, CalendarSource, "*.json"))
	if len(files) != 4 {
		t.Fatalf("%d files", len(files))
	}
	var env struct {
		Kind, Source, Ref string
		Record            eventDrop
	}
	b, _ := os.ReadFile(filepath.Join(dir, CalendarSource, fileName("e4")))
	json.Unmarshal(b, &env)
	if env.Kind != "event" || env.Source != CalendarSource || env.Ref != "e4" || env.Record.Response != "organizer" || env.Record.JoinURL == "" {
		t.Errorf("drop %+v", env)
	}
	if len(env.Record.Attendees) != 1 || env.Record.Attendees[0].Email != "kai.moreno@corp.example" || env.Record.Organizer.Email != "robin.vale@corp.example" {
		t.Errorf("people %+v / %+v (rooms left out, emails lower-cased)", env.Record.Attendees, env.Record.Organizer)
	}
	if st, err := os.Stat(files[0]); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", st.Mode())
	}
	// Nothing changed: nothing rewritten.
	if rep, _ := SyncCalendar(context.Background(), c, dir, from, to); rep.Written != 0 || rep.Cancelled != 0 {
		t.Errorf("second sync %+v", rep)
	}
	// e1 deleted from the calendar: its drop becomes cancelled.
	g2 := fakeGraph(t, events[1:])
	defer g2.Close()
	c.Graph = g2.URL
	if rep, err := SyncCalendar(context.Background(), c, dir, from, to); err != nil || rep.Cancelled != 1 {
		t.Errorf("after delete %+v %v", rep, err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, CalendarSource, fileName("e1")))
	json.Unmarshal(b, &env)
	if !env.Record.IsCancelled {
		t.Error("deleted event not marked cancelled")
	}
}

func TestClientRefusesBeforeSending(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	c := &Client{Auth: &Auth{Secrets: &Memory{}}, Graph: srv.URL, HTTP: srv.Client()}
	if _, err := c.Get(context.Background(), "/v1.0/me/sendMail", nil); err == nil || called {
		t.Fatalf("sendMail: err %v, request sent %v", err, called)
	}
}
