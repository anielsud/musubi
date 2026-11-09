package m365

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TeamsSource is the inbox source of Teams transcripts and meeting chats.
const TeamsSource = "teams"

// TeamsOptions says what the Teams sync reads.
type TeamsOptions struct {
	// Unattended also keeps transcripts of meetings the owner didn't join.
	Unattended bool
	// Chat writes each meeting's chat (messages during the occurrence) as a
	// chat drop.
	Chat bool
	// OwnerName and OwnerEmail recognise the owner among speakers.
	OwnerName, OwnerEmail string
	// Recorded reports whether Tomoe recorded the owner between start and
	// end: the owner attended.
	Recorded func(start, end time.Time) bool
}

// TeamsReport counts what a sync did.
type TeamsReport struct {
	Meetings, Transcripts, Skipped, Chats, Refused int
}

// SyncTeams writes the transcripts (and meeting chats) of the events' Teams
// meetings that ended before now, as inbox drops under dir/teams. A
// transcript is fetched once: its file, or its id in the skip list, stops
// it being read again.
//
// A meeting is found by its join link. When the owner was invited by name,
// Graph lists the transcripts of every occurrence. When the owner was
// invited only through a distribution list, Graph refuses the lookup
// (3003); then the meeting chat names the recording, and the transcript
// kept with the recording is read from SharePoint.
func SyncTeams(ctx context.Context, c *Client, dir string, events []Event, o TeamsOptions, now time.Time) (TeamsReport, error) {
	var rep TeamsReport
	out := filepath.Join(dir, TeamsSource)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return rep, err
	}
	// What is passed over or settled depends on what is wanted: when the
	// options change (unattended or chat turned on), start over.
	opts := fmt.Sprintf("unattended=%v chat=%v", o.Unattended, o.Chat)
	if b, _ := os.ReadFile(filepath.Join(out, ".options")); string(b) != opts {
		_ = os.Remove(filepath.Join(out, ".skipped"))
		_ = os.Remove(filepath.Join(out, ".settled"))
		_ = os.WriteFile(filepath.Join(out, ".options"), []byte(opts), 0o600)
	}
	skip := loadSkip(out)
	defer skip.save()
	// Occurrences checked a day or more after they ended won't gain a
	// transcript or chat: they are settled and not read again.
	settled := loadSet(filepath.Join(out, ".settled"))
	defer settled.save()
	listed := map[string][]transcriptMeta{} // per meeting id: a series lists all occurrences
	for _, ev := range events {
		if ev.Cancelled || ev.JoinURL == "" || ev.End.After(now) || ev.Response == "declined" || settled.has(ev.ID) {
			continue
		}
		rep.Meetings++
		if now.After(ev.End.Add(24 * time.Hour)) {
			defer settled.add(ev.ID) // marked only when the whole sync gets this far
		}
		m, err := findMeeting(ctx, c, ev.JoinURL)
		if err != nil && !IsNotFound(err) {
			return rep, err
		}
		refused := err != nil // lookup refused: invited through a list, or not found
		var found []transcriptMeta
		if m.id != "" {
			ts, ok := listed[m.id]
			if !ok {
				var lerr error
				ts, lerr = listTranscripts(ctx, c, m.id)
				if lerr != nil && !IsNotFound(lerr) {
					return rep, lerr
				}
				refused = refused && lerr != nil
				listed[m.id] = ts
			}
			for _, t := range ts {
				if t.Start.After(ev.Start.Add(-time.Hour)) && t.Start.Before(ev.End.Add(3*time.Hour)) {
					found = append(found, t)
				}
			}
		}
		for _, t := range found {
			if skip.has(t.ID) || exists(filepath.Join(out, transcriptFile(t.ID))) {
				continue
			}
			turns, err := transcriptTurns(ctx, c, m.id, t.ID)
			if err != nil {
				if IsNotFound(err) {
					continue
				}
				return rep, err
			}
			if kept, err := keepTranscript(out, ev, t, turns, o, skip); err != nil {
				return rep, err
			} else if kept {
				rep.Transcripts++
			} else {
				rep.Skipped++
			}
		}
		// Invited through a list: the chat and the recording.
		if len(found) == 0 && refused && m.thread != "" && (o.Unattended || attendedByTime(o, ev)) {
			rep.Refused++
			if err := recordingTranscripts(ctx, c, out, ev, m.thread, o, skip, &rep); err != nil && !IsNotFound(err) {
				return rep, err
			}
		}
		if o.Chat && m.thread != "" && (o.Unattended || attendedByTime(o, ev) || len(found) > 0) {
			if wrote, err := writeMeetingChat(ctx, c, out, ev, m.thread); err != nil && !IsNotFound(err) {
				return rep, err
			} else if wrote {
				rep.Chats++
			}
		}
	}
	return rep, nil
}

