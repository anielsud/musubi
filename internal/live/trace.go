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

// evidenceAt is when the conversation last said something the query is
// about: the end of the latest word sharing a content word with it (a
// speaker's name included), or the start of the line without word timings.
// Latency is measured from there: a topic discussed for a minute counts
// from its latest mention, not its first. -1 if never said as such (the
// model inferred it).
func evidenceAt(query string, lines []Segment) float64 {
	keys := contentWords(query)
	at := -1.0
	for _, s := range lines {
		for _, w := range words(speakerName(s.Speaker)) {
			if keys[w] && s.Start > at {
				at = s.Start
			}
		}
		if len(s.Words) == 0 {
			for _, w := range words(s.Text) {
				if keys[w] && s.Start > at {
					at = s.Start
				}
			}
			continue
		}
		for _, wt := range s.Words {
			for _, w := range words(wt.T) {
				if keys[w] && wt.E > at {
					at = wt.E
				}
			}
		}
	}
	return at
}

func contentWords(q string) map[string]bool {
	keys := map[string]bool{}
	for _, w := range words(q) {
		if len(w) >= 3 && !stop[w] {
			keys[w] = true
		}
	}
	return keys
}

// overlap is the share of the smaller query's content words found in the
// other: "45 days patching SLAs" and "patching policy 45 days" overlap.
func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, big := a, b
	if len(b) < len(a) {
		small, big = b, a
	}
	n := 0
	for w := range small {
		if big[w] {
			n++
		}
	}
	return float64(n) / float64(len(small))
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
