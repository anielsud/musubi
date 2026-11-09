// Package pipeline runs ingest and the derive stages. Every stage is
// re-runnable: it runs for an item when its version or input hash differs
// from the stamp it last left, and only then.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/stages"
	"github.com/anielsud/musubi/internal/store"
)

// TranslatorVersion changes when item translation changes, so unchanged raw
// records get re-translated.
const TranslatorVersion = "t3"

const LinkVersion = "l3"

type Runner struct {
	Cfg        config.Config
	Store      *store.Store
	Raw        *raw.Store
	Connectors []source.Connector
	// Embedders may hold several models; each keeps its own vectors so they
	// can be compared on the same data.
	Embedders   []provider.Embedder
	CuratedPath string
	Log         io.Writer
	Workers     int
}

type Stats struct {
	Fetched, Changed, Skipped int
	Ran                       map[string]int
	Elapsed                   time.Duration
	// M365 is the Microsoft 365 sync's report, or its error.
	M365 string
}

func (r *Runner) logf(f string, a ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, f+"\n", a...)
	}
}

// Ingest fetches every connector into the raw store and translates changed
// records into items.
func (r *Runner) Ingest(ctx context.Context, st *Stats) error {
	for _, c := range r.Connectors {
		err := c.Fetch(ctx, func(rec source.Record) error {
			st.Fetched++
			if gone, err := r.Store.IsForgotten(rec.Source, rec.Ref); err != nil || gone {
				st.Skipped++
				return err
			}
			h, err := r.Raw.Put(rec.Data)
			if err != nil {
				return err
			}
			key := h + "/" + TranslatorVersion
			if old, ok, err := r.Store.ManifestHash(rec.Source, rec.Ref); err != nil {
				return err
			} else if ok && old == key {
				return nil
			}
			items, err := item.Translate(rec, h)
			if err != nil {
				r.logf("skip %s/%s: %v", rec.Source, rec.Ref, err)
				st.Skipped++
				return nil
			}
			for _, it := range items {
				changed, err := r.Store.UpsertItem(it)
				if err != nil {
					return err
				}
				if changed {
					st.Changed++
				}
			}
			return r.Store.PutManifest(rec.Source, rec.Ref, key, rec.ContentType)
		})
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name(), err)
		}
	}
	return nil
}

