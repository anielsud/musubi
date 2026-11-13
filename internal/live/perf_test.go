package live

// TestLivePerf times Live's read paths on a real database, cold (the first
// call on a fresh store) and warm, and plays a synthetic meeting through
// Live's whole card path with stand-ins for the embedder and the
// classifier: no model, local or hosted, is called. It is skipped unless
// -perf.db names a database, which must be a disposable COPY (the store
// migrates it and turns WAL on when it opens it):
//
//	cp <dataset>/musubi.db /tmp/copy.db
//	nice -n 10 go test -tags sqlite_fts5 -run TestLivePerf -v ./internal/live/ \
//	  -args -perf.db /tmp/copy.db -perf.cache_mb 64 -perf.mmap_mb 2048 -perf.read_conns 4 -perf.cache
//
// Results: docs/research-log.md, 2026-11-12.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

var (
	perfDB        = flag.String("perf.db", "", "a disposable copy of a Musubi database (TestLivePerf)")
	perfCacheMB   = flag.Int("perf.cache_mb", 0, "store.Options.CacheMB")
	perfMmapMB    = flag.Int("perf.mmap_mb", 0, "store.Options.MmapMB")
	perfReadConns = flag.Int("perf.read_conns", 0, "store.Options.ReadConns")
	perfCache     = flag.Bool("perf.cache", false, "live.cache: Live's read cache")
	perfSkip      = flag.Bool("perf.skip", false, "live.skip_idle")
	perfMinutes   = flag.Float64("perf.minutes", 10, "length of the synthetic meeting")
	perfWarm      = flag.Int("perf.warm", 5, "warm repetitions per measure")
	perfOnly      = flag.String("perf.only", "", "comma-separated measures (default all)")
	perfGraphs    = flag.Int("perf.graphs", 50, "entities whose graphs are timed, most mentioned first")
)

