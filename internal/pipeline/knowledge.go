package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/stages"
	"github.com/anielsud/musubi/internal/store"
)

type KnowledgeOptions struct {
	LLM provider.LLM
	// Extractor takes points from meetings (knowledge.extract = "points");
	// nil uses LLM.
	Extractor provider.LLM
	Embedder  provider.Embedder
	// Items limits extraction to these item ids (or id prefixes); empty
	// means every meeting, email and chat.
	Items []string
	// Limit caps how many items are extracted this run (0: no cap).
	Limit   int
	Workers int
	// Decider answers the yes/no and pick-one questions behind the gate,
	// worth scores and the pair judge (config.Knowledge); nil uses LLM when
	// it can.
	Decider provider.Decider
	// LinkTrace receives the linker's decisions.
	LinkTrace func(format string, args ...any)
	// Threader does the threading (knowledge.grouping = "threads"); nil
	// builds it from knowledge.threads.model, else uses LLM.
	Threader provider.LLM
	// Redo names stages to bring up to their current version now: "extract"
	// (items extracted under an earlier version; RedoNewest of them, newest
	// first, when set), "links" (claims; in thread mode this rethreads too,
	// as threading's relations live on claims), "threads" (every item
	// rethreaded in order).
	Redo       []string
	RedoNewest int
	// Plan reports what a run would do, and what is on an earlier version,
	// without extracting, linking or threading.
	Plan bool
	// NoThreads stops after linking: threads are made elsewhere (the
	// benchmark's hosted prototypes, applied with apply_threads.py).
	NoThreads bool
	// StatusLLM writes thread statuses; nil builds it from
	// knowledge.status.model, else uses the threads model.
	StatusLLM provider.LLM
}

func (o KnowledgeOptions) redo(stage string) bool {
	for _, s := range o.Redo {
		if s == stage {
			return true
		}
	}
	return false
}

// Outdated is what was done under an earlier version and kept
// (knowledge.updates = "explicit").
type Outdated struct {
	Extract int  // items extracted under an earlier version
	Links   bool // claims built under earlier linking rules
	Threads int  // items threaded under earlier rules
	// Backfilled: items threaded after newer ones (older meetings added
	// later); --redo threads puts everything in time order.
	Backfilled int
}

// Planned is what a run would do (KnowledgeOptions.Plan).
type Planned struct {
	Extract        int  // items to extract: new, changed, or asked to redo
	RebuildLinks   bool // claims to rebuild from stored statements
	Thread         int  // items to thread
	RebuildThreads bool // every thread cleared first
}

type KnowledgeStats struct {
	Items, Statements, Embedded int
	Chunks                      int
	Gated                       int // passages the gate skipped
	Scored, Unworthy            int // statements scored for worth; below MinWorth
	ExtractTime, LinkTime       time.Duration
	Link                        knowledge.LinkStats
	Topics                      int
	Pending                     int // items still to extract (when limited)
	Labelled, Checked           int // statements labelled sensitive; checked by the labeller
	Statuses, StatusFailed      int // thread statuses written; failed (tried again next run)
	Threads                     knowledge.ThreadStats
	Outdated                    Outdated
	Planned                     Planned
}

