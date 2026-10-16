package knowledge

import (
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Imported is a knowledge item extracted outside Musubi (for experiments
// with other models and prompts): its kind, how settled it is, its text,
// the people it belongs to, and the meeting lines it rests on, numbered
// from 1 across the meeting as NewMeeting numbers them.
type Imported struct {
	Kind   string   `json:"kind"`
	Status string   `json:"status"`
	Text   string   `json:"text"`
	Who    []string `json:"who"`
	Lines  []int    `json:"lines"`
}

// ImportKinds are the kinds an imported item may carry: the statement and
// point kinds plus the insight kinds (docs/insights-direction.md).
var ImportKinds = map[string]bool{
	"decision": true, "commitment": true, "fact": true, "metric": true, "position": true,
	"disagreement": true, "framing": true, "rule": true, "outside_fact": true, "question": true,
	"issue": true, "sensitivity_cue": true, "personal": true,
}

// ImportStatements turns imported items into statements the way points
// become statements: checked against the meeting (names and figures must
// appear in it), labelled with the most sensitive passage they rest on,
// personal details owner-only. Insight kinds are kept as they are; only a
// proposed decision or commitment and an answered question are re-kinded,
// as for points. Items with an unknown kind or no line in the meeting are
// skipped and counted.
//
// keepProposed keeps a proposed decision or commitment as its kind, with
// status "proposed" saying how settled it is, rather than re-kinding it
// "opinion": a programme lead's proposed milestones are commitments in all
// but name, and as opinions they sank to the bottom of every list. Typed
// values (dates, figures) are read from the text.
func (m *Meeting) ImportStatements(items []Imported, itemStart time.Time, holderID func(string) string, labels map[int]sensitivity.Label, keepProposed bool) ([]store.Statement, int) {
	var pts []Point
	skipped := 0
	for _, it := range items {
		text := strings.TrimSpace(it.Text)
		if text == "" || !ImportKinds[it.Kind] {
			skipped++
			continue
		}
		var ev []PointEvidence
		for _, l := range it.Lines {
			if l >= 1 && l <= len(m.lines) {
				ev = append(ev, PointEvidence{Line: l})
			}
		}
		if len(ev) == 0 {
			skipped++
			continue
		}
		status := it.Status
		if !pointStatuses[status] {
			status = "stated"
		}
		pts = append(pts, Point{Kind: it.Kind, Status: status, Text: text, Who: it.Who, Personal: it.Kind == "personal", Evidence: ev})
	}
	pts = m.Check(pts)
	out := make([]store.Statement, 0, len(pts))
	for _, p := range pts {
		st := m.PointStatement(p, itemStart, holderID, labels)
		switch {
		case keepProposed && (p.Kind == "decision" || p.Kind == "commitment") && p.Status == "proposed":
			st.Kind = p.Kind
		case p.Kind == "decision", p.Kind == "commitment", p.Kind == "question", p.Kind == "issue":
		default:
			st.Kind = p.Kind
		}
		if keepProposed {
			st.Value = ValueFromText(st.Kind, st.Text, st.SaidAt)
		}
		out = append(out, st)
	}
	return out, skipped
}

// MeetingLines is how many lines NewMeeting numbered; an import must cite
// lines within it.
func (m *Meeting) MeetingLines() int { return len(m.lines) }
