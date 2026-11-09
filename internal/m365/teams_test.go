package m365

import (
	"context"
	"encoding/json"
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

const sampleVTT = "WEBVTT\n\n" +
	"a1/1-0\n00:00:31.866 --> 00:00:32.826\n<v Robin Vale>Morning all.</v>\n\n" +
	"a1/2-0\n00:00:33.100 --> 00:00:36.000\n<v Robin Vale>Let's start with the plan.</v>\n\n" +
	"a1/3-0\n00:01:05.000 --> 00:01:09.500\n<v Owner Name>I'll send it today,\nafter lunch.</v>\n\n" +
	"a1/4-0\n00:01:10.000 --> 00:01:11.000\nNo speaker here.\n"

func TestParseVTT(t *testing.T) {
	got := parseVTT(sampleVTT)
	if len(got) != 3 {
		t.Fatalf("%d turns: %+v", len(got), got)
	}
	if got[0].Speaker != "Robin Vale" || got[0].Text != "Morning all. Let's start with the plan." || got[0].Offset < 31.8 || got[0].Offset > 31.9 {
		t.Errorf("merged turn %+v", got[0])
	}
	if got[1].Speaker != "Owner Name" || got[1].Text != "I'll send it today, after lunch." || got[1].Offset != 65 {
		t.Errorf("multi-line cue %+v", got[1])
	}
	if got[2].Speaker != "" || got[2].Text != "No speaker here." {
		t.Errorf("speaker-less cue %+v", got[2])
	}
}

func TestSPTurns(t *testing.T) {
	b := []byte(`{"entries":[{"speakerDisplayName":"Kai Moreno","startOffset":"00:00:03.1100000","text":"Welcome."},
		{"speakerDisplayName":"Kai Moreno","startOffset":"00:00:06.1760000","text":"Two items today."},
		{"speakerDisplayName":"Robin Vale","startOffset":"00:01:00.0000000","text":"Thanks."}]}`)
	got := spTurns(b)
	if len(got) != 2 || got[0].Text != "Welcome. Two items today." || got[0].Offset < 3.1 || got[1].Speaker != "Robin Vale" || got[1].Offset != 60 {
		t.Errorf("%+v", got)
	}
}

func TestJoinParts(t *testing.T) {
	u := "https://teams.microsoft.com/l/meetup-join/19%3ameeting_ABC123%40thread.v2/0?context=%7b%22Tid%22%3a%22t-1%22%2c%22Oid%22%3a%22o-9%22%7d"
	thread, oid := joinParts(u)
	if thread != "19:meeting_ABC123@thread.v2" || oid != "o-9" {
		t.Errorf("thread %q oid %q", thread, oid)
	}
	if th, o := joinParts("https://teams.microsoft.com/meet/123456?p=abc"); th != "" || o != "" {
		t.Errorf("short link: %q %q", th, o)
	}
}

// routed sends every request, whatever its host, to h: the fake Graph and
// SharePoint keep their real hosts, so the allow-list sees real URLs.
type routed struct{ h http.Handler }

func (r routed) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

type fakeMS struct {
	mu   sync.Mutex
	hits map[string]int
	// listRefused: the owner was invited through a list (3003 on lookup and
	// on transcripts).
	listRefused bool
}

func (f *fakeMS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	key := r.URL.Host + r.URL.EscapedPath()
	f.hits[key]++
	f.mu.Unlock()
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(401)
		return
	}
	refuse := func() {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":"Forbidden","message":"3003: User does not have access to lookup meeting"}}`)
	}
	p := r.URL.EscapedPath()
	switch {
	case r.URL.Host == "graph.microsoft.com" && p == "/v1.0/me/onlineMeetings":
		if f.listRefused {
			refuse()
			return
		}
		fmt.Fprint(w, `{"value":[{"id":"MEETING1","chatInfo":{"threadId":"19:meeting_ABC@thread.v2"}}]}`)
	case strings.HasSuffix(p, "/transcripts") && strings.HasPrefix(p, "/v1.0/me/onlineMeetings/"):
		if f.listRefused {
			refuse()
			return
		}
		fmt.Fprint(w, `{"value":[
			{"id":"T-ATTENDED","createdDateTime":"2026-03-02T15:01:00Z","endDateTime":"2026-03-02T15:40:00Z"},
			{"id":"T-SKIPPED","createdDateTime":"2026-03-09T15:01:00Z","endDateTime":"2026-03-09T15:40:00Z"},
			{"id":"T-OTHER-WEEK","createdDateTime":"2026-02-23T15:01:00Z","endDateTime":"2026-02-23T15:40:00Z"}]}`)
	case strings.HasSuffix(p, "/content"):
		if strings.Contains(p, "T-SKIPPED") {
			fmt.Fprint(w, "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<v Robin Vale>Without the owner.</v>\n")
			return
		}
		fmt.Fprint(w, sampleVTT)
	case strings.HasPrefix(p, "/v1.0/chats/"):
		json.NewEncoder(w).Encode(map[string]any{"value": []any{
			map[string]any{"id": "m1", "messageType": "message", "createdDateTime": "2026-03-02T15:10:00Z", "from": map[string]any{"user": map[string]any{"displayName": "Kai Moreno"}}, "body": map[string]any{"contentType": "html", "content": "<p>Deck is <b>here</b></p>"}},
			map[string]any{"id": "m2", "messageType": "systemEventMessage", "createdDateTime": "2026-03-02T15:39:00Z", "eventDetail": map[string]any{"@odata.type": "#microsoft.graph.callRecordingEventMessageDetail", "callRecordingStatus": "success", "callRecordingUrl": "https://corp-my.sharepoint.com/:v:/g/personal/organizer/rec.mp4"}},
			map[string]any{"id": "m3", "messageType": "message", "createdDateTime": "2026-02-20T09:00:00Z", "from": map[string]any{"user": map[string]any{"displayName": "Kai Moreno"}}, "body": map[string]any{"contentType": "text", "content": "old, outside the meeting"}},
		}})
	case strings.HasPrefix(p, "/v1.0/shares/"):
		fmt.Fprint(w, `{"id":"ITEM1","parentReference":{"driveId":"DRIVE1"}}`)
	case r.URL.Host == "corp-my.sharepoint.com" && strings.HasSuffix(p, "/media/transcripts"):
		fmt.Fprint(w, `{"value":[{"id":"SP-T1"}]}`)
	case r.URL.Host == "corp-my.sharepoint.com" && strings.HasSuffix(p, "/streamContent"):
		fmt.Fprint(w, `{"entries":[{"speakerDisplayName":"Kai Moreno","startOffset":"00:00:03.1100000","text":"Welcome to the all-hands."}]}`)
	default:
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error":{"code":"NotFound","message":"%s"}}`, p)
	}
}

