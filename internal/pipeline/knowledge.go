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
	// Decider and Gate skip passages unlikely to hold knowledge (see
	// config.Knowledge.Gate); Gate 0 extracts everything.
	Decider provider.Decider
	Gate    float64
	// LinkTrace receives the linker's decisions.
	LinkTrace func(format string, args ...any)
}

type KnowledgeStats struct {
	Items, Statements, Embedded int
	Chunks                      int
	Gated                       int // passages the gate skipped
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

	gated := o.Gate > 0 && o.Decider != nil
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
			h = hashOf(h, knowledge.GateVersion, fmt.Sprint(o.Gate))
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
				for _, c := range j.chunks {
					lines := knowledge.SplitLines(c.Text)
					if len(lines) == 0 {
						continue
					}
					ex, err := knowledge.Extract(ctx, o.LLM, r.Cfg.Owner.Name, j.title, lines, j.orgs)
					if err != nil {
						if ctx.Err() != nil {
							return nil, err
						}
						// One bad reply (e.g. cut off at the token limit) skips
						// that passage, not the run.
						r.logf("extract %s#%d skipped: %v", j.it.ID, c.Seq, err)
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

	// 3. Link, oldest first.
	if _, err := r.Store.DropEmptyClaims(); err != nil {
		return ks, err
	}
	vecs, err := r.Store.StatementVecs(o.Embedder.Model())
	if err != nil {
		return ks, err
	}
	lk := &knowledge.Linker{Store: r.Store, LLM: o.LLM, Vecs: vecs, Trace: o.LinkTrace, Entities: topicEntities(reg)}
	if err := lk.Load(); err != nil {
		return ks, err
	}
	unlinked, err := r.Store.UnlinkedStatements()
	if err != nil {
		return ks, err
	}
	t0 := time.Now()
	if ks.Link, err = lk.Link(ctx, unlinked); err != nil {
		return ks, err
	}
	ks.LinkTime = time.Since(t0)
	if len(unlinked) > 0 {
		r.logf("link: %d statements (%d new claims, %d joined, %d judged, %d relations) in %s",
			len(unlinked), ks.Link.New, ks.Link.Joined, ks.Link.Judged, ks.Link.Related, time.Since(t0).Round(time.Second))
	}

	// 4. Topics, when the claims changed.
	sig, err := r.Store.ClaimSignature()
	if err != nil {
		return ks, err
	}
	h := hashOf("topics", knowledge.TopicsVersion, sig, o.LLM.ModelName())
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
