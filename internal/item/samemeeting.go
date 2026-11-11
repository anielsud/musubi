package item

import (
	"strings"
	"time"
	"unicode"
)

// SameMeetingWords is the WordAgreement at or above which a transcript
// overlapping a recording is the same conversation.
const SameMeetingWords = 0.6

// WordAgreement is the share of the Teams copy's distinct longer words
// (four letters or more), said while Tomoe was recording, that the Tomoe
// recording also has. On all-t4 the same meeting agreed 0.89–0.98 and a
// different conversation in the same slot 0.13–0.41 (2026-11-09).
func WordAgreement(m, t *Item) float64 {
	words := func(it *Item, window bool) map[string]bool {
		w := map[string]bool{}
		for _, sg := range it.Segments {
			if at := it.OccurredAt.Add(time.Duration(sg.Start * float64(time.Second))); window && (at.Before(m.OccurredAt) || at.After(m.End())) {
				continue
			}
			for _, f := range strings.FieldsFunc(strings.ToLower(sg.Text), func(r rune) bool { return !unicode.IsLetter(r) }) {
				if len(f) >= 4 {
					w[f] = true
				}
			}
		}
		return w
	}
	tw, mw := words(t, true), words(m, false)
	if len(tw) == 0 {
		return 0
	}
	n := 0
	for w := range tw {
		if mw[w] {
			n++
		}
	}
	return float64(n) / float64(len(tw))
}

// Overlapping is the Teams copy that shares the most time with m (at least
// a quarter of the recording), or nil.
func Overlapping(m *Item, ts []*Item) *Item {
	var best *Item
	var bestD time.Duration
	for _, t := range ts {
		lo, hi := m.OccurredAt, m.End()
		if t.OccurredAt.After(lo) {
			lo = t.OccurredAt
		}
		if t.End().Before(hi) {
			hi = t.End()
		}
		if d := hi.Sub(lo); d > bestD && d >= m.End().Sub(m.OccurredAt)/4 {
			best, bestD = t, d
		}
	}
	return best
}