func teamsClient(t *testing.T, ms *fakeMS) *Client {
	t.Helper()
	f := &fakeEntra{}
	entra := httptest.NewServer(f.handler(t))
	t.Cleanup(entra.Close)
	a := &Auth{Secrets: &Memory{}, HTTP: entra.Client()}
	connect(t, a, entra)
	a.HTTP = &http.Client{Transport: routedEntra{entra: entra, rest: routed{ms}}}
	return &Client{Auth: a, Graph: graphResource, HTTP: &http.Client{Transport: routed{ms}}}
}

// routedEntra keeps token requests on the fake Entra (by its URL) and
// sends anything else to rest.
type routedEntra struct {
	entra *httptest.Server
	rest  http.RoundTripper
}

func (r routedEntra) RoundTrip(req *http.Request) (*http.Response, error) {
	if u, _ := url.Parse(r.entra.URL); req.URL.Host == u.Host {
		return http.DefaultTransport.RoundTrip(req)
	}
	return r.rest.RoundTrip(req)
}

func weekly(day int) Event {
	start := time.Date(2026, 3, day, 15, 0, 0, 0, time.UTC)
	return Event{ID: fmt.Sprintf("occ-%d", day), Subject: "Planning sync", Start: start, End: start.Add(45 * time.Minute), Response: "accepted",
		JoinURL:   "https://teams.microsoft.com/l/meetup-join/19%3ameeting_ABC%40thread.v2/0?context=%7b%22Oid%22%3a%22org-1%22%7d",
		Organizer: person{Name: "Robin Vale", Email: "robin.vale@corp.example"}, Invitees: []person{{Name: "Kai Moreno", Email: "kai.moreno@corp.example"}}}
}

func readDrops(t *testing.T, dir string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	files, _ := filepath.Glob(filepath.Join(dir, TeamsSource, "*.json"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var env map[string]any
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatal(err)
		}
		out[env["kind"].(string)+":"+env["ref"].(string)] = env
	}
	return out
}