// Process runs the global steps (registry, links) and then the per-item
// stages wherever they are stale.
func (r *Runner) Process(ctx context.Context, st *Stats) error {
	if st.Ran == nil {
		st.Ran = map[string]int{}
	}
	items, err := r.Store.AllItems()
	if err != nil {
		return err
	}
	reg, err := registry.Build(r.Cfg, r.CuratedPath, items)
	if err != nil {
		return err
	}
	if old, _ := r.Store.GlobalStamp("registry"); old != reg.Hash {
		if err := r.Store.ReplaceEntities(reg.Entities, reg.Aliases); err != nil {
			return err
		}
		r.Store.SetGlobalStamp("registry", reg.Hash)
		st.Ran["registry"]++
		r.logf("registry: %d entities, %d aliases", len(reg.Entities), len(reg.Aliases))
	}

	byID := map[string]*item.Item{}
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	linkHash, links := linkMeetings(items)
	if old, _ := r.Store.GlobalStamp("link"); old != linkHash {
		var ls []store.Link
		for m, e := range links {
			ls = append(ls, store.Link{ItemID: m, Rel: "occurred_as", OtherID: e.id, Score: e.score})
		}
		if err := r.Store.ReplaceLinks("occurred_as", ls); err != nil {
			return err
		}
		r.Store.SetGlobalStamp("link", linkHash)
		st.Ran["link"]++
		r.logf("link: %d meetings matched to calendar events", len(ls))
	}
	linked := func(id string) *item.Item {
		if l, ok := links[id]; ok {
			return byID[l.id]
		}
		return nil
	}
	linkKey := func(id string) string {
		if l := linked(id); l != nil {
			return l.ID + "@" + l.ContentHash()
		}
		return ""
	}

	matcher := stages.NewMatcher(reg.Aliases)
	prox := stages.NewProximity(items, reg)
	type embedJob struct {
		it     *item.Item
		e      provider.Embedder
		stage  string
		hash   string
		chunks []store.Chunk
	}
	var jobs []embedJob
	for i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		it := &items[i]
		ch := it.ContentHash()
		title := it.Title
		if l := linked(it.ID); l != nil {
			title = l.Title
		}
		chunkHash := hashOf("chunk", stages.ChunkVersion, ch, title)
		chunks, err := r.ensure("chunk", it.ID, stages.ChunkVersion, chunkHash, st, func() error {
			return r.Store.ReplaceChunks(it.ID, stages.ChunkVersion, chunkHash, stages.Chunk(*it, title))
		})
		if err != nil {
			return err
		}
		if chunks == nil {
			if chunks, err = r.Store.Chunks(it.ID); err != nil {
				return err
			}
		}
		lk := linkKey(it.ID)
		minAud := r.Cfg.Sensitivity.InternalMinAudience
		spk := r.Cfg.Sensitivity.AudienceFromSpeakers
		clsHash := hashOf("classify", stages.ClassifyVersion, chunkHash, reg.Hash, lk, fmt.Sprint(minAud), fmt.Sprint(spk))
		if _, err := r.ensure("classify", it.ID, stages.ClassifyVersion, clsHash, st, func() error {
			il, cls, aud := stages.Classify(*it, linked(it.ID), chunks, reg, r.Cfg.Owner.InternalDomains, minAud, spk)
			return r.Store.ReplaceLabels(it.ID, stages.ClassifyVersion, clsHash, il, cls, aud)
		}); err != nil {
			return err
		}
		// reg.Hash covers every item's participants, so it also covers the
		// proximity counts.
		resHash := hashOf("resolve", stages.ResolveVersion, chunkHash, reg.Hash, lk)
		if _, err := r.ensure("resolve", it.ID, stages.ResolveVersion, resHash, st, func() error {
			ms, ies := stages.Resolve(*it, linked(it.ID), chunks, reg, matcher, prox)
			return r.Store.ReplaceMentions(it.ID, stages.ResolveVersion, resHash, ms, ies)
		}); err != nil {
			return err
		}
		for _, e := range r.Embedders {
			stage := "embed/" + e.Model()
			h := hashOf(stage, stages.EmbedVersion, chunkHash)
			if cur, _ := r.Store.StageStamp(stage, it.ID); cur != stages.EmbedVersion+"/"+h {
				jobs = append(jobs, embedJob{it, e, stage, h, chunks})
			}
		}
	}
	return r.runPool(ctx, st, "embed", len(jobs), func(yield func(func() (func() error, error))) {
		for _, j := range jobs {
			j := j
			yield(func() (func() error, error) {
				vecs, err := stages.Embed(ctx, j.e, j.chunks)
				if err != nil {
					return nil, fmt.Errorf("embed %s: %w", j.it.ID, err)
				}
				return func() error {
					return r.Store.PutVecs(j.it.ID, j.stage, j.e.Model(), stages.EmbedVersion, j.hash, vecs)
				}, nil
			})
		}
	})
}

// ensure runs fn if the stage's stamp is stale. It returns the item's fresh
// chunks when the chunk stage ran, nil otherwise.
func (r *Runner) ensure(stage, itemID, version, hash string, st *Stats, fn func() error) ([]store.Chunk, error) {
	cur, err := r.Store.StageStamp(stage, itemID)
	if err != nil {
		return nil, err
	}
	if cur == version+"/"+hash {
		return nil, nil
	}
	if err := fn(); err != nil {
		return nil, fmt.Errorf("%s %s: %w", stage, itemID, err)
	}
	st.Ran[stage]++
	if stage == "chunk" {
		return r.Store.Chunks(itemID)
	}
	return nil, nil
}

// runPool computes on several workers (the model server is the
// bottleneck) and writes results on this goroutine, since SQLite has one
// writer.
func (r *Runner) runPool(ctx context.Context, st *Stats, label string, n int, each func(func(func() (func() error, error)))) error {
	if n == 0 {
		return nil
	}
	workers := r.Workers
	if workers <= 0 {
		workers = min(4, runtime.NumCPU())
	}
	type result struct {
		write func() error
		err   error
	}
	tasks := make(chan func() (func() error, error))
	results := make(chan result)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				wfn, err := t()
				results <- result{wfn, err}
			}
		}()
	}
	go func() {
		each(func(t func() (func() error, error)) {
			select {
			case tasks <- t:
			case <-ctx.Done():
			}
		})
		close(tasks)
		wg.Wait()
		close(results)
	}()
	done := 0
	var firstErr error
	start := time.Now()
	for res := range results {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		if err := res.write(); err != nil && firstErr == nil {
			firstErr = err
		}
		done++
		st.Ran[label]++
		if done%25 == 0 || done == n {
			r.logf("%s: %d/%d items (%s)", label, done, n, time.Since(start).Round(time.Second))
		}
	}
	return firstErr
}