type meeting struct{ id, thread string }

// joinParts reads a classic join link
// (…/l/meetup-join/19%3ameeting_…%40thread.v2/0?context={"Tid":…,"Oid":…}):
// the chat thread and the organizer's object id.
var threadRE = regexp.MustCompile(`19:[^/?]+@thread\.(v2|skype)`)

func joinParts(joinURL string) (thread, oid string) {
	dec, err := url.PathUnescape(joinURL)
	if err != nil {
		dec = joinURL
	}
	if u, err := url.QueryUnescape(dec); err == nil {
		dec = u
	}
	thread = threadRE.FindString(dec)
	if i := strings.Index(dec, `"Oid":"`); i >= 0 {
		rest := dec[i+7:]
		if j := strings.IndexByte(rest, '"'); j > 0 {
			oid = rest[:j]
		}
	}
	return thread, oid
}

// findMeeting looks the meeting up by its join link; if Graph refuses (the
// owner was invited through a list), the id is built from the link, which
// Graph accepts for transcripts when the owner was invited by name.
func findMeeting(ctx context.Context, c *Client, joinURL string) (meeting, error) {
	thread, oid := joinParts(joinURL)
	m := meeting{thread: thread}
	var res struct {
		Value []struct {
			ID       string `json:"id"`
			ChatInfo struct {
				ThreadID string `json:"threadId"`
			} `json:"chatInfo"`
		} `json:"value"`
	}
	filter := url.Values{"$filter": {"JoinWebUrl eq '" + strings.ReplaceAll(joinURL, "'", "''") + "'"}}
	err := c.GetJSON(ctx, "/v1.0/me/onlineMeetings?"+filter.Encode(), &res)
	if err == nil && len(res.Value) > 0 {
		m.id = res.Value[0].ID
		if t := res.Value[0].ChatInfo.ThreadID; t != "" {
			m.thread = t
		}
		return m, nil
	}
	if err != nil && !IsNotFound(err) {
		return m, err
	}
	if thread != "" && oid != "" {
		m.id = base64.StdEncoding.EncodeToString([]byte("1*" + oid + "*0**" + thread))
	}
	return m, err
}

type transcriptMeta struct {
	ID         string
	Start, End time.Time
}