// Knowledge runs the knowledge layer: extract statements from items whose
// chunks changed, embed them, link them into claims, and rebuild topics if
// the claims changed. Run Process first.
func (r *Runner) Knowledge(ctx context.Context, o KnowledgeOptions) (KnowledgeStats, error) {
	var ks KnowledgeStats
	if o.LLM == nil || o.Embedder == nil {
		return ks, fmt.Errorf("the knowledge layer needs a chat model and an embedder")
	}
	items, err := r.Store.AllItems()
	if err != nil {
		return ks, err
	}
	reg, err := registry.Build(r.Cfg, r.CuratedPath, items)
	if err != nil {
		return ks, err
	}
	links, err := r.Store.LinksFrom("occurred_as")
	if err != nil {
		return ks, err
	}
	byID := map[string]*item.Item{}
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	entName := map[string]string{}
	for _, e := range reg.Entities {
		entName[e.ID] = e.Name
	}
	// speakerOrgs maps an item's outside speakers to their organisation.
	speakerOrgs := func(it *item.Item) map[string]string {
		out := map[string]string{}
		ps := append([]item.Participant(nil), it.Participants...)
		if e, ok := links[it.ID]; ok && byID[e] != nil {
			ps = append(ps, byID[e].Participants...)
		}
		for _, p := range ps {
			org := entName[reg.OrgFor(p.Email)]
			if p.Email == "" || org == "" {
				continue
			}
			name := p.Name
			if name == "" {
				name = entName[reg.Participant(p)]
			}
			if name != "" {
				out[strings.ToLower(name)] = org
			}
		}
		return out
	}
	wanted := func(id string) bool {
		if len(o.Items) == 0 {
			return true
		}
		for _, p := range o.Items {
			if strings.HasPrefix(id, p) {
				return true
			}
		}
		return false
	}

	kc := r.Cfg.Knowledge
	if o.Decider == nil {
		o.Decider, _ = o.LLM.(provider.Decider)
	}
	gated := kc.Gate > 0 && o.Decider != nil
	if o.Extractor == nil {
		o.Extractor = o.LLM
	}
	if kc.Extract == "points" && !o.Extractor.Local() && !kc.AllowHosted {
		return ks, fmt.Errorf("points extraction reads meeting text, so it needs a local model (got %s)", o.Extractor.ModelName())
	}
	pc := kc.Points
	// invitees are who the calendar says was invited, else who was heard.
	invitees := func(it *item.Item) []knowledge.Invitee {
		ps := it.Participants
		if e, ok := links[it.ID]; ok && byID[e] != nil {
			ps = byID[e].Participants
		}
		var out []knowledge.Invitee
		for _, p := range ps {
			if !p.Owner {
				out = append(out, knowledge.Invitee{Name: p.Name, Email: p.Email})
			}
		}
		return out
	}
	// 1. Extract, for items whose chunks changed since they were extracted.
	type job struct {
		it      *item.Item
		hash    string
		input   string // the hash of what it reads, without version or model
		version string
		chunks  []store.Chunk
		title   string
		orgs    map[string]string
		known   string // points: the facts sheet; "" extracts statements
	}
	var jobs, older []job
	for i := range items {
		it := &items[i]
		if it.Kind == item.KindEvent || !wanted(it.ID) || kc.Extract == "none" {
			continue
		}
		chunkStamp, err := r.Store.StageStamp("chunk", it.ID)
		if err != nil || chunkStamp == "" {
			continue
		}
		version, known := knowledge.ExtractVersion, ""
		h := hashOf("extract", knowledge.ExtractVersion, chunkStamp, o.LLM.ModelName(), r.Cfg.Owner.Name)
		input := hashOf("input", chunkStamp)
		if gated {
			h = hashOf(h, knowledge.GateVersion, fmt.Sprint(kc.Gate))
		}
		if kc.Extract == "points" && it.Kind == item.KindMeeting {
			known = knowledge.KnownFacts(r.Cfg.Owner.Name, r.Cfg.Owner.Email, r.Cfg.Owner.InternalDomains, invitees(it))
			// Points carry their passages' labels, so relabelling re-extracts.
			clsStamp, _ := r.Store.StageStamp("classify", it.ID)
			version = knowledge.PointsVersion
			h = hashOf("points", version, chunkStamp, clsStamp, o.Extractor.ModelName(), r.Cfg.Owner.Name, known,
				fmt.Sprint(pc.Think), fmt.Sprint(pc.Segment))
			input = hashOf("input", chunkStamp, clsStamp, known)
		}
		cur, _ := r.Store.StageStamp("extract", it.ID)
		if cur == version+"/"+h {
			continue
		}
		title := it.Title
		if e, ok := links[it.ID]; ok && byID[e] != nil {
			title = byID[e].Title
		}
		j := job{it: it, hash: h, input: input, version: version, title: title, orgs: speakerOrgs(it), known: known}
		if cur != "" && kc.Updates != "auto" {
			// Extracted before under another version. Its input changed too
			// (or it is new) → extract; only the version changed → keep it
			// until asked. Items extracted before inputs were recorded count
			// as unchanged.
			if in, _ := r.Store.StageStamp("extract-input", it.ID); in == "" || in == "in/"+input {
				older = append(older, j)
				continue
			}
		}
		jobs = append(jobs, j)
	}
	if o.redo("extract") && len(older) > 0 {
		sort.Slice(older, func(a, b int) bool { return older[a].it.OccurredAt.After(older[b].it.OccurredAt) })
		n := len(older)
		if o.RedoNewest > 0 && o.RedoNewest < n {
			n = o.RedoNewest
		}
		r.logf("extract: redoing %d of %d items extracted under an earlier version, newest first", n, len(older))
		jobs = append(jobs, older[:n]...)
		older = older[n:]
	}
	ks.Outdated.Extract = len(older)
	if o.Limit > 0 && len(jobs) > o.Limit {
		ks.Pending = len(jobs) - o.Limit
		jobs = jobs[:o.Limit]
	}
	if o.Plan {
		ks.Planned.Extract = len(jobs)
		requeued := map[string]bool{}
		for _, j := range jobs {
			requeued[j.it.ID] = true
		}
		return ks, r.planLinksAndThreads(o, requeued, &ks)
	}
	for i := range jobs {
		if jobs[i].chunks, err = r.Store.Chunks(jobs[i].it.ID); err != nil {
			return ks, err
		}
	}
	for _, j := range jobs {
		ks.Chunks += len(j.chunks)
	}
	tExtract := time.Now()
	var gatedN atomic.Int64
	workers := r.Workers
	r.Workers = o.Workers
	var st Stats
	st.Ran = map[string]int{}
	err = r.runPool(ctx, &st, "extract", len(jobs), func(yield func(func() (func() error, error))) {
		for _, j := range jobs {
			j := j
			yield(func() (func() error, error) {
				if j.known != "" {
					sts, err := r.extractPoints(ctx, o.Extractor, pc, reg, j.it, j.title, j.known, j.chunks)
					if err != nil {
						return nil, fmt.Errorf("points %s: %w", j.it.ID, err)
					}
					return func() error {
						ks.Items++
						ks.Statements += len(sts)
						return r.Store.ReplaceStatementsFrom(j.it.ID, j.version, j.hash, j.input, sts)
					}, nil
				}
				var sts []store.Statement
				tried, failed := 0, 0
				var lastErr error
				for _, c := range j.chunks {
					lines := knowledge.SplitLines(c.Text)
					if len(lines) == 0 {
						continue
					}
					if gated {
						p, err := knowledge.Worth(ctx, o.Decider, j.title, lines)
						if err == nil && p < kc.Gate {
							r.logf("extract %s#%d gated (p=%.2f)", j.it.ID, c.Seq, p)
							gatedN.Add(1)
							continue
						}
						// A failed gate call extracts: the gate only saves time.
					}
					tried++
					ex, err := knowledge.Extract(ctx, o.LLM, r.Cfg.Owner.Name, j.title, lines, j.orgs)
					if err != nil {
						if ctx.Err() != nil {
							return nil, err
						}
						// One bad reply (e.g. cut off at the token limit) skips
						// that passage, not the run. A failure on every passage
						// is a broken model or cache, which must not pass as
						// "nothing worth extracting".
						r.logf("extract %s#%d skipped: %v", j.it.ID, c.Seq, err)
						failed++
						lastErr = err
						continue
					}
					for _, e := range ex {
						holderID := ""
						if e.Holder == "You" || strings.EqualFold(e.Holder, r.Cfg.Owner.Name) {
							holderID = reg.Owner()
						} else if e.Holder != "" {
							holderID = reg.Participant(item.Participant{Name: e.Holder})
						}
						sts = append(sts, knowledge.ToStatement(e, c, j.it.OccurredAt, holderID, lines))
					}
				}
				if tried > 0 && failed == tried {
					return nil, fmt.Errorf("extract %s: every passage failed: %w", j.it.ID, lastErr)
				}
				return func() error {
					ks.Items++
					ks.Statements += len(sts)
					return r.Store.ReplaceStatementsFrom(j.it.ID, knowledge.ExtractVersion, j.hash, j.input, sts)
				}, nil
			})
		}
	})
	r.Workers = workers
	ks.ExtractTime = time.Since(tExtract)
	ks.Gated = int(gatedN.Load())
	if err != nil {
		return ks, err
	}

	// 2. Embed statements.
	missing, err := r.Store.StatementsWithoutVec(o.Embedder.Model())
	if err != nil {
		return ks, err
	}
	for i := 0; i < len(missing); i += 64 {
		batch := missing[i:min(i+64, len(missing))]
		texts := make([]string, len(batch))
		for k, s := range batch {
			texts[k] = s.Text
		}
		vs, err := o.Embedder.Embed(ctx, texts, false)
		if err != nil {
			return ks, err
		}
		for k, s := range batch {
			if err := r.Store.PutStatementVec(s.ID, o.Embedder.Model(), vs[k]); err != nil {
				return ks, err
			}
		}
		ks.Embedded += len(batch)
	}

	// 2a. Search text, for statements extracted before it existed or
	// before the way it is made changed.
	old, err := r.Store.StatementsWithoutSearchText()
	if err != nil {
		return ks, err
	}
	if cur, _ := r.Store.GlobalStamp("search"); cur != knowledge.SearchVersion {
		if old, err = r.Store.AllStatements(); err != nil {
			return ks, err
		}
	}
	for _, s := range old {
		if f := knowledge.SearchForm(s.Text); f != s.SearchText {
			if err := r.Store.SetSearchText(s.ID, f); err != nil {
				return ks, err
			}
		}
	}
	if err := r.Store.SetGlobalStamp("search", knowledge.SearchVersion); err != nil {
		return ks, err
	}

	// 2b. Score statements for worth, once per scorer version.
	if kc.MinWorth > 0 && o.Decider != nil {
		todo, err := r.Store.StatementsToScore(knowledge.WorthVersion)
		if err != nil {
			return ks, err
		}
		for _, s := range todo {
			if s.Status != "" {
				// Points: the extraction already judged them worth keeping,
				// and the worth prompt drops personal details.
				continue
			}
			p, err := knowledge.StatementWorth(ctx, o.Decider, s.Text)
			if err != nil {
				if ctx.Err() != nil {
					return ks, err
				}
				// Unscored statements are kept: scoring only filters.
				r.logf("worth %d skipped: %v", s.ID, err)
				continue
			}
			if err := r.Store.SetWorth(s.ID, p, knowledge.WorthVersion); err != nil {
				return ks, err
			}
			ks.Scored++
		}
	}

	// 2b. Label statements that reveal sensitive things about people.
	if kc.Label.Enabled {
		if err := r.labelStatements(ctx, o, &ks); err != nil {
			return ks, err
		}
	}

	// 3. Link, oldest first.
	if _, err := r.Store.DropEmptyClaims(); err != nil {
		return ks, err
	}
	vecs, err := r.Store.StatementVecs(o.Embedder.Model())
	if err != nil {
		return ks, err
	}
	threads := kc.Grouping == "threads"
	lk := &knowledge.Linker{Store: r.Store, LLM: o.LLM, Vecs: vecs, Trace: o.LinkTrace, Entities: topicEntities(reg), Strict: kc.StrictLinks}
	if kc.Judge == "decide" && o.Decider != nil {
		lk.Decider, lk.ConsiderAt, lk.MaxJudged = o.Decider, 0.65, 6
	}
	if threads {
		// Threads relate statements with the whole subject in view; the
		// pairwise judge would only add its own, less reliable relations.
		lk.LLM, lk.Decider = nil, nil
	}
	// Claims are cheap to rebuild from stored statements, but in thread mode
	// threading's relations live on them, so a rebuild means rethreading:
	// done when asked (or with knowledge.updates = "auto").
	linkStamp, rebuildLinks, linksOld := r.linkDecision(o)
	ks.Outdated.Links = linksOld
	if linksOld {
		r.logf("link: claims were built under earlier linking rules and are kept; new statements are linked under the current ones (musubi knowledge --redo links)")
	}
	// The claims stamp is separate from the calendar-link stamp ("link"),
	// which Process sets; sharing one name made every update look like a
	// rule change and rebuilt all claims.
	// The stamp is recorded before linking, so a run that is interrupted
	// resumes under the same rules instead of clearing its progress; claims
	// are cleared only when the rules really change. A database linked
	// before this change and never stamped keeps its claims.
	if rebuildLinks {
		if err := r.Store.ClearClaims(); err != nil {
			return ks, err
		}
		r.logf("link: rebuilding claims under the current rules")
	}
	if err := r.Store.SetGlobalStamp("claims", linkStamp); err != nil {
		return ks, err
	}
	if err := lk.Load(); err != nil {
		return ks, err
	}
	unlinked, err := r.Store.UnlinkedStatements()
	if err != nil {
		return ks, err
	}
	if kc.MinWorth > 0 {
		// Left unlinked rather than deleted: lowering the threshold links
		// them on the next run.
		keep := unlinked[:0]
		for _, s := range unlinked {
			if s.Status == "" && s.Worth >= 0 && s.Worth < kc.MinWorth {
				ks.Unworthy++
				continue
			}
			keep = append(keep, s)
		}
		unlinked = keep
	}
	t0 := time.Now()
	if ks.Link, err = lk.Link(ctx, unlinked); err != nil {
		return ks, err
	}
	ks.LinkTime = time.Since(t0)
	if err := r.Store.SetGlobalStamp("claims", linkStamp); err != nil {
		return ks, err
	}
	if len(unlinked) > 0 {
		r.logf("link: %d statements (%d new claims, %d joined, %d judged, %d relations) in %s",
			len(unlinked), ks.Link.New, ks.Link.Joined, ks.Link.Judged, ks.Link.Related, time.Since(t0).Round(time.Second))
	}

	// 4. Threads or topics.
	if threads && !o.NoThreads {
		if err := r.thread(ctx, o, linkStamp, rebuildLinks, &ks); err != nil {
			return ks, err
		}
	}
	if threads {
		// Threads made elsewhere (NoThreads) get statuses too.
		if err := r.writeStatuses(ctx, o, &ks); err != nil {
			return ks, err
		}
		if kc.ProposeAliases {
			n, err := knowledge.ProposeAliases(r.Store, r.Cfg.Owner.Name)
			if err != nil {
				return ks, err
			}
			r.logf("aliases: %d candidates proposed", n)
		}
		return ks, nil
	}
	sig, err := r.Store.ClaimSignature()
	if err != nil {
		return ks, err
	}
	h := hashOf("topics", knowledge.TopicsVersion, sig, o.LLM.ModelName(), linkStamp)
	if old, _ := r.Store.GlobalStamp("topics"); old != h {
		tp := &knowledge.Topicer{Store: r.Store, LLM: o.LLM, Entities: topicEntities(reg)}
		if ks.Topics, err = tp.Build(ctx); err != nil {
			return ks, err
		}
		r.Store.SetGlobalStamp("topics", h)
		r.logf("topics: %d", ks.Topics)
	}
	return ks, nil
}