type linkTarget struct {
	id    string
	score float64
}

// linkMeetings matches each Tomoe recording to the calendar event it most
// likely was: the event must contain the recording's start (with 15 minutes
// of slack either side) and overlap a good share of it.
func linkMeetings(items []item.Item) (string, map[string]linkTarget) {
	var meetings, events []item.Item
	h := sha256.New()
	h.Write([]byte(LinkVersion))
	for _, it := range items {
		switch {
		case it.Kind == item.KindMeeting:
			meetings = append(meetings, it)
		case it.Kind == item.KindEvent && linkable(it):
			events = append(events, it)
		default:
			continue
		}
		fmt.Fprintf(h, "%s %d %d|", it.ID, it.OccurredAt.Unix(), it.EndedAt.Unix())
	}
	const slack = 15 * time.Minute
	out := map[string]linkTarget{}
	for _, m := range meetings {
		mDur := m.EndedAt.Sub(m.OccurredAt)
		var best linkTarget
		var bestGap time.Duration
		for _, e := range events {
			if m.OccurredAt.Before(e.OccurredAt.Add(-slack)) || m.OccurredAt.After(e.EndedAt) {
				continue
			}
			lo, hi := maxT(m.OccurredAt, e.OccurredAt), minT(m.EndedAt, e.EndedAt)
			overlap := hi.Sub(lo)
			denom := min(mDur, e.EndedAt.Sub(e.OccurredAt))
			score := 0.0
			if denom > 0 && overlap > 0 {
				score = float64(overlap) / float64(denom)
			}
			// A short recording at the start of an event still belongs to it.
			if mDur < 5*time.Minute && overlap > 0 {
				score = max(score, 0.5)
			}
			if score > 0 {
				score += preference(m, e)
			}
			gap := absDur(m.OccurredAt.Sub(e.OccurredAt))
			if score > best.score || (score == best.score && score > 0 && gap < bestGap) {
				best, bestGap = linkTarget{e.ID, score}, gap
			}
		}
		if best.score >= 0.3 {
			out[m.ID] = best
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), out
}

// preference breaks ties between overlapping events: the one whose subject
// the recording's title names (Tomoe titles a recording from the meeting
// window), then the ones the owner organized or accepted over those they
// left unanswered or declined.
func preference(m, e item.Item) float64 {
	p := 0.0
	subj, title := normTitle(e.Title), normTitle(m.Title)
	switch {
	case len(subj) >= 4 && (strings.Contains(title, subj) || len(title) >= 4 && strings.Contains(subj, title)):
		p += 0.5
	case title != "" && !genericTitle.MatchString(m.Title):
		// The recording names a different meeting (an ad-hoc call with
		// someone, during an event the owner didn't join): not this event.
		p -= 1
	}
	switch e.Native["response"] {
	case "organizer", "accepted":
		p += 0.1
	case "tentativelyAccepted":
		p += 0.05
	case "declined":
		p -= 0.2
	}
	return p
}

// genericTitle is the name Tomoe gives a recording when it saw no meeting
// window title ("Teams Meeting 2026-11-06 10:02", "Session 2026-11-06 10:02").
var genericTitle = regexp.MustCompile(`^((\w+ )?Meeting|Session) \d{4}-\d\d-\d\d \d\d:\d\d$`)

func normTitle(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// linkable excludes calendar entries that aren't meetings: all-day or very
// long blocks ("No internal meetings", travel), and free or cancelled
// entries. Linking a recording to one would hand it the block's attendee
// list, and with it a label that is too open.
func linkable(e item.Item) bool {
	switch {
	case e.Native["cancelled"] == "true", e.Native["allDay"] == "true", e.Native["showAs"] == "free":
		return false
	case e.EndedAt.Sub(e.OccurredAt) > 4*time.Hour:
		return false
	}
	return true
}

func hashOf(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

func maxT(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minT(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
