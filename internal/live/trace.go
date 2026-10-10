package live

import (
	"strings"
	"time"
)

// Trace is one step of the live panel's processing, for the diagnostics
// view: what arrived, what was read, what was asked, what came back, and
// how long each took. Clock is the meeting time (seconds from its start),
// so steps can be lined up against when things were said.
type Trace struct {
	Seq    int       `json:"seq"`
	Cycle  int       `json:"cycle"` // suggestion cycle; 0 for feed and manual lookups
	At     time.Time `json:"at"`
	Clock  float64   `json:"clock"`
	Kind   string    `json:"kind"` // feed, cycle, entities, phrases, llm, rank, search, summary, brief, error
	Title  string    `json:"title"`
	Ms     int64     `json:"ms,omitempty"`
	Detail any       `json:"detail,omitempty"`
}

const traceKeep = 800

// feedStats counts what arrived between suggestion cycles.
type feedStats struct {
	Drafts   int       `json:"drafts"`
	Pending  int       `json:"pending"`
	Finals   int       `json:"finals"`
	Removed  int       `json:"removed"`
	FinalLag []float64 `json:"-"`
}

// FeedSummary is feed activity since the previous cycle.
type FeedSummary struct {
	Drafts  int `json:"drafts"`
	Pending int `json:"pending"`
	Finals  int `json:"finals"`
	Removed int `json:"removed"`
	// FinalLagAvg/Max: seconds from the end of speech to its final text
	// arriving.
	FinalLagAvg float64 `json:"finalLagAvg"`
	FinalLagMax float64 `json:"finalLagMax"`
}

func (f *feedStats) summary() FeedSummary {
	s := FeedSummary{Drafts: f.Drafts, Pending: f.Pending, Finals: f.Finals, Removed: f.Removed}
	for _, x := range f.FinalLag {
		s.FinalLagAvg += x
		if x > s.FinalLagMax {
			s.FinalLagMax = x
		}
	}
	if n := len(f.FinalLag); n > 0 {
		s.FinalLagAvg /= float64(n)
	}
	return s
}

// SearchHitTrace is a search result as the diagnostics view shows it.
type SearchHitTrace struct {
	Rank    int     `json:"rank"`
	Item    string  `json:"item"`
	Seq     int     `json:"seq"`
	Title   string  `json:"title"`
	Date    string  `json:"date"`
	Score   float64 `json:"score"`
	Level   string  `json:"level"`
	Cats    string  `json:"cats"`
	Excerpt string  `json:"excerpt"`
}

// evidenceAt is when the conversation first said something the query is
// about: the start of the earliest line in the window sharing a content word
// with it. -1 if none (the model inferred the topic).
func evidenceAt(query string, lines []Segment) float64 {
	keys := map[string]bool{}
	for _, w := range words(query) {
		if len(w) >= 3 && !stop[w] {
			keys[w] = true
		}
	}
	for _, s := range lines {
		// A speaker's name counts: someone talking is in the conversation.
		for _, w := range words(speakerName(s.Speaker) + " " + s.Text) {
			if keys[w] {
				return s.Start
			}
		}
	}
	return -1
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
