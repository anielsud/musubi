package live

import (
	"fmt"
	"sort"
	"strings"
)

// Meeting is the conversation so far: the latest version of every line.
type Meeting struct {
	Session Session
	order   []string
	segs    map[string]Segment
	// version increases with every change, so the suggester can tell
	// whether anything new was said; finals counts final lines applied.
	version int
	finals  int
}

func (m *Meeting) Finals() int { return m.finals }

func NewMeeting(s Session) *Meeting {
	return &Meeting{Session: s, segs: map[string]Segment{}}
}

// Apply records a new or revised line.
func (m *Meeting) Apply(s Segment) {
	if s.Status == "removed" {
		if _, ok := m.segs[s.ID]; ok {
			delete(m.segs, s.ID)
			m.version++
		}
		return
	}
	if _, ok := m.segs[s.ID]; !ok {
		m.order = append(m.order, s.ID)
	}
	m.segs[s.ID] = s
	m.version++
	if s.Status == "" {
		m.finals++
	}
}

func (m *Meeting) Version() int { return m.version }

// Lines returns the lines in time order.
func (m *Meeting) Lines() []Segment {
	out := make([]Segment, 0, len(m.segs))
	for _, id := range m.order {
		if s, ok := m.segs[id]; ok {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// RecentLines returns the lines ending in the last window seconds.
func (m *Meeting) RecentLines(window float64) []Segment {
	lines := m.Lines()
	if len(lines) == 0 {
		return nil
	}
	end := lines[len(lines)-1].End
	var out []Segment
	for _, s := range lines {
		if s.End >= end-window && strings.TrimSpace(s.Text) != "" {
			out = append(out, s)
		}
	}
	return out
}

// Recent returns the last window seconds as "Speaker: text" lines.
func (m *Meeting) Recent(window float64) string {
	var b strings.Builder
	for _, s := range m.RecentLines(window) {
		fmt.Fprintf(&b, "%s: %s\n", speakerName(s.Speaker), s.Text)
	}
	return b.String()
}

// speakerName shows Tomoe's "Person 3 (Name)" as the name.
func speakerName(s string) string {
	if i := strings.Index(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		return s[i+2 : len(s)-1]
	}
	return s
}

// Speakers are the people heard so far, other than "You", in order of
// first speaking.
func (m *Meeting) Speakers() []string {
	seen := map[string]bool{"You": true}
	var out []string
	for _, s := range m.Lines() {
		if n := speakerName(s.Speaker); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
