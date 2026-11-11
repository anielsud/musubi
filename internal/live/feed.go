// Package live drives the side panel during a meeting: it follows Tomoe's
// live feed (docs/tomoe-live-feed.md), keeps the conversation so far, turns
// the recent part into suggested queries, and briefs on the top ones.
package live

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"time"
)

// Segment is a Tomoe transcript line as the feed sends it.
type Segment struct {
	ID      string  `json:"id"`
	Speaker string  `json:"speaker"`
	Text    string  `json:"text"`
	Start   float64 `json:"start_time"`
	End     float64 `json:"end_time"`
	Source  string  `json:"source"`
	// Status: "live" (draft), "pending", "" (final), "removed".
	Status string `json:"status"`
	// Words carry per-word times (seconds from the session start).
	Words []Word `json:"words,omitempty"`
}

type Word struct {
	T string  `json:"t"`
	S float64 `json:"s"`
	E float64 `json:"e"`
}

type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Platform  string    `json:"platform"`
	CreatedAt time.Time `json:"created_at"`
	// Invited are the other people on the calendar event under way, set
	// here (Tomoe doesn't send them).
	Invited []string `json:"invited,omitempty"`
}

// Event is one feed message.
type Event struct {
	V         int      `json:"v"`
	Type      string   `json:"type"` // hello, session, segment
	App       string   `json:"app,omitempty"`
	Version   string   `json:"version,omitempty"`
	State     string   `json:"state,omitempty"` // started, stopped, saved
	Session   *Session `json:"session,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Segment   *Segment `json:"segment,omitempty"`
}

// Follow connects to Tomoe's socket and delivers events until ctx ends,
// reconnecting every few seconds. onConn reports connection changes.
func Follow(ctx context.Context, path string, onEvent func(Event), onConn func(bool)) {
	for ctx.Err() == nil {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		onConn(true)
		go func() {
			<-ctx.Done()
			conn.Close()
		}()
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 1<<20), 1<<22)
		for sc.Scan() {
			var ev Event
			if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.V == 1 {
				onEvent(ev)
			}
		}
		conn.Close()
		onConn(false)
	}
}
