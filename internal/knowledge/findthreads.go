package knowledge

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// ThreadFinder finds the threads a question is about. A thread matches by
// its centroid, by its best-matching statements (a thread about many
// things still holds the few that answer), and by words of its name: a
// name ("Juniper", "pipeline") is often the whole question.
type ThreadFinder struct {
	rows   []store.ThreadIndexRow
	ids    []int64     // statements with vectors
	vecs   [][]float32 // their vectors, normalised
	member map[int64][]int64
	latest map[int64]time.Time // thread → its latest statement

	// Newer adds one thread a little further from the best (newerMargin)
	// whose latest item is later than every thread found: where a subject's
	// latest turn was threaded on its own (a role question whose latest
	// word is in a comp thread).
	Newer bool
	// NewerFirst puts that thread first rather than last.
	NewerFirst bool

	nameOnce sync.Once
	nameDF   map[string]int // a word → how many thread names have it
}

const newerMargin = 0.12

// ThreadMatch is a thread found for a question.
type ThreadMatch struct {
	ID    int64
	Name  string
	Score float64
	// Newer: added as later than every thread found (Newer).
	Newer bool
}

// NewThreadFinder loads the index for the embedding model.
func NewThreadFinder(st *store.Store, model string) (*ThreadFinder, error) {
	rows, err := st.ThreadIndex()
	if err != nil {
		return nil, err
	}
	vecs, err := st.StatementVecs(model)
	if err != nil {
		return nil, err
	}
	member, err := st.ThreadMembership()
	if err != nil {
		return nil, err
	}
	said, _, err := st.StatementTimes()
	if err != nil {
		return nil, err
	}
	f := &ThreadFinder{rows: rows, member: member, latest: map[int64]time.Time{}}
	for id, ts := range member {
		for _, t := range ts {
			if said[id].After(f.latest[t]) {
				f.latest[t] = said[id]
			}
		}
	}
	for i := range f.rows {
		f.rows[i].Centroid = unit32(f.rows[i].Centroid)
	}
	for id, v := range vecs {
		if len(member[id]) > 0 {
			f.ids = append(f.ids, id)
			f.vecs = append(f.vecs, unit32(v))
		}
	}
	return f, nil
}

func unit32(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func dot32(a, b []float32) float64 {
	var s float32
	for i := range a {
		if i < len(b) {
			s += a[i] * b[i]
		}
	}
	return float64(s)
}

var askStop = map[string]bool{"what": true, "where": true, "when": true, "which": true, "about": true, "with": true, "from": true, "that": true,
	"this": true, "have": true, "were": true, "does": true, "status": true, "update": true, "latest": true, "current": true, "should": true,
	"would": true, "there": true, "their": true, "they": true, "since": true, "into": true, "going": true, "know": true, "tell": true}

// QuestionWords are a question's content words, for matching names.
func QuestionWords(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }) {
		if len(w) >= 4 && !askStop[w] {
			out = append(out, w)
		}
	}
	return out
}

// Find returns up to k threads whose name sc may see, best first: those
// scoring within margin of the best.
func (f *ThreadFinder) Find(sc sensitivity.Scope, q []float32, question string, k int) []ThreadMatch {
	if f == nil || len(f.rows) == 0 {
		return nil
	}
	q = unit32(q)
	// The three best statement matches per thread.
	best := map[int64][3]float64{}
	for i, v := range f.vecs {
		s := dot32(q, v)
		for _, t := range f.member[f.ids[i]] {
			b := best[t]
			switch {
			case s > b[0]:
				b = [3]float64{s, b[0], b[1]}
			case s > b[1]:
				b = [3]float64{b[0], s, b[1]}
			case s > b[2]:
				b[2] = s
			}
			best[t] = b
		}
	}
	words := QuestionWords(question)
	var out []ThreadMatch
	for _, r := range f.rows {
		if r.LabelLevel > sc.MaxLevel {
			continue
		}
		b := best[r.ID]
		s := (b[0] + b[1] + b[2]) / 3
		if len(r.Centroid) > 0 {
			s = math.Max(s, dot32(q, r.Centroid))
		}
		name := strings.ToLower(r.Name + " " + r.Scope)
		bonus := 0.0
		for _, w := range words {
			if strings.Contains(name, w) {
				bonus += 0.06
			}
		}
		out = append(out, ThreadMatch{ID: r.ID, Name: r.Name, Score: s + math.Min(bonus, 0.12)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	const margin = 0.08
	n := 0
	for n < len(out) && n < k && out[n].Score >= out[0].Score-margin {
		n++
	}
	found := out[:n:n]
	if f.Newer && n > 0 {
		var last time.Time
		for _, m := range found {
			if f.latest[m.ID].After(last) {
				last = f.latest[m.ID]
			}
		}
		for _, m := range out[n:] {
			if m.Score < out[0].Score-newerMargin {
				break
			}
			if f.latest[m.ID].After(last) {
				m.Newer = true
				if f.NewerFirst {
					found = append([]ThreadMatch{m}, found...)
				} else {
					found = append(found, m)
				}
				break
			}
		}
	}
	return found
}

// About finds the threads a meeting's query (MeetingQuery) is about: at
// most PrepAboutMax, best first, each at least PrepAboutMin similar and
// with a word of the query in its name that few thread names have.
// Similarity alone ranks a long, general description close to many
// threads; a word like "product" is in dozens of names, "interview" in
// a couple.
func (f *ThreadFinder) About(sc sensitivity.Scope, q []float32, query string) []ThreadMatch {
	if f == nil {
		return nil
	}
	f.nameOnce.Do(func() {
		f.nameDF = map[string]int{}
		for _, r := range f.rows {
			seen := map[string]bool{}
			for _, w := range QuestionWords(r.Name) {
				if !seen[w] {
					seen[w] = true
					f.nameDF[w]++
				}
			}
		}
	})
	rare := max(3, len(f.rows)/300)
	var words []string
	for _, w := range QuestionWords(query) {
		if !meetingWords[w] {
			words = append(words, w)
		}
	}
	var out []ThreadMatch
	for _, m := range f.Find(sc, q, query, PrepAboutMax) {
		if m.Newer || m.Score < PrepAboutMin {
			continue
		}
		if sharesRare(words, QuestionWords(m.Name), f.nameDF, rare) {
			out = append(out, m)
		}
	}
	return out
}

// sharesRare: a name word is one of words, or the same with a short
// ending ("model", "models"), and at most rare thread names have it.
func sharesRare(words, name []string, df map[string]int, rare int) bool {
	for _, n := range name {
		if df[n] > rare {
			continue
		}
		for _, w := range words {
			a, b := w, n
			if len(a) > len(b) {
				a, b = b, a
			}
			if strings.HasPrefix(b, a) && len(b)-len(a) <= 2 {
				return true
			}
		}
	}
	return false
}
