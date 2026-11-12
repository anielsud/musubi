package recording

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// A feed client gets the recording session's lines as a snapshot, then
// lines as they are applied -- including a stopped session's late
// refinements -- and never a version older than the one the session
// kept.
func TestLiveFeedSnapshotAndAppliedSegments(t *testing.T) {
	dir, err := os.MkdirTemp("", "lf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	a := newTestRecorder(t)
	a.currentSess = &session.Session{
		ID: "current", Title: "Test meeting", CreatedAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
		Segments: []session.Segment{{ID: "seg-1", Text: "first line", StartTime: 1}, {ID: "seg-2", Text: "second line", StartTime: 3, Status: "pending"}},
	}
	srv, err := livefeed.Listen(filepath.Join(dir, "live.sock"), livefeed.Options{Snapshot: a.liveFeedSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	a.liveFeed = srv
	defer a.stopLiveFeed()

	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	type msg struct {
		Type      string          `json:"type"`
		State     string          `json:"state"`
		SessionID string          `json:"session_id"`
		Session   json.RawMessage `json:"session"`
		Segment   session.Segment `json:"segment"`
	}
	// next skips the Musubi app's messages (status, diag...) as an older
	// reader would, counting them in others.
	others := map[string]int{}
	next := func() msg {
		t.Helper()
		for {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if !sc.Scan() {
				t.Fatalf("no message: %v", sc.Err())
			}
			var m msg
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatal(err)
			}
			switch m.Type {
			case "hello", "session", "segment":
				return m
			}
			others[m.Type]++
		}
	}

	if m := next(); m.Type != "hello" {
		t.Fatalf("first message %+v", m)
	}
	if m := next(); m.Type != "session" || m.State != "started" {
		t.Fatalf("second message %+v", m)
	}
	for _, want := range []string{"first line", "second line"} {
		if m := next(); m.Type != "segment" || m.SessionID != "current" || m.Segment.Text != want {
			t.Fatalf("snapshot line %+v, want %q", m, want)
		}
	}
	for srv.Clients() != 1 {
		time.Sleep(5 * time.Millisecond)
	}
	// The snapshot's status comes after its lines: seen by the first live
	// line's read.

	stopped := &session.Session{ID: "stopped"}
	a.applySegment(stopped, nil, session.Segment{ID: "seg-7", Text: "Late refinement.", StartTime: 9})
	// A draft arriving after the final text: the session keeps the final
	// text, and so must the feed.
	a.applySegment(stopped, nil, session.Segment{ID: "seg-7", Text: "late draft", StartTime: 9, Status: "pending"})
	for i := 0; i < 2; i++ {
		m := next()
		if m.SessionID != "stopped" || m.Segment.ID != "seg-7" || m.Segment.Text != "Late refinement." || m.Segment.Status != "" {
			t.Errorf("applied line %d = %+v, want the stopped session's final text", i, m)
		}
	}
	if others["status"] != 1 || others["diag"] != 1 {
		t.Errorf("other messages %v, want the snapshot's status and the first line's diag", others)
	}
}