func TestLivePerf(t *testing.T) {
	if *perfDB == "" {
		t.Skip("needs -perf.db (a copy of a database)")
	}
	opts := store.Options{CacheMB: *perfCacheMB, MmapMB: *perfMmapMB, ReadConns: *perfReadConns}
	fmt.Printf("options %+v, live.cache %v, live.skip_idle %v\n", opts, *perfCache, *perfSkip)
	samples := perfSamples(t, *perfDB)
	st, err := store.OpenWith(*perfDB, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, _ := sensitivity.ParseScope("owner", nil)
	want := func(m string) bool { return *perfOnly == "" || strings.Contains(","+*perfOnly+",", ","+m+",") }
	var rc *store.ReadCache
	if *perfCache {
		rc = st.NewReadCache()
	}
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.TomoeSessions = ""
	cfg.Live.Cache, cfg.Live.SkipIdle = *perfCache, *perfSkip
	emb := &perfEmbedder{model: samples.model, pool: samples.vecs}
	eng := &engine.Engine{Cfg: cfg, Store: st, Embedder: emb}

	if want("topicinfos") {
		timeIt("TopicInfos", func() {
			if rc != nil {
				_, err = rc.TopicInfos()
			} else {
				_, err = st.TopicInfos()
			}
			check(t, err)
		})
	}
	if want("graphs") {
		ents := samples.entities[:min(*perfGraphs, len(samples.entities))]
		timeIt(fmt.Sprintf("EntityGraph ×%d (most mentioned)", len(ents)), func() {
			for _, id := range ents {
				if rc != nil {
					_, err = rc.EntityGraph(owner, id, nil, 4)
				} else {
					_, err = st.EntityGraph(owner, id, nil, 4)
				}
				check(t, err)
			}
		})
	}
	if want("near") {
		timeIt(fmt.Sprintf("StatementsNear ×%d (400 claims)", len(samples.queries)), func() {
			for _, q := range samples.queries {
				if rc != nil {
					_, err = rc.StatementsNear(owner, q, samples.model, stmtClaims, nil)
				} else {
					_, err = st.StatementsNear(owner, q, samples.model, stmtClaims, nil)
				}
				check(t, err)
			}
		})
	}
	var cs *engine.Cards
	if want("cards") || want("meeting") {
		t0 := time.Now()
		cs, err = eng.LoadCards()
		check(t, err)
		fmt.Printf("%-44s %9.1f ms (once)\n", "card index", ms(time.Since(t0)))
	}
	if want("cards") {
		topics := samples.topics
		timeIt(fmt.Sprintf("Card ×%d (largest topics)", len(topics)), func() {
			for _, id := range topics {
				_, err := cs.CardWith(rc, owner, knowledge.Anchor{Topic: id}, time.Now())
				check(t, err)
			}
		})
		timeIt("ThreadStatuses", func() {
			if rc != nil {
				_, err = rc.ThreadStatuses(owner)
			} else {
				_, err = st.ThreadStatuses(owner)
			}
			check(t, err)
		})
	}
	if want("queue") {
		timeIt("AllItems", func() { _, err := st.AllItems(); check(t, err) })
		timeIt("work queue (engine.Queue)", func() { _, err := eng.Queue(); check(t, err) })
		timeIt("WorkStamp", func() { _, err := st.WorkStamp(); check(t, err) })
	}
	if want("person") && samples.person != "" {
		timeIt("person page (most met)", func() { _, err := eng.PersonPage(samples.person); check(t, err) })
	}
	if want("stats") {
		timeIt("Stats (status bar, every 30 s)", func() { _, err := st.Stats(); check(t, err) })
	}
	if want("contention") {
		// A read while the writer is busy: a backup (VACUUM INTO) holds
		// the writer's connection for seconds, as a long write does.
		done := make(chan time.Duration)
		go func() {
			t0 := time.Now()
			check(t, st.BackupTo(filepath.Join(t.TempDir(), "backup.db")))
			done <- time.Since(t0)
		}()
		time.Sleep(200 * time.Millisecond)
		t0 := time.Now()
		_, err := st.ThreadStatuses(owner)
		check(t, err)
		read := time.Since(t0)
		fmt.Printf("%-44s %9.1f ms for ThreadStatuses during a backup that took %.1f ms\n", "read behind a busy writer", ms(read), ms(<-done))
	}
	if want("meeting") {
		perfMeeting(t, eng, cs, samples)
	}
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	fmt.Printf("%-44s %9.0f MB (macOS reports bytes)\n", "peak resident memory", float64(ru.Maxrss)/(1<<20))
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// timeIt prints the first call (cold) and the median of the warm ones.
func timeIt(name string, f func()) {
	t0 := time.Now()
	f()
	cold := time.Since(t0)
	var warm []time.Duration
	for i := 0; i < *perfWarm; i++ {
		t1 := time.Now()
		f()
		warm = append(warm, time.Since(t1))
	}
	sort.Slice(warm, func(i, j int) bool { return warm[i] < warm[j] })
	med := time.Duration(0)
	if len(warm) > 0 {
		med = warm[len(warm)/2]
	}
	fmt.Printf("%-44s cold %9.1f ms   warm %9.1f ms\n", name, ms(cold), ms(med))
}

// perfSampleSet is what the measures read: picked from the database
// itself, so nothing about its content is in the code.
type perfSampleSet struct {
	model    string
	entities []string // the 50 most mentioned
	queries  [][]float32
	vecs     [][]float32 // the embedder stand-in's answers
	topics   []int64     // the 20 largest
	person   string      // the person in the most meetings' mentions
	names    []string    // words the synthetic meeting says
}

func perfSamples(t *testing.T, path string) perfSampleSet {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	check(t, err)
	defer db.Close()
	var s perfSampleSet
	check(t, db.QueryRow(`SELECT model FROM statement_vecs GROUP BY model ORDER BY count(*) DESC LIMIT 1`).Scan(&s.model))
	rows, err := db.Query(`SELECT m.entity_id, e.name, e.type FROM mentions m JOIN entities e ON e.id = m.entity_id
		WHERE m.entity_id IS NOT NULL GROUP BY m.entity_id ORDER BY count(*) DESC LIMIT 50`)
	check(t, err)
	for rows.Next() {
		var id, name, typ string
		rows.Scan(&id, &name, &typ)
		s.entities = append(s.entities, id)
		s.names = append(s.names, name)
		if typ == "person" && s.person == "" && strings.Contains(id, "@") {
			s.person = id
		}
	}
	rows.Close()
	rows, err = db.Query(`SELECT vec FROM statement_vecs WHERE model = ? AND statement_id % 37 = 0 LIMIT 2000`, s.model)
	check(t, err)
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		v := make([]float32, len(b)/4)
		for i := range v {
			u := uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24
			v[i] = math.Float32frombits(u)
		}
		s.vecs = append(s.vecs, v)
	}
	rows.Close()
	for i := 0; i < 20 && i*97 < len(s.vecs); i++ {
		s.queries = append(s.queries, s.vecs[i*97])
	}
	rows, err = db.Query(`SELECT id FROM topics ORDER BY size DESC LIMIT 20`)
	check(t, err)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		s.topics = append(s.topics, id)
	}
	rows.Close()
	if len(s.vecs) == 0 || len(s.entities) == 0 {
		t.Fatal("the database has no statement vectors or mentions")
	}
	return s
}

// perfEmbedder stands in for the embedder: a stored statement vector
// chosen by the text's hash, so lookups land among real neighbours.
type perfEmbedder struct {
	model string
	pool  [][]float32
	calls int
}

func (e *perfEmbedder) Model() string { return e.model }
func (e *perfEmbedder) Local() bool   { return true }
func (e *perfEmbedder) Embed(_ context.Context, texts []string, _ bool) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		h := fnv.New32a()
		h.Write([]byte(t))
		out[i] = e.pool[int(h.Sum32())%len(e.pool)]
		e.calls++
	}
	return out, nil
}

// perfClassifier stands in for the relevance classifier: the first option
// (by name) fits, deterministically.
type perfClassifier struct{ calls int }

