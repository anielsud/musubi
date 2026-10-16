// Command scale-bench measures Musubi's read paths as the corpus grows
// (docs/scaling.md §3, "reads at scale"). It generates a synthetic store
// shaped like real leadership meetings (per meeting: ~120 passages, ~250
// statements, a handful of subjects drawn from a pool that grows more
// slowly than the meetings), with random vectors, then times what the app
// does: search, the topic list and map, the card index, a card, thread
// links. Only speed and memory are measured; the text is not meaningful.
//
//	go run -tags sqlite_fts5 ./tools/scale-bench -meetings 400 -db /tmp/scale-400.db
//
// An existing database is reused (generation dominates the run time).
package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

const model = "nomic-embed-text"

var (
	meetings = flag.Int("meetings", 40, "meetings to generate")
	dbPath   = flag.String("db", "", "database file (reused if it exists)")
	dim      = flag.Int("dim", 768, "vector size")
	chunksPM = flag.Int("chunks", 120, "passages per meeting")
	stmtsPM  = flag.Int("statements", 250, "statements per meeting")
	only     = flag.String("only", "", "comma-separated measures to run (default all)")
)

func main() {
	flag.Parse()
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "-db is required")
		os.Exit(2)
	}
	_, statErr := os.Stat(*dbPath)
	t0 := time.Now()
	st, err := store.Open(*dbPath)
	check(err)
	defer st.Close()
	if statErr != nil {
		generate(st)
		fmt.Printf("generated %d meetings in %s\n", *meetings, time.Since(t0).Round(time.Second))
	}
	measure(st)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type subject struct {
	name   string
	words  []string
	center []float32
	thread int64
}

func unit(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}

