package session

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// textExport holds the few strings that tell the Markdown and plain-text
// exports apart; the layout is otherwise the same.
type textExport struct {
	title, date, duration, platform string // header line formats
	line                            string // paragraph: time, speaker, text
}

var (
	markdownExport = textExport{
		title:    "# %s\n\n",
		date:     "**Date:** %s  \n",
		duration: "**Duration:** %s\n",
		platform: "**Platform:** %s\n",
		line:     "**[%s] %s:** %s\n\n",
	}
	plainExport = textExport{
		title:    "%s\n",
		date:     "Date: %s\n",
		duration: "Duration: %s\n",
		platform: "Platform: %s\n",
		line:     "[%s] %s: %s\n",
	}
)

func (x textExport) write(sess *Session, w io.Writer) error {
	header := []struct {
		format, value string
	}{
		{x.title, sess.Title},
		{x.date, sess.CreatedAt.Format("2006-01-02 15:04:05")},
		{x.duration, formatDuration(sess.Duration)},
	}
	if sess.Platform != "" {
		header = append(header, struct{ format, value string }{x.platform, sess.Platform})
	}
	for _, h := range header {
		if _, err := fmt.Fprintf(w, h.format, h.value); err != nil {
			return err
		}
	}
	// The blank line under the header is cosmetic; a writer that fails
	// here will fail on the first paragraph too.
	_, _ = io.WriteString(w, "\n")
	for _, p := range Paragraphs(sess.Segments) {
		if _, err := fmt.Fprintf(w, x.line,
			formatTimestamp(p.StartTime), p.Speaker, p.Text); err != nil {
			return err
		}
	}
	return nil
}

// ExportMarkdown writes the session to w as a Markdown document: a title,
// the date, length and meeting app, then one paragraph per speaker turn.
func ExportMarkdown(sess *Session, w io.Writer) error {
	return markdownExport.write(sess, w)
}

// ExportPlainText writes the same content as ExportMarkdown without the
// Markdown.
func ExportPlainText(sess *Session, w io.Writer) error {
	return plainExport.write(sess, w)
}

// ExportSRT writes the session as SRT subtitles. Unlike the text exports
// it keeps one cue per segment, in stored order, since subtitles follow
// the audio line by line.
func ExportSRT(sess *Session, w io.Writer) error {
	for n, seg := range sess.Segments {
		if _, err := fmt.Fprintf(w, "%d\n%s --> %s\n%s: %s\n\n", n+1,
			formatSRTTimestamp(seg.StartTime), formatSRTTimestamp(seg.EndTime),
			seg.Speaker, seg.Text); err != nil {
			return err
		}
	}
	return nil
}

// hms splits a whole number of seconds into hours, minutes and seconds.
func hms(secs int) (h, m, s int) {
	return secs / 3600, secs / 60 % 60, secs % 60
}

// formatTimestamp renders a time to the nearest second, as HH:MM:SS.
func formatTimestamp(seconds float64) string {
	h, m, s := hms(int(math.Round(seconds)))
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// formatSRTTimestamp renders a time as SRT wants it, HH:MM:SS,mmm. The
// milliseconds are cut, not rounded.
func formatSRTTimestamp(seconds float64) string {
	millis := int(seconds * 1000)
	h, m, s := hms(millis / 1000)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, millis%1000)
}

// formatDuration renders a length compactly, leaving out leading zero
// units: "45s", "2m05s", "1h02m05s".
func formatDuration(seconds float64) string {
	h, m, s := hms(int(math.Round(seconds)))
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// A Paragraph is consecutive lines by one speaker, shown and exported as
// one block: the speech detector ends a line at every half-second pause,
// so one turn is often several lines.
type Paragraph struct {
	Speaker            string
	StartTime, EndTime float64
	Text               string
}

// Paragraphs joins consecutive segments (in time order) with the same
// speaker into paragraphs.
func Paragraphs(segs []Segment) []Paragraph {
	sorted := append([]Segment(nil), segs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartTime < sorted[j].StartTime })
	var out []Paragraph
	for _, s := range sorted {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Speaker == s.Speaker {
			out[n-1].Text += " " + text
			out[n-1].EndTime = max(out[n-1].EndTime, s.EndTime)
			continue
		}
		out = append(out, Paragraph{Speaker: s.Speaker, StartTime: s.StartTime, EndTime: s.EndTime, Text: text})
	}
	return out
}
