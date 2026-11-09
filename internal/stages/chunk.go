// Package stages holds the per-item derive stages. Each is versioned; the
// pipeline re-runs a stage for an item whenever its version or input hash
// changes.
package stages

import (
	"strings"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/store"
)

const ChunkVersion = "c2"

// Chunk sizes in words. Passages around 150 words keep one topic in a
// meeting while carrying enough context to embed well; a passage closes at a
// speaker change once it has the minimum, so turns stay whole.
const (
	chunkMin    = 80
	chunkTarget = 160
	chunkMax    = 260
)

// Chunk groups an item's segments into retrieval passages. Each line of a
// passage is "Speaker: text" so search can match on who said it. title is
// the item's display title (for a meeting, its calendar event's subject);
// every chunk carries it so keyword search and embeddings know which
// meeting a passage is from.
func Chunk(it item.Item, title string) []store.Chunk {
	var out []store.Chunk
	var cur *store.Chunk
	var lines []string
	speakers := map[string]bool{}
	var spkOrder []string
	words := 0
	flush := func(to int) {
		if cur == nil {
			return
		}
		cur.SegTo = to
		cur.Title = title
		cur.Text = strings.Join(lines, "\n")
		cur.Speakers = strings.Join(spkOrder, ", ")
		out = append(out, *cur)
		cur, lines, words = nil, nil, 0
		speakers, spkOrder = map[string]bool{}, nil
	}
	for i, s := range it.Segments {
		n := len(strings.Fields(s.Text))
		if n == 0 {
			continue
		}
		if cur != nil {
			prev := it.Segments[i-1].Speaker
			if words+n > chunkMax || words >= chunkTarget || (words >= chunkMin && s.Speaker != prev) {
				flush(i)
			}
		}
		if cur == nil {
			cur = &store.Chunk{ItemID: it.ID, Seq: len(out), SegFrom: i, Start: s.Start}
		}
		line := s.Text
		if s.From != "" {
			// Marked, so what's read from it knows the owner didn't hear it.
			line = "[" + s.From + " transcript, not heard by you] " + s.Text
		}
		if s.Speaker != "" {
			line = s.Speaker + ": " + line
			if !speakers[s.Speaker] {
				speakers[s.Speaker] = true
				spkOrder = append(spkOrder, s.Speaker)
			}
		}
		lines = append(lines, line)
		cur.End = s.End
		words += n
	}
	flush(len(it.Segments))
	return out
}
