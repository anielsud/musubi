package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/stages"
	"github.com/anielsud/musubi/internal/store"
)

type KnowledgeOptions struct {
	LLM      provider.LLM
	Embedder provider.Embedder
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
	// 1. Extract, for items whose chunks changed since they were extracted.
	type job struct {
		it     *item.Item
		hash   string
		chunks []store.Chunk
		title  string
		orgs   map[string]string
	}
	var jobs []job
	for i := range items {
		it := &items[i]
		if it.Kind == item.KindEvent || !wanted(it.ID) {
			continue
		}
		chunkStamp, err := r.Store.StageStamp("chunk", it.ID)
		if err != nil || chunkStamp == "" {
			continue
		}
		h := hashOf("extract", knowledge.ExtractVersion, chunkStamp, o.LLM.ModelName(), r.Cfg.Owner.Name)
		if gated {
			h = hashOf(h, knowledge.GateVersion, fmt.Sprint(kc.Gate))
		}
		if cur, _ := r.Store.StageStamp("extract", it.ID); cur == knowledge.ExtractVersion+"/"+h {
			continue
		}
		chunks, err := r.Store.Chunks(it.ID)
		if err != nil {
			return ks, err
		}
		title := it.Title
		if e, ok := links[it.ID]; ok && byID[e] != nil {
			title = byID[e].Title
		}
		jobs = append(jobs, job{it, h, chunks, title, speakerOrgs(it)})
	}
	if o.Limit > 0 && len(jobs) > o.Limit {
		ks.Pending = len(jobs) - o.Limit
		jobs = jobs[:o.Limit]
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
					return r.Store.ReplaceStatements(j.it.ID, knowledge.ExtractVersion, j.hash, sts)
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

	// 2a. Search text for statements extracted before it existed.
	old, err := r.Store.StatementsWithoutSearchText()
	if err != nil {
		return ks, err
	}
	for _, s := range old {
		if err := r.Store.SetSearchText(s.ID, knowledge.SearchForm(s.Text)); err != nil {
			return ks, err
		}
	}

	// 2b. Score statements for worth, once per scorer version.
	if kc.MinWorth > 0 && o.Decider != nil {
		todo, err := r.Store.StatementsToScore(knowledge.WorthVersion)
		if err != nil {
			return ks, err
		}
		for _, s := range todo {
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

	// 3. Link, oldest first.
	if _, err := r.Store.DropEmptyClaims(); err != nil {
		return ks, err
	}
	vecs, err := r.Store.StatementVecs(o.Embedder.Model())
	if err != nil {
		return ks, err
	}
	lk := &knowledge.Linker{Store: r.Store, LLM: o.LLM, Vecs: vecs, Trace: o.LinkTrace, Entities: topicEntities(reg)}
	if kc.Judge == "decide" && o.Decider != nil {
		lk.Decider, lk.ConsiderAt, lk.MaxJudged = o.Decider, 0.65, 6
	}
	// Claims are cheap to rebuild from stored statements; rebuild them when
	// the rules that made them change, so old and new rules never mix.
	linkStamp := hashOf("link", knowledge.LinkVersion, o.LLM.ModelName(), fmt.Sprint(kc.MinWorth), kc.Judge, knowledge.WorthVersion)
	if old, _ := r.Store.GlobalStamp("link"); old != linkStamp {
		if err := r.Store.ClearClaims(); err != nil {
			return ks, err
		}
		r.logf("link: rules changed, rebuilding claims")
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
			if s.Worth >= 0 && s.Worth < kc.MinWorth {
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
	if err := r.Store.SetGlobalStamp("link", linkStamp); err != nil {
		return ks, err
	}
	if len(unlinked) > 0 {
		r.logf("link: %d statements (%d new claims, %d joined, %d judged, %d relations) in %s",
			len(unlinked), ks.Link.New, ks.Link.Joined, ks.Link.Judged, ks.Link.Related, time.Since(t0).Round(time.Second))
	}

	// 4. Topics, when the claims changed.
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