func TestSyncTeamsInvitedByName(t *testing.T) {
	ms := &fakeMS{}
	c := teamsClient(t, ms)
	dir := t.TempDir()
	now := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	future := weekly(16) // hasn't happened: left alone
	o := TeamsOptions{Chat: true, OwnerName: "Owner Name", Recorded: func(s, e time.Time) bool { return false }}
	rep, err := SyncTeams(context.Background(), c, dir, []Event{weekly(2), weekly(9), future}, o, now)
	if err != nil {
		t.Fatal(err)
	}
	// 2 Mar: the owner speaks, kept. 9 Mar: owner absent, passed over (unattended off).
	if rep.Meetings != 2 || rep.Transcripts != 1 || rep.Skipped != 1 {
		t.Errorf("report %+v", rep)
	}
	drops := readDrops(t, dir)
	var meeting map[string]any
	for k, v := range drops {
		if strings.HasPrefix(k, "meeting:") {
			meeting = v
		}
	}
	if meeting == nil {
		t.Fatalf("no meeting drop in %v", drops)
	}
	rec := meeting["record"].(map[string]any)
	if rec["title"] != "Planning sync" || rec["start"] != "2026-03-02T15:01:00Z" || len(rec["turns"].([]any)) != 3 || len(rec["attendees"].([]any)) != 2 {
		t.Errorf("drop record %v", rec)
	}
	if meeting["source"] != TeamsSource {
		t.Errorf("source %v", meeting["source"])
	}
	// The meeting chat: the message during the meeting, not the old one or
	// the system event, HTML stripped.
	var chat map[string]any
	for k, v := range drops {
		if strings.HasPrefix(k, "chat:") {
			chat = v
		}
	}
	if chat == nil {
		t.Fatalf("no chat drop")
	}
	msgs := chat["record"].(map[string]any)["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "Deck is here" {
		t.Errorf("chat %v", msgs)
	}
	// A second sync reads no transcript content again: kept ones have files,
	// passed-over ones are on the skip list.
	before := ms.hits["graph.microsoft.com/v1.0/me/onlineMeetings/MEETING1/transcripts/T-ATTENDED/content"] + ms.hits["graph.microsoft.com/v1.0/me/onlineMeetings/MEETING1/transcripts/T-SKIPPED/content"]
	if _, err := SyncTeams(context.Background(), c, dir, []Event{weekly(2), weekly(9)}, o, now); err != nil {
		t.Fatal(err)
	}
	after := ms.hits["graph.microsoft.com/v1.0/me/onlineMeetings/MEETING1/transcripts/T-ATTENDED/content"] + ms.hits["graph.microsoft.com/v1.0/me/onlineMeetings/MEETING1/transcripts/T-SKIPPED/content"]
	if after != before {
		t.Errorf("content fetched again: %d → %d", before, after)
	}
	// Unattended on (and the skip list cleared): 9 Mar is kept too.
	ClearSkipped(dir)
	o.Unattended = true
	rep, _ = SyncTeams(context.Background(), c, dir, []Event{weekly(9)}, o, now)
	if rep.Transcripts != 1 {
		t.Errorf("unattended %+v", rep)
	}
}

func TestSyncTeamsInvitedThroughList(t *testing.T) {
	ms := &fakeMS{listRefused: true}
	c := teamsClient(t, ms)
	dir := t.TempDir()
	now := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	ev := weekly(2)
	ev.Subject = "All-hands"
	// Not attended and unattended off: nothing is read from SharePoint.
	o := TeamsOptions{OwnerName: "Owner Name"}
	if rep, err := SyncTeams(context.Background(), c, dir, []Event{ev}, o, now); err != nil || rep.Transcripts != 0 || rep.Refused != 0 {
		t.Fatalf("not attended: %+v %v", rep, err)
	}
	if ms.hits["corp-my.sharepoint.com/_api/v2.1/drives/DRIVE1/items/ITEM1/media/transcripts"] != 0 {
		t.Error("SharePoint read for a meeting the owner skipped")
	}
	o.Unattended = true
	rep, err := SyncTeams(context.Background(), c, dir, []Event{ev}, o, now)
	if err != nil || rep.Refused != 1 || rep.Transcripts != 1 {
		t.Fatalf("through a list: %+v %v", rep, err)
	}
	for _, d := range readDrops(t, dir) {
		rec := d["record"].(map[string]any)
		turns := rec["turns"].([]any)
		if len(turns) != 1 || turns[0].(map[string]any)["speaker"] != "Kai Moreno" || rec["title"] != "All-hands" {
			t.Errorf("recording transcript %v", rec)
		}
	}
}
