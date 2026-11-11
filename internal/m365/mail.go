package m365

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Sources of the mail and chat drops.
const (
	MailSource = "m365-mail"
	ChatSource = "m365-chat"
)

// MailReport counts what a mail sync wrote.
type MailReport struct{ Seen, Written int }

// watermark remembers how far a sync has read, per source, in the inbox
// folder (a name the inbox reader ignores).
func watermark(dir string) (time.Time, func(time.Time)) {
	path := filepath.Join(dir, ".since")
	var t time.Time
	if b, err := os.ReadFile(path); err == nil {
		t, _ = time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	}
	return t, func(nt time.Time) { _ = os.WriteFile(path, []byte(nt.UTC().Format(time.RFC3339)), 0o600) }
}

// SyncMail writes the owner's mail received or sent since the last sync
// (on the first, since first) as email drops under dir/m365-mail. With
// focusedOnly, mail Outlook files as "Other" (newsletters, notifications)
// is left out. Bodies are plain text, as Outlook renders them.
func SyncMail(ctx context.Context, c *Client, dir string, first time.Time, focusedOnly bool, now time.Time) (MailReport, error) {
	var rep MailReport
	out := filepath.Join(dir, MailSource)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return rep, err
	}
	since, save := watermark(out)
	if since.IsZero() || since.Before(first) {
		since = first
	}
	q := url.Values{
		"$filter":  {"lastModifiedDateTime ge " + since.UTC().Format(time.RFC3339)},
		"$select":  {"id,conversationId,subject,from,toRecipients,ccRecipients,sentDateTime,body,sensitivity,inferenceClassification,isDraft"},
		"$top":     {"50"},
		"$orderby": {"lastModifiedDateTime"},
	}
	path := "/v1.0/me/messages?" + q.Encode()
	hdr := http.Header{"Prefer": {`outlook.body-content-type="text"`}}
	for path != "" {
		b, err := c.Get(ctx, path, hdr)
		if err != nil {
			return rep, err
		}
		var page struct {
			Value []struct {
				ID, ConversationID, Subject, Sensitivity, InferenceClassification string
				IsDraft                                                           bool
				SentDateTime                                                      time.Time
				From                                                              *struct{ EmailAddress emailAddress }
				ToRecipients, CcRecipients                                        []struct{ EmailAddress emailAddress }
				Body                                                              struct{ Content string }
			} `json:"value"`
			Next string `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			return rep, err
		}
		for _, m := range page.Value {
			rep.Seen++
			if m.IsDraft || m.SentDateTime.IsZero() || focusedOnly && strings.EqualFold(m.InferenceClassification, "other") {
				continue
			}
			addr := func(rs []struct{ EmailAddress emailAddress }) []person {
				var ps []person
				for _, r := range rs {
					ps = append(ps, person{Name: r.EmailAddress.Name, Email: strings.ToLower(r.EmailAddress.Address)})
				}
				return ps
			}
			var from person
			if m.From != nil {
				from = person{Name: m.From.EmailAddress.Name, Email: strings.ToLower(m.From.EmailAddress.Address)}
			}
			rec := map[string]any{"subject": m.Subject, "from": from, "to": addr(m.ToRecipients), "cc": addr(m.CcRecipients),
				"sentAt": m.SentDateTime.UTC().Format(time.RFC3339), "body": m.Body.Content, "threadId": m.ConversationID, "sensitivity": m.Sensitivity}
			wrote, err := WriteJSONIfChanged(filepath.Join(out, "m-"+shortHash(m.ID)+".json"), envelope{Kind: "email", Source: MailSource, Ref: m.ID, Record: rec})
			if err != nil {
				return rep, err
			}
			if wrote {
				rep.Written++
			}
		}
		path = page.Next
	}
	save(now)
	return rep, nil
}

// ChatReport counts what a chat sync wrote.
type ChatReport struct{ Chats, Days, Messages int }

// SyncChats writes the owner's Teams chats (one-to-one and group; meeting
// chats are written with their meeting) as chat drops: one per chat and
// day, so a long-running chat becomes many small items. Only chats with
// activity since the last sync (on the first, since first) are read.
func SyncChats(ctx context.Context, c *Client, dir string, first time.Time, now time.Time) (ChatReport, error) {
	var rep ChatReport
	out := filepath.Join(dir, ChatSource)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return rep, err
	}
	since, save := watermark(out)
	if since.IsZero() || since.Before(first) {
		since = first
	}
	type chat struct {
		ID, Topic, ChatType string
		LastUpdated         time.Time `json:"lastUpdatedDateTime"`
		Members             []struct {
			DisplayName string `json:"displayName"`
			Email       string `json:"email"`
		} `json:"members"`
	}
	var chats []chat
	err := c.Pages(ctx, "/v1.0/me/chats?$expand=members&$top=50", func(raw json.RawMessage) error {
		var ch chat
		if err := json.Unmarshal(raw, &ch); err != nil {
			return err
		}
		if ch.ChatType != "meeting" && !ch.LastUpdated.Before(since) {
			chats = append(chats, ch)
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	for _, ch := range chats {
		rep.Chats++
		msgs, err := chatMessages(ctx, c, ch.ID, since, now)
		if err != nil {
			if IsNotFound(err) {
				continue
			}
			return rep, err
		}
		byDay := map[string][]map[string]any{}
		for _, m := range msgs {
			if m.Type != "message" || m.Deleted != nil || m.From == nil || m.From.User == nil {
				continue
			}
			text := m.Body.Content
			if m.Body.ContentType == "html" {
				text = strings.TrimSpace(htmlText(text))
			}
			if text == "" {
				continue
			}
			day := m.SentAt.UTC().Format("2006-01-02")
			byDay[day] = append(byDay[day], map[string]any{"from": person{Name: m.From.User.DisplayName}, "sentAt": m.SentAt.UTC().Format(time.RFC3339), "text": text})
		}
		var members []person
		for _, mb := range ch.Members {
			members = append(members, person{Name: mb.DisplayName, Email: strings.ToLower(mb.Email)})
		}
		topic := ch.Topic
		if topic == "" {
			var names []string
			for _, mb := range members {
				names = append(names, mb.Name)
			}
			sort.Strings(names)
			topic = strings.Join(names, ", ")
		}
		for day, kept := range byDay {
			ref := ch.ID + "/" + day
			// Merge with what an earlier sync wrote for the same day.
			path := filepath.Join(out, "c-"+shortHash(ref)+".json")
			kept = mergeMessages(path, kept)
			rec := map[string]any{"chatId": ch.ID, "topic": topic, "members": members, "messages": kept}
			if _, err := WriteJSONIfChanged(path, envelope{Kind: "chat", Source: ChatSource, Ref: ref, Record: rec}); err != nil {
				return rep, err
			}
			rep.Days++
			rep.Messages += len(kept)
		}
	}
	save(now)
	return rep, nil
}

func mergeMessages(path string, msgs []map[string]any) []map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return msgs
	}
	var env struct {
		Record struct {
			Messages []map[string]any `json:"messages"`
		} `json:"record"`
	}
	if json.Unmarshal(b, &env) != nil {
		return msgs
	}
	seen := map[string]bool{}
	var out []map[string]any
	for _, m := range append(env.Record.Messages, msgs...) {
		k, _ := json.Marshal(m)
		if !seen[string(k)] {
			seen[string(k)] = true
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a]["sentAt"].(string) < out[b]["sentAt"].(string) })
	return out
}