func listTranscripts(ctx context.Context, c *Client, meetingID string) ([]transcriptMeta, error) {
	var out []transcriptMeta
	err := c.Pages(ctx, "/v1.0/me/onlineMeetings/"+url.PathEscape(meetingID)+"/transcripts", func(raw json.RawMessage) error {
		var t struct {
			ID      string    `json:"id"`
			Created time.Time `json:"createdDateTime"`
			End     time.Time `json:"endDateTime"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		out = append(out, transcriptMeta{ID: t.ID, Start: t.Created.UTC(), End: t.End.UTC()})
		return nil
	})
	return out, err
}

// turn is one speaker turn of a transcript.
type turn struct {
	Speaker string  `json:"speaker"`
	Email   string  `json:"email"`
	Offset  float64 `json:"offset"`
	Text    string  `json:"text"`
}

func transcriptTurns(ctx context.Context, c *Client, meetingID, tid string) ([]turn, error) {
	b, err := c.Get(ctx, "/v1.0/me/onlineMeetings/"+url.PathEscape(meetingID)+"/transcripts/"+url.PathEscape(tid)+"/content?$format=text/vtt", nil)
	if err != nil {
		return nil, err
	}
	return parseVTT(string(b)), nil
}

var (
	cueTimeRE = regexp.MustCompile(`^(\d+):(\d\d):(\d\d)[.,](\d+)\s+-->`)
	voiceRE   = regexp.MustCompile(`(?s)<v ([^>]*)>(.*?)(?:</v>|$)`)
)

// parseVTT turns a Teams WebVTT transcript into turns: one per voice span,
// consecutive cues of one speaker joined.
func parseVTT(vtt string) []turn {
	var out []turn
	for _, block := range strings.Split(strings.ReplaceAll(vtt, "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		var at float64 = -1
		var body []string
		for _, l := range lines {
			if m := cueTimeRE.FindStringSubmatch(l); m != nil {
				at = hms(m[1], m[2], m[3], m[4])
				continue
			}
			if at >= 0 {
				body = append(body, l)
			}
		}
		if at < 0 || len(body) == 0 {
			continue
		}
		text := strings.Join(body, " ")
		spans := voiceRE.FindAllStringSubmatch(text, -1)
		if spans == nil {
			spans = [][]string{{"", "", text}}
		}
		for _, s := range spans {
			speaker, t := strings.TrimSpace(s[1]), strings.TrimSpace(s[2])
			if t == "" {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Speaker == speaker && at-out[n-1].Offset < 60 {
				out[n-1].Text += " " + t
				continue
			}
			out = append(out, turn{Speaker: speaker, Offset: at, Text: t})
		}
	}
	return out
}

func hms(h, m, s, frac string) float64 {
	hh, _ := strconv.Atoi(h)
	mm, _ := strconv.Atoi(m)
	ss, _ := strconv.Atoi(s)
	f, _ := strconv.ParseFloat("0."+frac, 64)
	return float64(hh*3600+mm*60+ss) + f
}

// keepTranscript writes the transcript as a meeting drop unless the owner
// didn't attend and unattended meetings aren't wanted (then it's noted, so
// it isn't fetched again).
func keepTranscript(out string, ev Event, t transcriptMeta, turns []turn, o TeamsOptions, skip *skipList) (bool, error) {
	if len(turns) == 0 {
		skip.add(t.ID)
		return false, nil
	}
	if !o.Unattended && !attended(o, ev, t, turns) {
		skip.add(t.ID)
		return false, nil
	}
	return true, writeTranscript(out, ev, t, turns)
}

func writeTranscript(out string, ev Event, t transcriptMeta, turns []turn) error {
	start, end := t.Start, t.End
	if start.IsZero() {
		start = ev.Start
	}
	if end.IsZero() || end.Before(start) {
		end = start.Add(time.Duration(turns[len(turns)-1].Offset) * time.Second)
	}
	people := append([]person{}, ev.Invitees...)
	if ev.Organizer.Email != "" {
		people = append([]person{ev.Organizer}, people...)
	}
	rec := map[string]any{"title": ev.Subject, "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339), "attendees": people, "turns": turns}
	_, err := WriteJSONIfChanged(filepath.Join(out, transcriptFile(t.ID)), envelope{Kind: "meeting", Source: TeamsSource, Ref: "teams-" + shortHash(t.ID), Record: rec})
	return err
}

func attendedByTime(o TeamsOptions, ev Event) bool {
	return o.Recorded != nil && o.Recorded(ev.Start, ev.End)
}

func attended(o TeamsOptions, ev Event, t transcriptMeta, turns []turn) bool {
	if o.Recorded != nil {
		s, e := t.Start, t.End
		if s.IsZero() {
			s, e = ev.Start, ev.End
		}
		if o.Recorded(s, e) {
			return true
		}
	}
	name := strings.ToLower(strings.TrimSpace(o.OwnerName))
	for _, tr := range turns {
		if name != "" && strings.ToLower(tr.Speaker) == name || o.OwnerEmail != "" && strings.EqualFold(tr.Email, o.OwnerEmail) {
			return true
		}
	}
	return false
}

// recordingTranscripts reads the transcript kept with the occurrence's
// recording: the meeting chat's recording event names the file, and
// SharePoint's media API has its transcript with speaker names.
func recordingTranscripts(ctx context.Context, c *Client, out string, ev Event, thread string, o TeamsOptions, skip *skipList, rep *TeamsReport) error {
	msgs, err := chatMessages(ctx, c, thread, ev.Start.Add(-30*time.Minute), ev.End.Add(3*time.Hour))
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if m.EventDetail == nil || !strings.HasSuffix(m.EventDetail.Type, "callRecordingEventMessageDetail") || m.EventDetail.Status != "success" || m.EventDetail.URL == "" {
			continue
		}
		share := "u!" + strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(m.EventDetail.URL)), "=")
		var item struct {
			ID     string `json:"id"`
			Parent struct {
				DriveID string `json:"driveId"`
			} `json:"parentReference"`
		}
		if err := c.GetJSON(ctx, "/v1.0/shares/"+share+"/driveItem", &item); err != nil {
			if IsNotFound(err) {
				continue
			}
			return err
		}
		u, err := url.Parse(m.EventDetail.URL)
		if err != nil || !strings.HasSuffix(u.Host, ".sharepoint.com") {
			continue
		}
		base := "https://" + u.Host + "/_api/v2.1/drives/" + url.PathEscape(item.Parent.DriveID) + "/items/" + url.PathEscape(item.ID) + "/media/transcripts"
		var list struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
		}
		if err := c.GetJSON(ctx, base, &list); err != nil {
			if IsNotFound(err) {
				continue
			}
			return err
		}
		for _, tr := range list.Value {
			key := "sp:" + item.ID + ":" + tr.ID
			if skip.has(key) || exists(filepath.Join(out, transcriptFile(key))) {
				continue
			}
			b, err := c.Get(ctx, base+"/"+url.PathEscape(tr.ID)+"/streamContent?format=json", nil)
			if err != nil {
				if IsNotFound(err) {
					continue
				}
				return err
			}
			turns := spTurns(b)
			t := transcriptMeta{ID: key, Start: m.CallStart(ev), End: m.SentAt}
			if kept, err := keepTranscript(out, ev, t, turns, o, skip); err != nil {
				return err
			} else if kept {
				rep.Transcripts++
			} else {
				rep.Skipped++
			}
		}
	}
	return nil
}

// spTurns reads SharePoint's JSON transcript (entries with
// speakerDisplayName and startOffset "hh:mm:ss.fffffff").
func spTurns(b []byte) []turn {
	var doc struct {
		Entries []struct {
			Speaker string `json:"speakerDisplayName"`
			Start   string `json:"startOffset"`
			Text    string `json:"text"`
		} `json:"entries"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	var out []turn
	for _, e := range doc.Entries {
		at := 0.0
		if m := regexp.MustCompile(`^(\d+):(\d\d):(\d\d)(?:\.(\d+))?`).FindStringSubmatch(e.Start); m != nil {
			at = hms(m[1], m[2], m[3], m[4]+"0")
		}
		t := strings.TrimSpace(e.Text)
		if t == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Speaker == e.Speaker && at-out[n-1].Offset < 60 {
			out[n-1].Text += " " + t
			continue
		}
		out = append(out, turn{Speaker: e.Speaker, Offset: at, Text: t})
	}
	return out
}

type chatMessage struct {
	ID      string    `json:"id"`
	Type    string    `json:"messageType"`
	SentAt  time.Time `json:"createdDateTime"`
	Deleted *string   `json:"deletedDateTime"`
	From    *struct {
		User *struct {
			DisplayName string `json:"displayName"`
		} `json:"user"`
	} `json:"from"`
	Body struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	EventDetail *struct {
		Type   string `json:"@odata.type"`
		Status string `json:"callRecordingStatus"`
		URL    string `json:"callRecordingUrl"`
	} `json:"eventDetail"`
	started time.Time
}

// CallStart is when the call this recording belongs to started: the
// recording event's own time less nothing better known.
func (m chatMessage) CallStart(ev Event) time.Time {
	if !m.started.IsZero() {
		return m.started
	}
	return ev.Start
}

// chatMessages reads a meeting chat's messages sent between from and to.
// The API doesn't promise date order, so paging stops only once a page's
// oldest message is well before from.
func chatMessages(ctx context.Context, c *Client, thread string, from, to time.Time) ([]chatMessage, error) {
	var out []chatMessage
	// Newest first and only before the window's end, so a long-running
	// chat isn't paged through from today back to the meeting.
	q := url.Values{"$top": {"50"}, "$orderby": {"createdDateTime desc"}, "$filter": {"createdDateTime lt " + to.UTC().Format(time.RFC3339)}}
	path := "/v1.0/chats/" + url.PathEscape(thread) + "/messages?" + q.Encode()
	plain := false
	for pages := 0; path != "" && pages < 40; pages++ {
		var page struct {
			Value []chatMessage `json:"value"`
			Next  string        `json:"@odata.nextLink"`
		}
		if err := c.GetJSON(ctx, path, &page); err != nil {
			var ge *Error
			if !plain && pages == 0 && errors.As(err, &ge) && ge.Status == http.StatusBadRequest {
				// The service refused the filter: page plainly.
				plain, path, pages = true, "/v1.0/chats/"+url.PathEscape(thread)+"/messages?$top=50", -1
				continue
			}
			return nil, err
		}
		oldest := time.Time{}
		for _, m := range page.Value {
			at := m.SentAt.UTC()
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
			if !at.Before(from) && !at.After(to) {
				out = append(out, m)
			}
		}
		if !oldest.IsZero() && (oldest.Before(from.Add(-48*time.Hour)) || !plain && oldest.Before(from)) {
			break
		}
		path = page.Next
	}
	sort.Slice(out, func(a, b int) bool { return out[a].SentAt.Before(out[b].SentAt) })
	return out, nil
}

var tagRE = regexp.MustCompile(`<[^>]+>`)

// writeMeetingChat writes what was written in the meeting chat during the
// occurrence (an hour either side) as a chat drop. System events and
// deleted messages are left out.
func writeMeetingChat(ctx context.Context, c *Client, out string, ev Event, thread string) (bool, error) {
	msgs, err := chatMessages(ctx, c, thread, ev.Start.Add(-time.Hour), ev.End.Add(time.Hour))
	if err != nil {
		return false, err
	}
	type msg struct {
		From   person `json:"from"`
		SentAt string `json:"sentAt"`
		Text   string `json:"text"`
	}
	var kept []msg
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
		kept = append(kept, msg{From: person{Name: m.From.User.DisplayName}, SentAt: m.SentAt.UTC().Format(time.RFC3339), Text: text})
	}
	if len(kept) == 0 {
		return false, nil
	}
	rec := map[string]any{"chatId": thread, "topic": ev.Subject + " (meeting chat)", "members": ev.Invitees, "messages": kept}
	return WriteJSONIfChanged(filepath.Join(out, "chat-"+shortHash(ev.ID)+".json"), envelope{Kind: "chat", Source: TeamsSource, Ref: "chat-" + shortHash(ev.ID), Record: rec})
}

func htmlText(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "</p>", "\n", "&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	return tagRE.ReplaceAllString(s, "")
}

func transcriptFile(id string) string { return "t-" + shortHash(id) + ".json" }

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:10])
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// skipList remembers transcripts read and not kept (empty, or a meeting
// the owner didn't join while unattended is off), so they aren't fetched
// again. Turning unattended on later reads them: clear the list.
type skipList struct {
	path  string
	ids   map[string]bool
	dirty bool
}

func loadSkip(dir string) *skipList { return loadSet(filepath.Join(dir, ".skipped")) }

func loadSet(path string) *skipList {
	s := &skipList{path: path, ids: map[string]bool{}}
	if b, err := os.ReadFile(s.path); err == nil {
		var ids []string
		if json.Unmarshal(b, &ids) == nil {
			for _, id := range ids {
				s.ids[id] = true
			}
		}
	}
	return s
}

func (s *skipList) has(id string) bool { return s.ids[shortHash(id)] }
func (s *skipList) add(id string)      { s.ids[shortHash(id)], s.dirty = true, true }

func (s *skipList) save() {
	if !s.dirty {
		return
	}
	ids := make([]string, 0, len(s.ids))
	for id := range s.ids {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if b, err := json.Marshal(ids); err == nil {
		_ = os.WriteFile(s.path, b, 0o600)
	}
}

// ClearSkipped forgets which transcripts were passed over and which
// occurrences are settled, so the next sync reads them again. (Changing
// the options does this by itself.)
func ClearSkipped(dir string) error {
	for _, f := range []string{".skipped", ".settled"} {
		if err := os.Remove(filepath.Join(dir, TeamsSource, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