// extractPoints takes a meeting's points and turns them into statements.
func (r *Runner) extractPoints(ctx context.Context, llm provider.LLM, pc config.Points, reg *registry.Registry, it *item.Item, title, known string, chunks []store.Chunk) ([]store.Statement, error) {
	labels, err := r.Store.ItemLabels(it.ID)
	if err != nil {
		return nil, err
	}
	m := knowledge.NewMeeting(title, it.OccurredAt, known, r.Cfg.Owner.Name, chunks)
	px := &knowledge.Pointer{LLM: llm, Think: pc.Think, Segment: pc.Segment, Parallel: pc.Parallel, WarmFirst: r.Cfg.Knowledge.WarmCache, Logf: r.logf}
	ps, err := px.Extract(ctx, m)
	if err != nil {
		return nil, err
	}
	holder := func(name string) string {
		switch {
		case name == "":
			return ""
		case name == "You" || strings.EqualFold(name, r.Cfg.Owner.Name):
			return reg.Owner()
		}
		return reg.Participant(item.Participant{Name: name})
	}
	sts := make([]store.Statement, 0, len(ps))
	flagged := 0
	for _, p := range ps {
		if len(p.Issues) > 0 {
			flagged++
		}
		sts = append(sts, m.PointStatement(p, it.OccurredAt, holder, labels))
	}
	r.logf("points %s: %d points, %d flagged", it.ID, len(ps), flagged)
	return sts, nil
}

