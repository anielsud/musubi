package pipeline

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
)

func (r *Runner) digestLLM(o KnowledgeOptions) (provider.LLM, error) {
	m := r.Cfg.Knowledge.Digest.Model
	llm := o.DigestLLM
	if llm == nil {
		if m.Provider == "" {
			return r.threadLLM(o)
		}
		var err error
		if llm, err = provider.NewLLM(m); err != nil {
			return nil, err
		}
	}
	if !llm.Local() && !r.Cfg.Knowledge.AllowHosted {
		return nil, fmt.Errorf("meeting digests read meeting text, so they need a local model, or knowledge.allow_hosted (got %s)", llm.ModelName())
	}
	return llm, nil
}

type digestJob struct {
	it          *item.Item
	title       string
	known       string
	hash, input string
}

// digestMeetings reads each meeting whole into a digest (a sectioned
// summary and its to-dos) when its passages, labels or facts changed, and
// matches each digest to-do with the statement that says the same. A new
// digest version is used for new meetings; earlier ones are kept and
// reported, and redone with --redo digest (as for extraction). When an
// item is re-extracted its statements are new, so only the matching is
// redone, with no model call.
func (r *Runner) digestMeetings(ctx context.Context, o KnowledgeOptions, items []item.Item, wanted func(string) bool,
	links map[string]string, byID map[string]*item.Item, invitees func(*item.Item) []knowledge.Invitee, ks *KnowledgeStats) error {
	kc := r.Cfg.Knowledge
	llm, err := r.digestLLM(o)
	if err != nil {
		return err
	}
	var jobs, older []digestJob
	var have []string
	// A digest stored with a stand-in to-do (knowledge.StubTodo) is made
	// again, as if it were missing.
	stored, err := r.Store.DigestTodoTexts()
	if err != nil {
		return err
	}
	stub := map[string]bool{}
	for id, texts := range stored {
		for _, t := range texts {
			if knowledge.StubTodo(t) {
				stub[id] = true
			}
		}
	}
	for i := range items {
		it := &items[i]
		if it.Kind != item.KindMeeting || !wanted(it.ID) || o.hold[it.ID] {
			continue
		}
		chunkStamp, err := r.Store.StageStamp("chunk", it.ID)
		if err != nil || chunkStamp == "" {
			continue
		}
		clsStamp, _ := r.Store.StageStamp("classify", it.ID)
		known := knowledge.KnownFacts(r.Cfg.Owner.Name, r.Cfg.Owner.Email, r.Cfg.Owner.InternalDomains, invitees(it))
		lf, err := r.labelFingerprint(it.ID)
		if err != nil {
			continue
		}
		// What the digest reads of the classification is the labels, so a
		// re-classification that leaves them alone doesn't redo it.
		h := hashOf("digest", knowledge.DigestVersion, chunkStamp, lf, llm.ModelName(), r.Cfg.Owner.Name, known)
		input := hashOf("input", chunkStamp, lf, known)
		cur, _ := r.Store.StageStamp("digest", it.ID)
		if cur != "" && cur != knowledge.DigestVersion+"/"+h {
			// Stamped by the earlier scheme, which used the classify stamp:
			// if nothing else changed, just re-stamp.
			old := hashOf("digest", knowledge.DigestVersion, chunkStamp, clsStamp, llm.ModelName(), r.Cfg.Owner.Name, known)
			if cur == knowledge.DigestVersion+"/"+old {
				if err := r.Store.RestampDigest(it.ID, knowledge.DigestVersion, h, input); err == nil {
					cur = knowledge.DigestVersion + "/" + h
				}
			}
		}
		if cur != "" && cur != knowledge.DigestVersion+"/"+h {
			// Read from the passages the item had before a speaker relabel
			// (store.RelabelledStage), which rewrote the digest with them.
			for _, was := range r.Store.RelabelledChunkStamps(it.ID) {
				if cur != knowledge.DigestVersion+"/"+hashOf("digest", knowledge.DigestVersion, was, lf, llm.ModelName(), r.Cfg.Owner.Name, known) {
					continue
				}
				if o.Plan {
					cur = knowledge.DigestVersion + "/" + h
				} else if err := r.Store.RestampDigest(it.ID, knowledge.DigestVersion, h, input); err == nil {
					cur = knowledge.DigestVersion + "/" + h
					r.logf("digest %s: kept (relabelled speakers)", it.ID)
				}
				break
			}
		}
		if cur != "" && stub[it.ID] {
			r.logf("digest %s: a to-do is a stand-in, reading the meeting again", it.ID)
			cur = ""
		}
		if cur != "" {
			have = append(have, it.ID)
		}
		if cur == knowledge.DigestVersion+"/"+h {
			continue
		}
		title := it.Title
		if e, ok := links[it.ID]; ok && byID[e] != nil {
			title = byID[e].Title
		}
		j := digestJob{it: it, title: title, known: known, hash: h, input: input}
		if cur != "" && kc.Updates != "auto" {
			// Digested before under another version, with the same input:
			// keep it until asked.
			in, _ := r.Store.StageStamp("digest-input", it.ID)
			if in != "in/"+input {
				// Read from the passages before a speaker relabel: the same
				// input, so kept as an earlier version's.
				for _, was := range r.Store.RelabelledChunkStamps(it.ID) {
					if in == "in/"+hashOf("input", was, lf, known) {
						if !o.Plan {
							r.Store.SetStageInput("digest-input", it.ID, input)
						}
						in = "in/" + input
						break
					}
				}
			}
			if in == "in/"+input {
				older = append(older, j)
				continue
			}
		}
		jobs = append(jobs, j)
	}
	if o.redo("digest") && len(older) > 0 {
		sort.Slice(older, func(a, b int) bool { return older[a].it.OccurredAt.After(older[b].it.OccurredAt) })
		n := len(older)
		if o.RedoNewest > 0 && o.RedoNewest < n {
			n = o.RedoNewest
		}
		jobs = append(jobs, older[:n]...)
		older = older[n:]
	}
	ks.Outdated.Digest = len(older)
	if !o.Plan && len(o.Items) == 0 {
		r.setWaiting("digest", len(older))
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].it.OccurredAt.After(jobs[b].it.OccurredAt) })
	if o.Limit > 0 && len(jobs) > o.Limit {
		jobs = jobs[:o.Limit]
	}
	if o.Plan {
		ks.Planned.Digest = len(jobs)
		return nil
	}
	if len(jobs) > 0 {
		r.logf("digest: %d meetings to read with %s", len(jobs), llm.ModelName())
	}
	t0 := time.Now()
	par := max(kc.Digest.Parallel, 1)
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.digestOne(ctx, llm, j); err != nil {
				r.logf("digest %s: %v (tried again next run)", j.it.ID, err)
				mu.Lock()
				ks.DigestFailed++
				mu.Unlock()
				return
			}
			mu.Lock()
			ks.Digested++
			if ks.Digested%25 == 0 {
				r.logf("digest: %d of %d meetings read", ks.Digested, len(jobs))
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(jobs) > 0 {
		r.logf("digest: %d meetings read, %d failed, in %s", ks.Digested, ks.DigestFailed, time.Since(t0).Round(time.Second))
	}
	// Match to-dos with statements for every digested meeting whose
	// statements changed since (no model call).
	done := map[string]bool{}
	for _, j := range jobs {
		done[j.it.ID] = true
	}
	for _, id := range have {
		if done[id] {
			continue
		}
		if err := r.relinkDigest(id); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) digestOne(ctx context.Context, llm provider.LLM, j digestJob) error {
	chunks, err := r.Store.Chunks(j.it.ID)
	if err != nil {
		return err
	}
	labels, err := r.Store.ItemLabels(j.it.ID)
	if err != nil {
		return err
	}
	m := knowledge.NewMeeting(j.title, j.it.OccurredAt, j.known, r.Cfg.Owner.Name, chunks)
	dg := &knowledge.Digester{LLM: llm, Owner: r.Cfg.Owner.Name, Logf: r.logf, Parallel: !llm.Local()}
	d, err := dg.Digest(ctx, m, labels, j.it.OccurredAt)
	if err != nil {
		return err
	}
	if err := r.Store.ReplaceDigest(j.it.ID, knowledge.DigestVersion, j.hash, j.input, d); err != nil {
		return err
	}
	return r.relinkDigest(j.it.ID)
}

// labelFingerprint is a hash of an item's labels (level and categories of
// the item and each chunk), not of how they were arrived at.
func (r *Runner) labelFingerprint(itemID string) (string, error) {
	ls, err := r.Store.ItemLabels(itemID)
	if err != nil {
		return "", err
	}
	seqs := make([]int, 0, len(ls))
	for q := range ls {
		seqs = append(seqs, q)
	}
	sort.Ints(seqs)
	parts := make([]string, 0, len(seqs)+1)
	parts = append(parts, "labels")
	for _, q := range seqs {
		l := ls[q]
		parts = append(parts, fmt.Sprintf("%d:%d:%d", q, int(l.Level), int64(l.Categories)))
	}
	return hashOf(parts...), nil
}

// relinkDigest matches an item's to-dos with its statements, when it
// hasn't been done for the statements it has now.
func (r *Runner) relinkDigest(itemID string) error {
	ex, err := r.Store.StageStamp("extract", itemID)
	if err != nil {
		return err
	}
	h := hashOf("digest-link", knowledge.DigestVersion, ex)
	if cur, _ := r.Store.StageStamp("digest-link", itemID); cur == "l/"+h {
		return nil
	}
	todos, err := r.Store.DigestTodos(itemID)
	if err != nil {
		return err
	}
	sts, err := r.Store.ItemStatements(itemID)
	if err != nil {
		return err
	}
	return r.Store.SetDigestLinks(itemID, h, knowledge.LinkDigestTodos(todos, sts))
}
