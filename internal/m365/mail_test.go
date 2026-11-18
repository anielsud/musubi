package m365

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeMailChat struct{ mailFilter, mailExpand []string }

func (f *fakeMailChat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/v1.0/me/messages":
		f.mailFilter = append(f.mailFilter, r.URL.Query().Get("$filter"))
		if r.Header.Get("Prefer") != `outlook.body-content-type="text"` {
			w.WriteHeader(400)
			return
		}
		// As Graph: a message has no sensitivity property; selecting one
		// fails the whole request.
		if strings.Contains(r.URL.Query().Get("$select"), "sensitivity") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"RequestBroker--ParseUri","message":"Could not find a property named 'sensitivity' on type 'Microsoft.OutlookServices.Message'."}}`)
			return
		}
		f.mailExpand = append(f.mailExpand, r.URL.Query().Get("$expand"))
		msg := func(id, cls string, draft bool) map[string]any {
			m := map[string]any{"id": id, "conversationId": "conv-1", "subject": "Plan " + id, "inferenceClassification": cls, "isDraft": draft,
				"sentDateTime": "2026-03-02T10:00:00Z", "from": map[string]any{"emailAddress": map[string]any{"name": "Robin Vale", "address": "Robin.Vale@corp.example"}},
				"toRecipients": []any{map[string]any{"emailAddress": map[string]any{"name": "Owner Name", "address": "owner@corp.example"}}},
				"body":         map[string]any{"content": "Here is the plan.\n\nOn Monday Owner wrote:\n> earlier"}}
			if id == "m1" {
				m["singleValueExtendedProperties"] = []any{map[string]any{"id": "Integer 0x36", "value": "2"}}
			}
			return m
		}
		json.NewEncoder(w).Encode(map[string]any{"value": []any{msg("m1", "focused", false), msg("m2", "other", false), msg("m3", "focused", true)}})
	case p == "/v1.0/me/chats":
		json.NewEncoder(w).Encode(map[string]any{"value": []any{
			map[string]any{"id": "19:dm@unq.gbl.spaces", "chatType": "oneOnOne", "lastUpdatedDateTime": "2026-03-03T12:00:00Z", "members": []any{map[string]any{"displayName": "Kai Moreno", "email": "kai.moreno@corp.example"}, map[string]any{"displayName": "Owner Name", "email": "owner@corp.example"}}},
			map[string]any{"id": "19:meeting_x@thread.v2", "chatType": "meeting", "lastUpdatedDateTime": "2026-03-03T12:00:00Z"},
			map[string]any{"id": "19:old@thread.v2", "chatType": "group", "lastUpdatedDateTime": "2026-01-01T12:00:00Z"},
		}})
	case strings.HasPrefix(p, "/v1.0/chats/19:dm@unq.gbl.spaces/messages"):
		m := func(id, at, text string) map[string]any {
			return map[string]any{"id": id, "messageType": "message", "createdDateTime": at, "from": map[string]any{"user": map[string]any{"displayName": "Kai Moreno"}}, "body": map[string]any{"contentType": "text", "content": text}}
		}
		json.NewEncoder(w).Encode(map[string]any{"value": []any{m("a", "2026-03-02T09:00:00Z", "morning"), m("b", "2026-03-03T09:00:00Z", "next day")}})
	default:
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error":{"code":"NotFound","message":"%s"}}`, p)
	}
}

func TestSyncMailAndChats(t *testing.T) {
	f := &fakeMailChat{}
	c := teamsClient(t, &fakeMS{})
	c.HTTP = &http.Client{Transport: routed{f}}
	dir := t.TempDir()
	first, now := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	rep, err := SyncMail(context.Background(), c, dir, first, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Seen != 3 || rep.Written != 1 {
		t.Errorf("mail %+v (draft and Other left out)", rep)
	}
	if len(f.mailExpand) == 0 || !strings.Contains(f.mailExpand[0], "0x0036") {
		t.Errorf("expand %v (Outlook's sensitivity is asked for as PR_SENSITIVITY)", f.mailExpand)
	}
	var drop struct{ Record struct{ Sensitivity string } }
	if b, err := os.ReadFile(filepath.Join(dir, MailSource, "m-"+shortHash("m1")+".json")); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(b, &drop); err != nil || drop.Record.Sensitivity != "private" {
		t.Errorf("sensitivity %q, %v (PR_SENSITIVITY 2 is private)", drop.Record.Sensitivity, err)
	}
	if _, err := SyncMail(context.Background(), c, dir, first, true, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(f.mailFilter) != 2 || !strings.Contains(f.mailFilter[0], "2026-02-20") || !strings.Contains(f.mailFilter[1], "2026-03-04T00:00:00Z") {
		t.Errorf("filters %v (second sync resumes from the first's end)", f.mailFilter)
	}
	cr, err := SyncChats(context.Background(), c, dir, first, now)
	if err != nil {
		t.Fatal(err)
	}
	if cr.Chats != 1 || cr.Days != 2 || cr.Messages != 2 {
		t.Errorf("chats %+v (meeting chat and stale chat left out, one item per day)", cr)
	}
	// A later sync of the same day merges, not duplicates.
	if cr, _ := SyncChats(context.Background(), c, dir, first, now.Add(time.Hour)); cr.Messages != 0 && cr.Messages != 2 {
		t.Errorf("second chat sync %+v", cr)
	}
}