// topicEntities finds the organisations and projects a statement names, for
// keeping topics about different customers apart and linking topics about
// the same one. Not people (they run across topics), and not subject terms
// ("SSO"): a shared subject across customers is what "parallel" means.
func topicEntities(reg *registry.Registry) func(string) []string {
	m := stages.NewMatcher(reg.Aliases)
	types := map[string]string{}
	for _, e := range reg.Entities {
		types[e.ID] = e.Type
	}
	return func(text string) []string {
		var out []string
		seen := map[string]bool{}
		for _, c := range m.Find(text, true) {
			for _, a := range c.Aliases {
				t := types[a.EntityID]
				if (t == "org" || t == "project") && !seen[a.EntityID] {
					seen[a.EntityID] = true
					out = append(out, a.EntityID)
				}
			}
		}
		return out
	}
}

// LinkThreads derives the threads' centroids, entities and relations (no
// model call), for the topic map and topic pages.
func (r *Runner) LinkThreads() (int, error) {
	items, err := r.Store.AllItems()
	if err != nil {
		return 0, err
	}
	reg, err := registry.Build(r.Cfg, r.CuratedPath, items)
	if err != nil {
		return 0, err
	}
	return knowledge.LinkThreads(r.Store, topicEntities(reg))
}

// ImportVersion stamps statements loaded by ImportItems, so a later
// extraction (knowledge.extract other than "none") would replace them.
const ImportVersion = "import2"