func randVec(r *rand.Rand) []float32 {
	v := make([]float32, *dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return unit(v)
}

func near(r *rand.Rand, c []float32, noise float64) []float32 {
	v := make([]float32, len(c))
	for i := range v {
		v[i] = c[i] + float32(r.NormFloat64()*noise/math.Sqrt(float64(len(c))))
	}
	return unit(v)
}

// letters spells i in base 26 (a, b, …, ba, bb, …).
func letters(i int) string {
	s := ""
	for {
		s = string(rune('a'+i%26)) + s
		i /= 26
		if i == 0 {
			return s
		}
	}
}

// vocab is a Zipf-like word list: a few very common words, a long tail.
var vocab []string

func word(r *rand.Rand) string {
	i := int(math.Floor(math.Pow(r.Float64(), 3) * float64(len(vocab))))
	return vocab[i]
}

func sentence(r *rand.Rand, s *subject, n int) string {
	ws := make([]string, n)
	for i := range ws {
		if r.Intn(5) == 0 {
			ws[i] = s.words[r.Intn(len(s.words))]
		} else {
			ws[i] = word(r)
		}
	}
	return strings.Join(ws, " ")
}

func generate(st *store.Store) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		vocab = append(vocab, fmt.Sprintf("w%x", i*2654435761%1000003))
	}
	// The subject pool grows more slowly than the meetings, as real
	// subjects recur: about 14 new per meeting at first.
	pool := 20 + int(10*math.Pow(float64(*meetings), 0.7))
	subjects := make([]*subject, pool)
	for i := range subjects {
		// Letters only: the card index reads words as runs of letters.
		s := &subject{name: "Ka" + letters(i), center: randVec(r)}
		s.words = []string{s.name, "Vo" + letters(i) + "ra", "zu" + letters(i)}
		subjects[i] = s
	}
	start := time.Date(2024, 1, 1, 15, 0, 0, 0, time.UTC)
	for m := 0; m < *meetings; m++ {
		id := fmt.Sprintf("m:%05d", m)
		at := start.Add(time.Duration(m) * 24 * time.Hour)
		// Subjects of this meeting: mostly ones already in play, some new.
		opened := min(pool, 20+m*pool/max(*meetings, 1))
		var ms []*subject
		for len(ms) < 8+r.Intn(8) {
			ms = append(ms, subjects[r.Intn(opened)])
		}
		it := item.Item{ID: id, Source: "scale", Ref: id, Kind: item.KindMeeting, Title: "Weekly review", OccurredAt: at,
			Segments: []item.Segment{{Text: "x"}}}
		_, err := st.UpsertItem(it)
		check(err)
		chunks := make([]store.Chunk, *chunksPM)
		subjOf := make([]*subject, *chunksPM)
		for c := range chunks {
			s := ms[c*len(ms) / *chunksPM]
			subjOf[c] = s
			chunks[c] = store.Chunk{Seq: c, SegFrom: 0, SegTo: 1, Start: float64(30 * c), Text: sentence(r, s, 60)}
		}
		check(st.ReplaceChunks(id, "v", "h", chunks))
		check(st.ReplaceLabels(id, "v", "h", sensitivity.Label{Level: sensitivity.Internal}, nil, []string{"owner@corp.example"}))
		cs, err := st.Chunks(id)
		check(err)
		vecs := map[int64][]float32{}
		for c, ch := range cs {
			vecs[ch.ID] = near(r, subjOf[c].center, 0.8)
		}
		check(st.PutVecs(id, "embed", model, "v", "h", vecs))
		sts := make([]store.Statement, *stmtsPM)
		stSubj := map[string]*subject{}
		for k := range sts {
			c := k * len(cs) / *stmtsPM
			text := fmt.Sprintf("%s: %s", subjOf[c].name, sentence(r, subjOf[c], 18))
			stSubj[text] = subjOf[c]
			sts[k] = store.Statement{ChunkID: cs[c].ID, Line: 1, Kind: []string{"fact", "metric", "commitment", "decision", "question"}[r.Intn(5)],
				Status: "stated", Text: text, Holder: fmt.Sprintf("Person %d", r.Intn(12)), SaidAt: at.Add(time.Duration(30*c) * time.Second),
				Certainty: 0.9, Grounded: true}
		}
		check(st.ReplaceStatements(id, "x8", "h", sts))
		un, err := st.UnlinkedStatements()
		check(err)
		byThread := map[*subject][]int64{}
		for _, u := range un {
			s := stSubj[u.Text]
			v := near(r, s.center, 0.6)
			check(st.PutStatementVec(u.ID, model, v))
			_, err := st.NewClaim(u, v)
			check(err)
			byThread[s] = append(byThread[s], u.ID)
		}
		for s, ids := range byThread {
			if s.thread == 0 {
				s.thread, err = st.NewThread(s.name+" programme", "the "+s.name+" work", sensitivity.Internal)
				check(err)
			}
			check(st.AddToThread(s.thread, ids, sensitivity.Internal))
		}
		check(st.MarkThreaded(id, "scale"))
		if (m+1)%max(1, *meetings/10) == 0 {
			fmt.Fprintf(os.Stderr, "progress generate %d/%d\n", m+1, *meetings)
		}
	}
	check(st.PlaceClaimsInThreads())
}

type result struct {
	name string
	d    time.Duration
	note string
}

func timeIt(name string, reps int, fn func() string) result {
	var ds []time.Duration
	note := ""
	for i := 0; i < reps; i++ {
		t := time.Now()
		note = fn()
		ds = append(ds, time.Since(t))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return result{name, ds[len(ds)/2], note}
}

func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / 1e6
}