func (c *perfClassifier) Name() string { return "perf" }
func (c *perfClassifier) Classify(_ context.Context, _ any, qs map[string]provider.Question) (map[string]provider.Answer, error) {
	c.calls++
	out := map[string]provider.Answer{}
	for k, q := range qs {
		switch q.Type {
		case "choice":
			var names []string
			for n := range q.Options {
				if n != "none" {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			p := map[string]float64{"none": 0.2}
			for i, n := range names {
				p[n] = 0.6 / float64(i+1)
			}
			out[k] = provider.Answer{Probabilities: p}
		default:
			out[k] = provider.Answer{Noul: 0.7}
		}
	}
	return out, nil
}

// perfMeeting plays a synthetic meeting through Live on its own clock
// (stepped: each line's work done before the next), as a live meeting
// (not a replay, whose cards are built once). Each line arrives as drafts
// (the words so far, every 0.5 s), then pending (the last draft's text),
// then final; every 20 s of meeting the model's part is played by two
// queries from the last line, looked up as statements.
func perfMeeting(t *testing.T, eng *engine.Engine, cs *engine.Cards, s perfSampleSet) {
	eng.Cfg.Live.Pick.Enabled = true
	eng.Cfg.Live.KeepTopics = 8
	eng.Cfg.Live.Relevance.Provider = ""
	counts := map[string]int{}
	l := New(eng, func(ev string, d any) {
		tr, ok := d.(Trace)
		switch {
		case !ok:
		case strings.HasPrefix(tr.Title, "picked "):
			counts["picks"]++
		case strings.HasPrefix(tr.Title, "pick skipped"):
			counts["picks skipped"]++
		case strings.HasPrefix(tr.Title, "model run skipped"):
			counts["model runs skipped"]++
		case strings.Contains(tr.Title, "topic card(s) under"):
			counts["card refreshes"]++
		}
	}, nil)
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	rel := &perfClassifier{}
	l.mu.Lock()
	l.rel = rel
	l.mu.Unlock()
	var terms []string
	for _, h := range cs.Index.AllTerms() {
		terms = append(terms, h.Term)
	}
	sort.Strings(terms)
	filler := strings.Fields("so we should look at where this stands and what the next step is for the team this quarter given the numbers we saw last week")
	sess := Session{ID: "perf-meeting", Title: "Synthetic meeting", CreatedAt: time.Now()}
	l.stepped.Store(true)
	defer l.stepped.Store(false)
	l.setStepClock(0)
	l.handle(Event{V: 1, Type: "session", State: "started", Session: &sess}, "live")
	ctx := context.Background()
	var ru0 syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru0)
	t0 := time.Now()
	clock, line, events := 0.0, 0, 0
	lastModel := -1e9
	for clock < *perfMinutes*60 {
		line++
		// A line: filler with a name or a term in it now and then.
		words := append([]string(nil), filler[line%7:line%7+10]...)
		switch {
		case line%3 == 0 && len(s.names) > 0:
			words = append(words, s.names[line%len(s.names)])
		case line%3 == 1 && len(terms) > 0:
			words = append(words, terms[(line*13)%len(terms)])
		}
		id := fmt.Sprintf("s%d", line)
		start := clock
		var draft string
		for n := 2; n <= len(words); n += 2 {
			clock += 0.5
			draft = strings.ToLower(strings.Join(words[:n], " "))
			l.setStepClock(clock)
			l.handle(Event{V: 1, Type: "segment", SessionID: sess.ID, Segment: &Segment{ID: id, Speaker: "Person 2", Text: draft, Start: start, End: clock, Status: "live"}}, "live")
			events++
		}
		for _, st := range []string{"pending", ""} {
			clock += 0.5
			text := draft
			if st == "" {
				text = strings.Join(words, " ") + "."
			}
			l.setStepClock(clock)
			l.handle(Event{V: 1, Type: "segment", SessionID: sess.ID, Segment: &Segment{ID: id, Speaker: "Person 2", Text: text, Start: start, End: clock, Status: st}}, "live")
			events++
		}
		if clock-lastModel >= 20 {
			lastModel = clock
			l.mu.Lock()
			lines := l.meeting.RecentLines(90)
			l.mu.Unlock()
			qs := []Suggestion{{Query: strings.Join(words[len(words)-3:], " "), Kind: "llm"}, {Query: strings.Join(words[:3], " "), Kind: "llm"}}
			l.statementLookup(ctx, l.nextCycle(), qs, []string{"tomoe:" + sess.ID}, "owner", lines)
		}
		clock += 2
	}
	wall := time.Since(t0)
	var ru1 syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru1)
	cpu := time.Duration(ru1.Utime.Nano()-ru0.Utime.Nano()+ru1.Stime.Nano()-ru0.Stime.Nano()) * time.Nanosecond
	fmt.Printf("%-44s %9.1f ms wall, %9.1f ms CPU, %d events, %d lines, %.0f s of meeting\n", "synthetic meeting", ms(wall), ms(cpu), events, line, clock)
	fmt.Printf("%-44s %v; classifier calls %d, embeddings %d\n", "", counts, rel.calls, eng.Embedder.(*perfEmbedder).calls)
	fmt.Printf("%-44s %.1f ms CPU per meeting minute\n", "", ms(cpu)/(clock/60))
}