// ImportItems replaces an item's statements with items extracted outside
// Musubi (see knowledge.Imported). It returns how many were stored and how
// many were skipped (unknown kind, or no line inside the meeting).
func (r *Runner) ImportItems(itemID string, imported []knowledge.Imported, sourceTag string) (int, int, error) {
	items, err := r.Store.AllItems()
	if err != nil {
		return 0, 0, err
	}
	var it *item.Item
	for i := range items {
		if items[i].ID == itemID {
			it = &items[i]
		}
	}
	if it == nil {
		return 0, 0, fmt.Errorf("no item %s", itemID)
	}
	reg, err := registry.Build(r.Cfg, r.CuratedPath, items)
	if err != nil {
		return 0, 0, err
	}
	chunks, err := r.Store.Chunks(itemID)
	if err != nil {
		return 0, 0, err
	}
	labels, err := r.Store.ItemLabels(itemID)
	if err != nil {
		return 0, 0, err
	}
	// The check accepts figures and names the meeting itself vouches for:
	// its date (extractors write "on 2026-09-16") and the full names of
	// known people (extractors complete "Sam" to a surname nobody said).
	known := ""
	if r.Cfg.Knowledge.ImportKeepProposed {
		var b strings.Builder
		fmt.Fprintf(&b, "Meeting date: %s, %s.", it.OccurredAt.Format("2006-01-02"), it.OccurredAt.Format("2 January 2006"))
		for _, e := range reg.Entities {
			if e.Type == "person" {
				fmt.Fprintf(&b, " %s.", e.Name)
			}
		}
		known = b.String()
	}
	m := knowledge.NewMeeting(it.Title, it.OccurredAt, known, r.Cfg.Owner.Name, chunks)
	holder := func(name string) string {
		switch {
		case name == "":
			return ""
		case name == "You" || strings.EqualFold(name, r.Cfg.Owner.Name):
			return reg.Owner()
		}
		return reg.Participant(item.Participant{Name: name})
	}
	sts, skipped := m.ImportStatements(imported, it.OccurredAt, holder, labels, r.Cfg.Knowledge.ImportKeepProposed)
	chunkStamp, _ := r.Store.StageStamp("chunk", itemID)
	if err := r.Store.ReplaceStatements(itemID, ImportVersion, hashOf("import", chunkStamp, sourceTag), sts); err != nil {
		return 0, 0, err
	}
	return len(sts), skipped, nil
}

// thread threads every item not yet threaded under the current rules,
// oldest first (knowledge.grouping = "threads").
// linkStamp is the version of the rules claims are built under.
func (r *Runner) linkStamp(o KnowledgeOptions) string {
	kc := r.Cfg.Knowledge
	return hashOf("link", knowledge.LinkVersion, o.LLM.ModelName(), fmt.Sprint(kc.MinWorth), kc.Judge, knowledge.WorthVersion, fmt.Sprint(kc.StrictLinks), kc.Grouping)
}

// linkDecision returns the version claims stand under after this run,
// whether to rebuild them now, and whether they are kept under an earlier
// version.
func (r *Runner) linkDecision(o KnowledgeOptions) (stamp string, rebuild, older bool) {
	cur := r.linkStamp(o)
	old, _ := r.Store.GlobalStamp("claims")
	switch {
	case old == "" || old == cur:
		return cur, false, false
	case r.Cfg.Knowledge.Updates == "auto" || o.redo("links"):
		return cur, true, false
	}
	return old, false, true
}

// threadPlan is what threading will do.
type threadPlan struct {
	stamp, old string
	rebuild    bool // clear every thread and rethread every item
	todo       []knowledge.ThreadItem
	older      int // items threaded under earlier rules, kept
}

func (r *Runner) threadLLM(o KnowledgeOptions) (provider.LLM, error) {
	llm := o.Threader
	if llm == nil && r.Cfg.Knowledge.Threads.Model.Provider != "" {
		var err error
		if llm, err = provider.NewLLM(r.Cfg.Knowledge.Threads.Model); err != nil {
			return nil, err
		}
	}
	if llm == nil {
		llm = o.LLM
	}
	if !llm.Local() && !r.Cfg.Knowledge.AllowHosted {
		return nil, fmt.Errorf("threads read meeting text, so they need a local model, or knowledge.allow_hosted (got %s)", llm.ModelName())
	}
	return llm, nil
}

// planThreads decides what threading does: in "explicit" mode, items never
// threaded are threaded under the current rules and earlier ones keep
// their threads; everything is rethreaded only when asked (redo, or claims
// rebuilt); "auto" rethreads everything when the rules change.
func (r *Runner) planThreads(o KnowledgeOptions, llm provider.LLM, linkStamp string, claimsRebuilt bool) (threadPlan, error) {
	version := knowledge.ThreadsVersion
	if r.Cfg.Knowledge.Threads.AuditFixes {
		version = knowledge.ThreadsVersionFixes
		if r.Cfg.Knowledge.Threads.Merged {
			version = knowledge.ThreadsVersionMerged
		}
	}
	p := threadPlan{stamp: hashOf("threads", version, llm.ModelName(), linkStamp)}
	p.old, _ = r.Store.GlobalStamp("threads")
	auto := r.Cfg.Knowledge.Updates == "auto"
	p.rebuild = claimsRebuilt || o.redo("threads") || o.redo("links") || (auto && p.old != "" && p.old != p.stamp)
	done, err := r.Store.ThreadedItems()
	if err != nil {
		return p, err
	}
	items, err := r.Store.AllItems()
	if err != nil {
		return p, err
	}
	for _, it := range items {
		d := done[it.ID]
		switch {
		case p.rebuild, auto && d != p.stamp, d == "":
			p.todo = append(p.todo, knowledge.ThreadItem{ID: it.ID, Start: it.OccurredAt})
		case d != p.stamp:
			p.older++
		}
	}
	return p, nil
}

// planLinksAndThreads fills in what linking and threading would do, for a
// plan; requeued are items the run would extract (they are threaded again).
func (r *Runner) planLinksAndThreads(o KnowledgeOptions, requeued map[string]bool, ks *KnowledgeStats) error {
	linkStamp, rebuild, older := r.linkDecision(o)
	ks.Planned.RebuildLinks, ks.Outdated.Links = rebuild, older
	if r.Cfg.Knowledge.Grouping != "threads" {
		return nil
	}
	llm, err := r.threadLLM(o)
	if err != nil {
		return err
	}
	p, err := r.planThreads(o, llm, linkStamp, rebuild)
	if err != nil {
		return err
	}
	ks.Planned.RebuildThreads, ks.Outdated.Threads = p.rebuild, p.older
	n := len(p.todo)
	in := map[string]bool{}
	for _, t := range p.todo {
		in[t.ID] = true
	}
	for id := range requeued {
		if !in[id] {
			n++
		}
	}
	ks.Planned.Thread = n
	return nil
}

// threadShowEvery: in a long threading run, the views are brought up to
// date after this many items.
const threadShowEvery = 10

