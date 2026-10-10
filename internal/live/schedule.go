package live

import (
	"sort"
	"strings"
	"unicode"
)

// Replay timing model: when each version of a saved line would have
// reached Musubi had the meeting been live. Tomoe saves only final lines,
// so the draft and final times are reconstructed from its settings and
// measurements (2026-10, Tomoe feature/live defaults):
//
//   - Drafts: the live model's partial text is refreshed every 0.5 s while
//     someone talks. It is rebuilt here from the saved word timings: the
//     words spoken so far, lower case and unpunctuated, as the streaming
//     model writes them. (Real drafts also mishear; these don't.)
//   - Final: a turn closes after a 2 s pause (turn_max_gap), or as soon as
//     the other side starts talking, and is then decoded by Cohere, which
//     takes about 7.9% of the audio's length on this Mac
//     (docs/transcription-accuracy-research.md in Tomoe).
const (
	draftEvery    = 0.5
	turnGap       = 2.0
	decodeShare   = 0.079
	minDecodeTime = 0.3
)

type timed struct {
	At  float64 // seconds from the meeting start
	Seg Segment
}

// buildSchedule returns every version of every line in arrival order.
func buildSchedule(segs []Segment) []timed {
	var out []timed
	for i, g := range segs {
		// Drafts: one per refresh tick while the words come in.
		if len(g.Words) > 1 {
			lastN := 0
			for t := g.Words[0].S + draftEvery; t < g.End; t += draftEvery {
				n := 0
				for _, w := range g.Words {
					if w.E <= t {
						n++
					}
				}
				if n == 0 || n == lastN {
					continue
				}
				lastN = n
				d := g
				d.Status, d.Words = "live", nil
				d.Text = draftText(g.Words[:n])
				out = append(out, timed{At: t, Seg: d})
			}
		}
		// Final: the turn closes at the gap or when the other side speaks.
		closeAt := g.End + turnGap
		for _, n := range segs[i+1:] {
			if n.Start >= closeAt {
				break
			}
			if n.Source != g.Source && n.Start >= g.End {
				closeAt = n.Start
				break
			}
		}
		decode := (g.End - g.Start) * decodeShare
		if decode < minDecodeTime {
			decode = minDecodeTime
		}
		f := g
		f.Words = nil
		out = append(out, timed{At: closeAt + decode, Seg: f})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func draftText(ws []Word) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		t := strings.ToLower(strings.TrimFunc(w.T, func(r rune) bool { return unicode.IsPunct(r) }))
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}
