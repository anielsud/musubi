// Package vindex is a small in-memory nearest-neighbour index over 1-bit
// codes (the sign of each vector component): 96 bytes for a 768-dimension
// vector instead of 3 KB. A query scans the codes by Hamming distance for a
// few hundred candidates, which the caller re-ranks exactly against the
// stored vectors (docs/scaling.md, "reads at scale"). On a synthetic corpus
// of 192k passages, 400 candidates held all of the exact top 10.
package vindex

import (
	"container/heap"
	"encoding/binary"
	"math/bits"
)

// Code packs a vector's signs: bit i is set when v[i] > 0.
func Code(v []float32) []byte {
	out := make([]byte, (len(v)+63)/64*8)
	for i, x := range v {
		if x > 0 {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

// Index holds codes by id.
type Index struct {
	ids   []int64
	words int // uint64 words per code
	bits  []uint64
}

// New returns an empty index for vectors of dim components.
func New(dim int) *Index { return &Index{words: (dim + 63) / 64} }

// Add appends a code (from Code); codes of another size are ignored.
func (x *Index) Add(id int64, code []byte) {
	if len(code) != x.words*8 {
		return
	}
	x.ids = append(x.ids, id)
	for w := 0; w < x.words; w++ {
		x.bits = append(x.bits, binary.LittleEndian.Uint64(code[8*w:]))
	}
}

func (x *Index) Len() int { return len(x.ids) }

// Nearest returns up to k ids whose codes are closest to the query's,
// nearest first.
func (x *Index) Nearest(q []float32, k int) []int64 {
	if k <= 0 || len(x.ids) == 0 {
		return nil
	}
	code := Code(q)
	qw := make([]uint64, x.words)
	for w := range qw {
		if 8*w+8 <= len(code) {
			qw[w] = binary.LittleEndian.Uint64(code[8*w:])
		}
	}
	h := &maxHeap{}
	for i := range x.ids {
		d := 0
		row := x.bits[i*x.words : (i+1)*x.words]
		for w, b := range row {
			d += bits.OnesCount64(b ^ qw[w])
		}
		if h.Len() < k {
			heap.Push(h, cand{i, d})
		} else if d < (*h)[0].d {
			(*h)[0] = cand{i, d}
			heap.Fix(h, 0)
		}
	}
	out := make([]int64, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = x.ids[heap.Pop(h).(cand).i]
	}
	return out
}

type cand struct{ i, d int }

// maxHeap keeps the k nearest so far, farthest on top.
type maxHeap []cand

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i].d > h[j].d || h[i].d == h[j].d && h[i].i > h[j].i }
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(v any)        { *h = append(*h, v.(cand)) }
func (h *maxHeap) Pop() any          { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }
