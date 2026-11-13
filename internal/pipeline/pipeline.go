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
	"slices"
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

const LinkVersion = "l4"

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
	// rechunkIDs are held passages a run applying upgrades re-cuts (see
	// passagesWait); rechunkAll re-cuts them all.
	rechunkIDs map[string]bool
	rechunkAll bool
}

func (r *Runner) rechunk(id string) bool { return r.rechunkAll || r.rechunkIDs[id] }

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
	linkHash, links := linkMeetings(items, r.Cfg.Owner.Name)
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
	// A recording that missed part of its meeting takes those stretches
	// from the meeting's Teams transcript (knowledge.stitch), from here on.
	stitchedItems, into, err := r.stitches(items, true)
	if err != nil {
		return err
	}
	partner := stitchedRaw(into, byID)
	for i := range items {
		if s, ok := stitchedItems[items[i].ID]; ok {
			items[i] = s
		}
	}
	if len(stitchedItems) > 0 {
		r.logf("stitch: %d recordings filled from their Teams transcript", len(stitchedItems))
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
	given, err := r.Store.GivenTitles()
	if err != nil {
		return err
	}
	held := 0
	for i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		it := &items[i]
		title := r.passageTitle(it, linked(it.ID), given[it.ID].Title)
		pk := newPassageKey(it, title, partner[it.ID])
		chunkHash := pk.hash
		var chunks []store.Chunk
		if stored, wait := r.passagesWait(it.ID, pk); wait && !r.rechunk(it.ID) {
			// Re-cutting them would delete what was read from them, and
			// reading them again costs model calls: they wait for the owner
			// (knowledge.hold_upgrades), and the steps below keep working
			// from them.
			held++
			chunkHash = stored
		} else {
			if chunks, err = r.ensure("chunk", it.ID, chunkVersion, chunkHash, st, func() error {
				return r.Store.ReplaceChunks(it.ID, chunkVersion, chunkHash, chunkPassages(*it, title))
			}); err != nil {
				return err
			}
			if was, _ := r.Store.StageStamp("chunk-source", it.ID); was != "src/"+pk.source {
				if err := r.Store.SetChunkSource(it.ID, pk.source); err != nil {
					return err
				}
			}
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
	if held > 0 {
		r.logf("chunk: %d meetings keep their passages until an upgrade is applied (knowledge.hold_upgrades)", held)
	}
	r.setWaiting("passages", held)
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

// passageTitle is the title an item's passages carry: its calendar
// event's subject, else a name Musubi gave it, else its own.
func (r *Runner) passageTitle(it, linked *item.Item, given string) string {
	title := it.Title
	if linked != nil {
		title = linked.Title
	} else if given != "" {
		title = given
	}
	// An extracted item keeps the title its passages were extracted
	// under: a later link to a calendar event (or a new generated name)
	// would otherwise change the chunk stamp, and so re-extract it.
	if ex, _ := r.Store.StageStamp("extract", it.ID); ex != "" {
		if was, err := r.Store.ChunkTitle(it.ID); err == nil && was != "" {
			title = was
		}
	}
	return title
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
func linkMeetings(items []item.Item, owner string) (string, map[string]linkTarget) {
	var meetings, events []item.Item
	h := sha256.New()
	h.Write([]byte(LinkVersion + "\x00" + owner))
	for _, it := range items {
		switch {
		case it.Kind == item.KindMeeting:
			meetings = append(meetings, it)
		case it.Kind == item.KindEvent && linkable(it):
			events = append(events, it)
		default:
			continue
		}
		fmt.Fprintf(h, "%s %d %d %v|", it.ID, it.OccurredAt.Unix(), it.End().Unix(), others(it, owner))
	}
	const slack = 15 * time.Minute
	out := map[string]linkTarget{}
	for _, m := range meetings {
		mEnd := m.End()
		mDur := mEnd.Sub(m.OccurredAt)
		var best linkTarget
		var bestGap time.Duration
		for _, e := range events {
			if m.OccurredAt.Before(e.OccurredAt.Add(-slack)) || m.OccurredAt.After(e.EndedAt) {
				continue
			}
			lo, hi := maxT(m.OccurredAt, e.OccurredAt), minT(mEnd, e.EndedAt)
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
				score += preference(m, e, owner)
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
func preference(m, e item.Item, owner string) float64 {
	p := 0.0
	subj, title := normTitle(e.Title), normTitle(m.Title)
	switch {
	case len(subj) >= 4 && (strings.Contains(title, subj) || len(title) >= 4 && strings.Contains(subj, title)):
		p += 0.5
	case title != "" && !genericTitle.MatchString(m.Title) && !samePeople(m, e, owner):
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

// samePeople: the recording heard the event's invitees and no one else (a
// call that stood in for the meeting, titled for the caller).
func samePeople(m, e item.Item, owner string) bool {
	heard := others(m, owner)
	return len(heard) > 0 && slices.Equal(heard, others(e, owner))
}

// others are an item's participants but the owner, by name, lowercased
// and sorted.
func others(it item.Item, owner string) []string {
	var out []string
	for _, p := range it.Participants {
		n := strings.ToLower(strings.TrimSpace(p.Name))
		if p.Owner || n == "" || strings.EqualFold(n, owner) {
			continue
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return slices.Compact(out)
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