func measure(st *store.Store) {
	want := func(n string) bool { return *only == "" || strings.Contains(","+*only+",", ","+n+",") }
	s, err := st.Stats()
	check(err)
	threads, _ := st.Threads()
	all, _ := st.AllClaimIDs()
	fmt.Printf("corpus: %d meetings, %d passages, %d claims, %d threads\n", s.ItemsByKind["meeting"], s.Chunks, len(all), len(threads))
	sc := sensitivity.Owner()
	r := rand.New(rand.NewSource(7))
	q := randVec(r)
	var res []result
	if want("open") {
		res = append(res, timeIt("store open", 3, func() string {
			x, err := store.Open(*dbPath)
			check(err)
			x.Close()
			return ""
		}))
	}
	if want("search") {
		for _, m := range []store.SearchMode{store.ModeKeyword, store.ModeVector, store.ModeHybrid} {
			res = append(res, timeIt("search "+string(m), 5, func() string {
				hs, err := st.Search(sc, store.SearchQuery{Text: "Kad Vodra programme", Vec: q, Model: model, K: 10, Mode: m})
				check(err)
				return fmt.Sprintf("%d hits", len(hs))
			}))
		}
	}
	v := knowledge.Views{Store: st, Scope: sc, Now: time.Now()}
	if want("claims") {
		res = append(res, timeIt("claim search", 3, func() string {
			cs, err := v.SearchClaims("Vodra zud", 20)
			check(err)
			return fmt.Sprintf("%d claims", len(cs))
		}))
	}
	if want("topics") {
		res = append(res, timeIt("topic list (Views.Topics)", 1, func() string {
			ts, err := v.Topics()
			check(err)
			return fmt.Sprintf("%d topics", len(ts))
		}))
	}
	if want("links") {
		res = append(res, timeIt("thread links (LinkThreads)", 1, func() string {
			n, err := knowledge.LinkThreads(st, nil)
			check(err)
			return fmt.Sprintf("%d threads", n)
		}))
	}
	if want("map") {
		var first int64
		for _, label := range []string{"topic map (Views.Map)", "topic map, again (cached)"} {
			res = append(res, timeIt(label, 1, func() string {
				m, err := v.Map()
				check(err)
				if len(m.Topics) > 0 {
					first = m.Topics[0].ID
				}
				return fmt.Sprintf("%d topics, %d links, %d clusters", len(m.Topics), len(m.Edges), len(m.Clusters))
			}))
		}
		res = append(res, timeIt("topic page (Views.Topic)", 3, func() string {
			t, err := v.Topic(first)
			check(err)
			if t == nil {
				return "none"
			}
			return fmt.Sprintf("%d claims, %d related", len(t.ClaimList), len(t.Related))
		}))
	}
	if want("cards") {
		before := heapMB()
		var x *knowledge.CardIndex
		res = append(res, timeIt("card index build", 1, func() string {
			var err error
			x, err = knowledge.BuildCardIndex(st)
			check(err)
			return ""
		}))
		res[len(res)-1].note = fmt.Sprintf("%.0f MB held", heapMB()-before)
		var a knowledge.Anchor
		said := "we talked about Subject3 today"
		if ts := x.AllTerms(); len(ts) > 0 {
			said = "we talked about " + ts[0].Term + " today"
		}
		res = append(res, timeIt("card find (term)", 5, func() string {
			var how string
			a, how, err = x.Find(said, nil)
			check(err)
			return how
		}))
		cents, err := st.ClaimCentroids()
		check(err)
		var cq []float32
		for _, c := range cents {
			cq = c
			break
		}
		res = append(res, timeIt("card find (embedding)", 5, func() string {
			_, how, err := x.Find("something unusual", func(string) ([]float32, error) { return cq, nil })
			check(err)
			return how
		}))
		res = append(res, timeIt("card build (Views.Card)", 5, func() string {
			c, err := v.Card(x, a, "", "Owner")
			check(err)
			if c == nil {
				return "no card"
			}
			n := 0
			for _, s := range c.Sections {
				n += len(s.Lines)
			}
			return fmt.Sprintf("%d lines", n)
		}))
	}
	fmt.Printf("\n| Measure | Time | |\n|---|---|---|\n")
	for _, x := range res {
		fmt.Printf("| %s | %s | %s |\n", x.name, x.d.Round(time.Millisecond), x.note)
	}
}
