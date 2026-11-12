package procstat

// Ring keeps the last n values pushed, overwriting the oldest.
type Ring[T any] struct {
	buf  []T
	next int
	full bool
}

func NewRing[T any](n int) *Ring[T] {
	if n < 1 {
		n = 1
	}
	return &Ring[T]{buf: make([]T, n)}
}

func (r *Ring[T]) Push(v T) {
	r.buf[r.next] = v
	r.next++
	if r.next == len(r.buf) {
		r.next, r.full = 0, true
	}
}

func (r *Ring[T]) Len() int {
	if r.full {
		return len(r.buf)
	}
	return r.next
}

// Slice is a copy, oldest first.
func (r *Ring[T]) Slice() []T {
	if !r.full {
		return append([]T(nil), r.buf[:r.next]...)
	}
	out := make([]T, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}