func (r *Runner) thread(ctx context.Context, o KnowledgeOptions, linkStamp string, claimsRebuilt bool, ks *KnowledgeStats) error {
	kc := r.Cfg.Knowledge
	llm, err := r.threadLLM(o)
	if err != nil {
		return err
	}
	p, err := r.planThreads(o, llm, linkStamp, claimsRebuilt)
	if err != nil {
		return err
	}
	stamp := p.stamp
	// Recorded before threading, so an interrupted run resumes; threads are
	// cleared only when everything is to be rethreaded.
	if p.rebuild {
		if err := r.Store.ClearThreads(); err != nil {
			return err
		}
		r.logf("threads: rethreading every item under the current rules")
	}
	if p.rebuild || p.old == "" {
		if err := r.Store.SetGlobalStamp("threads", stamp); err != nil {
			return err
		}
	}
	ks.Outdated.Threads = p.older
	if p.older > 0 {
		r.logf("threads: %d items were threaded under earlier rules and keep their threads; new items use the current ones (musubi knowledge --redo threads)", p.older)
	}
	todo := p.todo
	knowledge.ThreadOrder(todo)
	// A few items threaded already but newer than new ones: thread them
	// again after, so everything appends in time order.
	if !p.rebuild && len(todo) > 0 && kc.Threads.Rewind > 0 {
		newer, err := r.Store.ThreadedSince(todo[0].Start)
		if err != nil {
			return err
		}
		inTodo := map[string]bool{}
		for _, it := range todo {
			inTodo[it.ID] = true
		}
		var redo []store.ItemTime
		for _, x := range newer {
			if !inTodo[x.ID] {
				redo = append(redo, x)
			}
		}
		older := 0
		if len(redo) > 0 {
			for _, it := range todo {
				if it.Start.Before(redo[len(redo)-1].At) {
					older++
				}
			}
		}
		if len(redo) > 0 && len(redo) <= kc.Threads.Rewind && older > len(redo) {
			for _, x := range redo {
				if _, err := knowledge.Unthread(r.Store, x.ID); err != nil {
					return err
				}
				if err := r.Store.UnmarkThreaded(x.ID); err != nil {
					return err
				}
				todo = append(todo, knowledge.ThreadItem{ID: x.ID, Start: x.At})
			}
			knowledge.ThreadOrder(todo)
			r.logf("threads: %d new items are older than %d threaded already; those are threaded again after them, so all append in time order", older, len(redo))
		}
	}
	// Threading follows time: an item is matched against threads as they
	// stood when it happened. Items older than ones already threaded (a
	// backfill) are inserted (threads.insert_older), or else threaded
	// after them all the same, and that is said.
	var latestDone time.Time
	if !p.rebuild && len(todo) > 0 && kc.Threads.InsertOlder {
		latestDone, _ = r.Store.LatestThreaded()
	}
	if !p.rebuild && len(todo) > 0 && !kc.Threads.InsertOlder {
		if latest, err := r.Store.LatestThreaded(); err == nil && todo[0].Start.Before(latest) {
			older := 0
			for _, it := range todo {
				if it.Start.Before(latest) {
					older++
				}
			}
			ks.Outdated.Backfilled = older
			r.logf("threads: %d new items are older than ones already threaded (latest %s); they are threaded after them. To thread everything in time order: musubi knowledge --redo threads", older, latest.Format("2006-01-02"))
		}
	}
	var vecs map[int64][]float32
	if o.Embedder != nil {
		if vecs, err = r.Store.StatementVecs(o.Embedder.Model()); err != nil {
			return err
		}
	}
	th := &knowledge.Threader{Store: r.Store, LLM: llm, Vecs: vecs, Version: stamp, Chunk: kc.Threads.Chunk, Parallel: kc.Threads.Parallel,
		Candidates: kc.Threads.Candidates, StateCap: kc.Threads.StateCap, WarmFirst: kc.WarmCache, AuditFixes: kc.Threads.AuditFixes, ConsolidateWindow: kc.Threads.ConsolidateWindow, Merged: kc.Threads.Merged, ConsolidateEvery: kc.Threads.ConsolidateEvery, Overlap: kc.Threads.Overlap, Logf: r.logf}
	// Corrections whose mending didn't finish (the model was unreachable
	// when they were made) are done first, so new items meet the graph as
	// corrected.
	if pending, err := r.Store.PendingCorrections(); err != nil {
		return err
	} else if len(pending) > 0 {
		var embed func(string) ([]float32, string, error)
		if o.Embedder != nil {
			embed = func(s string) ([]float32, string, error) {
				v, err := o.Embedder.Embed(ctx, []string{s}, false)
				if err != nil || len(v) == 0 {
					return nil, "", err
				}
				return v[0], o.Embedder.Model(), nil
			}
		}
		for _, pc := range pending {
			if _, err := th.Rerelate(ctx, pc.Statement, pc.Text, pc.By, embed); err != nil {
				return fmt.Errorf("correction of statement %d: %w", pc.Statement, err)
			}
		}
		r.logf("threads: %d corrections made earlier had their threads mended", len(pending))
	}
	// With overlap, an item's consolidation runs while the next item is
	// threaded (one at a time); waitTail collects it.
	type tailResult struct {
		st  knowledge.ThreadStats
		err error
	}
	var tailCh chan tailResult
	waitTail := func() error {
		if tailCh == nil {
			return nil
		}
		res := <-tailCh
		tailCh = nil
		ks.Threads.Relations += res.st.Relations
		ks.Threads.Calls += res.st.Calls
		return res.err
	}
	defer waitTail()
	for i, it := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		t0 := time.Now()
		// Not marked threaded (under these rules) but placed: a run stopped
		// mid-item, or an item kept from earlier rules being redone.
		if placed, err := r.Store.ThreadsOfItem(it.ID); err != nil {
			return err
		} else if len(placed) > 0 {
			if err := waitTail(); err != nil {
				return err
			}
		}
		if n, err := knowledge.Unthread(r.Store, it.ID); err != nil {
			return fmt.Errorf("threads %s: unthread: %w", it.ID, err)
		} else if n > 0 {
			r.logf("threads %s: was already in %d threads (a stopped run, or earlier rules); taken out first", it.ID, n)
		}
		inserting := !latestDone.IsZero() && it.Start.Before(latestDone)
		var st knowledge.ThreadStats
		var err error
		switch {
		case inserting:
			if err := waitTail(); err != nil {
				return err
			}
			st, err = th.Insert(ctx, it)
		case kc.Threads.Overlap:
			var tail func(context.Context) (knowledge.ThreadStats, error)
			st, tail, err = th.ThreadFront(ctx, it)
			if err == nil {
				w := time.Now()
				err = waitTail() // the previous item's review, overlapped with this one
				if d := time.Since(w); d > time.Second {
					r.logf("threads %s: waited %s for the previous item's review", it.ID, d.Round(time.Second))
				}
			}
			if err == nil && tail != nil {
				ch := make(chan tailResult, 1)
				go func() {
					s, e := tail(ctx)
					ch <- tailResult{s, e}
				}()
				tailCh = ch
			}
		default:
			st, err = th.Thread(ctx, it)
		}
		if err != nil {
			return fmt.Errorf("threads %s: %w", it.ID, err)
		}
		if st.Statements > 0 {
			r.logf("threads %s: %d statements, %d new threads, %d touched, %d relations, %d calls in %s%s",
				it.ID, st.Statements, st.NewThreads, st.Touched, st.Relations, st.Calls, time.Since(t0).Round(time.Second),
				map[bool]string{true: fmt.Sprintf("; %d left in no thread", st.Unplaced)}[st.Unplaced > 0])
		}
		ks.Threads.Items += st.Items
		ks.Threads.Statements += st.Statements
		ks.Threads.NewThreads += st.NewThreads
		ks.Threads.Touched += st.Touched
		ks.Threads.Relations += st.Relations
		ks.Threads.Calls += st.Calls
		if err := r.Store.MarkThreaded(it.ID, stamp); err != nil {
			return err
		}
		// A long run shows its threads as it goes: the topic views read a
		// claim's thread and the threads' centroids, set here every few
		// items as well as at the end.
		if n := len(todo); n > threadShowEvery && (i+1)%threadShowEvery == 0 && i+1 < n {
			if err := r.Store.PlaceClaimsInThreads(); err != nil {
				return err
			}
			if _, err := r.LinkThreads(); err != nil {
				return err
			}
		}
	}
	if err := waitTail(); err != nil {
		return err
	}
	// The owner's merges and splits, again wherever threading put their
	// statements (a no-op where they still hold).
	if n, err := knowledge.ApplyThreadEdits(r.Store); err != nil {
		return err
	} else if n > 0 {
		r.logf("threads: %d thread edits applied again", n)
	}
	if err := r.Store.PlaceClaimsInThreads(); err != nil {
		return err
	}
	if _, err := r.LinkThreads(); err != nil {
		return err
	}
	all, err := r.Store.Threads()
	ks.Topics = len(all)
	return err
}

