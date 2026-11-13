package session

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func exportSample(platform string) *Session {
	return &Session{
		Title:     "Roadmap review",
		Platform:  platform,
		CreatedAt: time.Date(2026, 2, 3, 14, 5, 9, 0, time.UTC),
		Duration:  3725.4,
		Segments: []Segment{
			{Speaker: "Person 1", Text: "Shall we start?", StartTime: 0.4, EndTime: 1.8},
			{Speaker: "You", Text: "Yes.", StartTime: 2.5, EndTime: 3.1},
			{Speaker: "You", Text: "First item.", StartTime: 3.5, EndTime: 4.25},
			{Speaker: "Person 1", Text: "", StartTime: 61, EndTime: 62},
			{Speaker: "Person 1", Text: "Go on.", StartTime: 3599.6, EndTime: 3601.0055},
		},
	}
}

func TestExportMarkdownLayout(t *testing.T) {
	var b bytes.Buffer
	if err := ExportMarkdown(exportSample("Teams"), &b); err != nil {
		t.Fatal(err)
	}
	want := "# Roadmap review\n\n" +
		"**Date:** 2026-02-03 14:05:09  \n" +
		"**Duration:** 1h02m05s\n" +
		"**Platform:** Teams\n" +
		"\n" +
		"**[00:00:00] Person 1:** Shall we start?\n\n" +
		"**[00:00:03] You:** Yes. First item.\n\n" +
		"**[01:00:00] Person 1:** Go on.\n\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

func TestExportPlainTextLayout(t *testing.T) {
	var b bytes.Buffer
	if err := ExportPlainText(exportSample(""), &b); err != nil {
		t.Fatal(err)
	}
	want := "Roadmap review\n" +
		"Date: 2026-02-03 14:05:09\n" +
		"Duration: 1h02m05s\n" +
		"\n" +
		"[00:00:00] Person 1: Shall we start?\n" +
		"[00:00:03] You: Yes. First item.\n" +
		"[01:00:00] Person 1: Go on.\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

// SRT keeps every stored segment as its own cue, empty ones included.
func TestExportSRTLayout(t *testing.T) {
	var b bytes.Buffer
	if err := ExportSRT(exportSample(""), &b); err != nil {
		t.Fatal(err)
	}
	want := "1\n00:00:00,400 --> 00:00:01,800\nPerson 1: Shall we start?\n\n" +
		"2\n00:00:02,500 --> 00:00:03,100\nYou: Yes.\n\n" +
		"3\n00:00:03,500 --> 00:00:04,250\nYou: First item.\n\n" +
		"4\n00:01:01,000 --> 00:01:02,000\nPerson 1: \n\n" +
		"5\n00:59:59,600 --> 01:00:01,005\nPerson 1: Go on.\n\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

type failAfter struct{ n int }

func (w *failAfter) Write(p []byte) (int, error) {
	if w.n == 0 {
		return 0, errors.New("disk full")
	}
	w.n--
	return len(p), nil
}

func TestExportsReportWriteErrors(t *testing.T) {
	for name, export := range map[string]func(*Session, *failAfter) error{
		"markdown": func(s *Session, w *failAfter) error { return ExportMarkdown(s, w) },
		"plain":    func(s *Session, w *failAfter) error { return ExportPlainText(s, w) },
		"srt":      func(s *Session, w *failAfter) error { return ExportSRT(s, w) },
	} {
		for _, n := range []int{0, 2} {
			if err := export(exportSample(""), &failAfter{n: n}); err == nil {
				t.Errorf("%s: no error with writer failing after %d writes", name, n)
			}
		}
	}
}

func TestTimeFormats(t *testing.T) {
	for _, c := range []struct {
		sec          float64
		ts, srt, dur string
	}{
		{0, "00:00:00", "00:00:00,000", "0s"},
		{0.4999, "00:00:00", "00:00:00,499", "0s"},
		{0.5, "00:00:01", "00:00:00,500", "1s"},
		{59.6, "00:01:00", "00:00:59,600", "1m00s"},
		{125, "00:02:05", "00:02:05,000", "2m05s"},
		{3661.123, "01:01:01", "01:01:01,123", "1h01m01s"},
		{36000, "10:00:00", "10:00:00,000", "10h00m00s"},
	} {
		if got := formatTimestamp(c.sec); got != c.ts {
			t.Errorf("formatTimestamp(%v) = %q, want %q", c.sec, got, c.ts)
		}
		if got := formatSRTTimestamp(c.sec); got != c.srt {
			t.Errorf("formatSRTTimestamp(%v) = %q, want %q", c.sec, got, c.srt)
		}
		if got := formatDuration(c.sec); got != c.dur {
			t.Errorf("formatDuration(%v) = %q, want %q", c.sec, got, c.dur)
		}
	}
}

func TestParagraphs(t *testing.T) {
	segs := []Segment{
		{Speaker: "Person 1 (Ana)", Text: "So the plan", StartTime: 0, EndTime: 2},
		{Speaker: "Person 1 (Ana)", Text: "is to ship it.", StartTime: 2.6, EndTime: 4},
		{Speaker: "You", Text: "Okay.", StartTime: 4.2, EndTime: 4.6},
		{Speaker: "Person 1 (Ana)", Text: "On Friday.", StartTime: 5, EndTime: 6},
		{Speaker: "Person 1 (Ana)", Text: "  ", StartTime: 6.5, EndTime: 7},
	}
	ps := Paragraphs(segs)
	if len(ps) != 3 || ps[0].Text != "So the plan is to ship it." || ps[0].EndTime != 4 || ps[2].Text != "On Friday." {
		t.Errorf("Paragraphs = %+v", ps)
	}
}

func TestUpsertSegmentRemoved(t *testing.T) {
	s := &Session{}
	s.UpsertSegment(Segment{ID: "a", Text: "yeah", Status: "live"})
	s.UpsertSegment(Segment{ID: "b", Text: "hello"})
	s.UpsertSegment(Segment{ID: "a", Status: StatusRemoved})
	s.UpsertSegment(Segment{ID: "c", Status: StatusRemoved})
	if len(s.Segments) != 1 || s.Segments[0].ID != "b" {
		t.Errorf("segments = %+v", s.Segments)
	}
}