// labelStatements runs the sensitivity labeller (knowledge.label) over
// statements this version hasn't checked, a few at a time.
func (r *Runner) labelStatements(ctx context.Context, o KnowledgeOptions, ks *KnowledgeStats) error {
	kc := r.Cfg.Knowledge
	d := o.Decider
	if kc.Label.Model.Provider != "" {
		llm, err := provider.NewLLM(kc.Label.Model)
		if err != nil {
			return err
		}
		if !llm.Local() && !kc.AllowHosted {
			return fmt.Errorf("the statement labeller reads meeting text, so it needs a local model, or knowledge.allow_hosted (got %s)", llm.ModelName())
		}
		d, _ = llm.(provider.Decider)
	}
	version, minP := knowledge.LabelVersion, kc.Label.MinP
	var cl provider.Classifier
	if c := kc.Label.Classifier; c.Provider != "" {
		if c.Provider != "jev" {
			return fmt.Errorf("knowledge.label.classifier: unknown provider %q", c.Provider)
		}
		if !kc.AllowHosted {
			return fmt.Errorf("the statement labeller reads meeting text, so a hosted classifier needs knowledge.allow_hosted")
		}
		cl = provider.NewJev(c.URL, c.Model, c.APIKey, c.PriceIn)
		version, minP = knowledge.LabelVersionTyped, c.MinP
		if minP == 0 {
			minP = 0.5
		}
	}
	if d == nil && cl == nil {
		return nil
	}
	// A classifier labels what no labeller has checked: statements labelled
	// earlier keep their labels (labels only add, so labelling them again
	// would give the union of two labellers) unless asked
	// (--redo label, which relabels everything under this version).
	var todo []store.Statement
	var err error
	if cl != nil && !o.redo("label") {
		todo, err = r.Store.StatementsUnlabelled()
	} else {
		todo, err = r.Store.StatementsToLabel(version)
	}
	if err != nil || len(todo) == 0 {
		return err
	}
	titles := map[string]string{}
	if cl != nil {
		items, err := r.Store.AllItems()
		if err != nil {
			return err
		}
		for _, it := range items {
			titles[it.ID] = it.Title
		}
	}
	t0 := time.Now()
	type result struct {
		id  int64
		l   sensitivity.Label
		err error
	}
	jobs := make(chan store.Statement)
	results := make(chan result)
	var wg sync.WaitGroup
	workers := max(o.Workers, 1)
	if cl != nil {
		workers = max(workers, 8) // a hosted classifier answers in about 0.1 s
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for st := range jobs {
				var c sensitivity.Categories
				var p float64
				var err error
				if cl != nil {
					c, p, err = knowledge.StatementSensitivityTyped(ctx, cl, st.Text, titles[st.ItemID])
				} else {
					c, p, err = knowledge.StatementSensitivity(ctx, d, st.Text)
				}
				var l sensitivity.Label
				if err == nil && c != 0 && p >= minP {
					l = sensitivity.Label{Level: sensitivity.LevelFor(c), Categories: c}
				}
				results <- result{st.ID, l, err}
			}
		}()
	}
	go func() {
		for _, st := range todo {
			if ctx.Err() != nil {
				break
			}
			jobs <- st
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	failed := 0
	for res := range results {
		if res.err != nil {
			// Left unchecked, so the next run tries again.
			failed++
			continue
		}
		if err := r.Store.LabelStatement(res.id, res.l, version); err != nil {
			return err
		}
		ks.Checked++
		if res.l.Categories != 0 {
			ks.Labelled++
		}
		if ks.Checked%500 == 0 {
			r.logf("label: %d of %d statements checked, %d sensitive", ks.Checked, len(todo), ks.Labelled)
		}
	}
	r.logf("label: %d statements checked, %d labelled sensitive, %d failed, in %s", ks.Checked, ks.Labelled, failed, time.Since(t0).Round(time.Second))
	return ctx.Err()
}

func (r *Runner) statusLLM(o KnowledgeOptions) (provider.LLM, error) {
	m := r.Cfg.Knowledge.Status.Model
	if o.StatusLLM != nil {
		return o.StatusLLM, nil
	}
	if m.Provider == "" {
		return r.threadLLM(o)
	}
	llm, err := provider.NewLLM(m)
	if err != nil {
		return nil, err
	}
	if !llm.Local() && !r.Cfg.Knowledge.AllowHosted {
		return nil, fmt.Errorf("thread statuses read meeting text, so they need a local model, or knowledge.allow_hosted (got %s)", llm.ModelName())
	}
	return llm, nil
}

// writeStatuses writes the status of every thread whose items changed
// since its status was written (or whose status is from an earlier
// version): a "full" status from every item, and an "open" one from the
// items a shared screen may show, when those differ.
func (r *Runner) writeStatuses(ctx context.Context, o KnowledgeOptions, ks *KnowledgeStats) error {
	sc := r.Cfg.Knowledge.Status
	if !sc.Enabled {
		return nil
	}
	threads, err := r.Store.Threads()
	if err != nil || len(threads) == 0 {
		return err
	}
	llm, err := r.statusLLM(o)
	if err != nil {
		return err
	}
	stamps, err := r.Store.ThreadStatusStamps()
	if err != nil {
		return err
	}
	// Written from the text as the owner's spelling rules read it: the
	// rules are in the input, so a rule made or undone rewrites the
	// statuses it touches.
	fix, err := r.Store.Corrector()
	if err != nil {
		return err
	}
	type job struct {
		th      store.ThreadRow
		variant string
		in      knowledge.StatusInput
	}
	shared := sensitivity.SharedScreen()
	open := func(s store.ThreadStatement) bool {
		return sensitivity.Visible(sensitivity.Label{Level: s.Level, Categories: s.Categories}, nil, shared)
	}
	var jobs []job
	for _, th := range threads {
		sts, err := r.Store.ThreadStatements(th.ID)
		if err != nil {
			return err
		}
		for i := range sts {
			sts[i].Text = fix.Text(sts[i].ID, sts[i].Text)
		}
		have := stamps[th.ID]
		full := knowledge.StatusInputFor(th.Name, th.Scope, sts, nil)
		if len(full.Items) == 0 {
			continue
		}
		if have["full"] != knowledge.StatusVersion+"/"+full.Hash {
			jobs = append(jobs, job{th, "full", full})
		} else if err := r.Store.RelabelThreadStatus(th.ID, "full", full.Label); err != nil {
			return err
		}
		op := knowledge.StatusInputFor(th.Name, th.Scope, sts, open)
		switch {
		case len(op.Items) == 0 || op.Hash == full.Hash:
			if have["open"] != "" {
				r.Store.SetThreadStatus(store.ThreadStatusRow{Topic: th.ID, Variant: "open"})
			}
		case have["open"] != knowledge.StatusVersion+"/"+op.Hash:
			jobs = append(jobs, job{th, "open", op})
		default:
			if err := r.Store.RelabelThreadStatus(th.ID, "open", op.Label); err != nil {
				return err
			}
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	t0 := time.Now()
	r.logf("status: writing %d thread statuses", len(jobs))
	type result struct {
		j   job
		st  knowledge.Status
		err error
	}
	results := make(chan result)
	sem := make(chan struct{}, max(sc.Parallel, 1))
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				results <- result{j: j, err: ctx.Err()}
				return
			}
			st, err := knowledge.WriteStatus(ctx, llm, j.th.Name, j.th.Scope, j.in)
			results <- result{j, st, err}
		}()
	}
	go func() { wg.Wait(); close(results) }()
	for res := range results {
		if res.err != nil {
			ks.StatusFailed++
			r.logf("status T%d (%s): %v", res.j.th.ID, res.j.variant, res.err)
			continue
		}
		body, _ := json.Marshal(res.st)
		if err := r.Store.SetThreadStatus(store.ThreadStatusRow{Topic: res.j.th.ID, Variant: res.j.variant, Version: knowledge.StatusVersion,
			InputHash: res.j.in.Hash, Body: string(body), Level: res.j.in.Label.Level, Categories: res.j.in.Label.Categories, AsOf: res.j.in.AsOf}); err != nil {
			return err
		}
		ks.Statuses++
		if ks.Statuses%50 == 0 {
			r.logf("status: %d of %d written", ks.Statuses, len(jobs))
		}
	}
	r.logf("status: %d written, %d failed, in %s", ks.Statuses, ks.StatusFailed, time.Since(t0).Round(time.Second))
	return ctx.Err()
}
