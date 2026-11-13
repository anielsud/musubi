package pipeline

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/stages"
)

// Upgrades that would cost model calls on existing data wait for the owner
// (knowledge.hold_upgrades; docs/data-safety.md, item 7). The rule for
// "changed by an upgrade": every stage stamp mixes code versions with a hash
// of what was read, so for each held step we keep, or recompute, a key of
// what was read alone. When the stamp is stale but that key is the same,
// only the code (or the model) changed: the step waits. When the key
// differs, the content changed and the step runs as before.
//
//   - Passages: the key is the item's raw record or its content, the raw
//     record of a transcript stitched into it, and its title
//     (store.SetChunkSource). Either the raw record or the content being
//     unchanged means nothing the owner has changed: a new translator
//     re-reads the same raw record; a new chunker re-cuts the same content.
//     Items cut before keys were recorded fall back to recomputing their
//     chunk hash under the version in their stamp.
//   - Statuses: the stamp's hash is recomputed under the version that wrote
//     it (knowledge.StatusHash).
//   - Names: the hash of the text a name was made from is kept beside its
//     input (nameInputKey).

// chunkVersion and chunkPassages are the chunker; variables so tests can
// stand in a new one.
var (
	chunkVersion  = stages.ChunkVersion
	chunkPassages = stages.Chunk
)

// holdUpgrades says whether work an upgrade alone would redo waits.
func (r *Runner) holdUpgrades() bool {
	kc := r.Cfg.Knowledge
	return kc.HoldUpgrades && kc.Updates != "auto"
}

// passageKey is what an item's passages are cut from, without versions.
type passageKey struct {
	hash    string // the chunk stamp's hash under the current chunker
	content string // it.ContentHash()
	title   string
	source  string // the chunk-source key
}

func newPassageKey(it *item.Item, title, stitchedRaw string) passageKey {
	ch := it.ContentHash()
	raw := ""
	if it.RawHash != "" {
		raw = hashOf("raw", it.RawHash, stitchedRaw, title)
	}
	return passageKey{
		hash:    hashOf("chunk", chunkVersion, ch, title),
		content: ch,
		title:   title,
		source:  raw + ":" + hashOf("content", ch, title),
	}
}

// sameSource: either the raw records or the content are unchanged.
func sameSource(was, now string) bool {
	wr, wc, _ := strings.Cut(was, ":")
	nr, nc, _ := strings.Cut(now, ":")
	return wc == nc || (wr != "" && wr == nr)
}

// passagesWait says whether an item's passages are out of date only
// because of an upgrade, and they were already read at a model's cost
// (extracted or summarised): they are kept, and stored is the hash of the
// chunk stamp they keep.
func (r *Runner) passagesWait(itemID string, k passageKey) (stored string, wait bool) {
	if !r.holdUpgrades() {
		return "", false
	}
	cur, _ := r.Store.StageStamp("chunk", itemID)
	ver, h, ok := strings.Cut(cur, "/")
	if !ok || cur == chunkVersion+"/"+k.hash {
		return "", false
	}
	ex, _ := r.Store.StageStamp("extract", itemID)
	dg, _ := r.Store.StageStamp("digest", itemID)
	if ex == "" && dg == "" {
		return "", false
	}
	if was, _ := r.Store.StageStamp("chunk-source", itemID); was != "" {
		return h, sameSource(strings.TrimPrefix(was, "src/"), k.source)
	}
	return h, ver != chunkVersion && hashOf("chunk", ver, k.content, k.title) == h
}

// stitchedRaw maps each recording to the raw record of the Teams
// transcript stitched into it (into: transcript → recording).
func stitchedRaw(into map[string]string, byID map[string]*item.Item) map[string]string {
	out := map[string]string{}
	for t, rec := range into {
		if it := byID[t]; it != nil {
			out[rec] = it.RawHash
		}
	}
	return out
}

// PassageWaits lists the items whose passages wait for the owner (see
// passagesWait), newest first.
func (r *Runner) PassageWaits() ([]string, error) {
	if !r.holdUpgrades() {
		return nil, nil
	}
	items, err := r.Store.AllItems()
	if err != nil {
		return nil, err
	}
	links, err := r.Store.LinksFrom("occurred_as")
	if err != nil {
		return nil, err
	}
	stitchedItems, into, err := r.stitches(items, false)
	if err != nil {
		return nil, err
	}
	byID := map[string]*item.Item{}
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	partner := stitchedRaw(into, byID)
	given, err := r.Store.GivenTitles()
	if err != nil {
		return nil, err
	}
	type wait struct {
		id string
		it *item.Item
	}
	var out []wait
	for i := range items {
		it := &items[i]
		if s, ok := stitchedItems[it.ID]; ok {
			it = &s
		}
		title := r.passageTitle(it, byID[links[it.ID]], given[it.ID].Title)
		if _, w := r.passagesWait(it.ID, newPassageKey(it, title, partner[it.ID])); w {
			out = append(out, wait{it.ID, it})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].it.OccurredAt.After(out[b].it.OccurredAt) })
	ids := make([]string, len(out))
	for i, w := range out {
		ids[i] = w.id
	}
	return ids, nil
}

// The counts of work waiting for the owner, as the last run that looked
// found them, for the work queue: "passages", "extract", "digest",
// "statuses", "names".
func (r *Runner) setWaiting(what string, n int) {
	key := "waiting/" + what
	if old, _ := r.Store.GlobalStamp(key); old == strconv.Itoa(n) || (old == "" && n == 0) {
		return
	}
	r.Store.SetGlobalStamp(key, strconv.Itoa(n))
}

func (r *Runner) waiting(what string) int {
	s, _ := r.Store.GlobalStamp("waiting/" + what)
	n, _ := strconv.Atoi(s)
	return n
}

// applyPassageUpgrades re-cuts held passages when a run redoes extraction
// (the newest RedoNewest of them when set), so the run extracts them again;
// otherwise it reports how many wait: counted now for a plan, else as the
// last update found them.
func (r *Runner) applyPassageUpgrades(ctx context.Context, o KnowledgeOptions, ks *KnowledgeStats) error {
	if !r.holdUpgrades() || o.DigestOnly {
		return nil
	}
	if !o.Plan && !o.redo("extract") {
		ks.Outdated.Passages = r.waiting("passages")
		return nil
	}
	ids, err := r.PassageWaits()
	if err != nil {
		return err
	}
	ks.Outdated.Passages = len(ids)
	if o.Plan || len(ids) == 0 {
		return nil
	}
	if o.RedoNewest > 0 && o.RedoNewest < len(ids) {
		ids = ids[:o.RedoNewest]
	}
	r.rechunkIDs = map[string]bool{}
	for _, id := range ids {
		r.rechunkIDs[id] = true
	}
	defer func() { r.rechunkIDs = nil }()
	r.logf("extract: cutting %d meetings' passages again under the current chunker, to extract them again", len(ids))
	if err := r.Process(ctx, &Stats{Ran: map[string]int{}}); err != nil {
		return err
	}
	ks.Outdated.Passages = r.waiting("passages")
	return nil
}

// planUpgrades counts, for a plan, the statuses and names that wait for
// the owner (the passages are counted by applyPassageUpgrades).
func (r *Runner) planUpgrades(o KnowledgeOptions, items []item.Item, links map[string]string, ks *KnowledgeStats) error {
	if !r.holdUpgrades() {
		return nil
	}
	kc := r.Cfg.Knowledge
	if kc.Grouping == "threads" && kc.Status.Enabled {
		threads, err := r.Store.Threads()
		if err != nil {
			return err
		}
		if _, ks.Outdated.Statuses, err = r.statusJobs(o, threads, true); err != nil {
			return err
		}
	}
	if kc.NameMeetings {
		// A naming model that can't be built (not allowed hosted, say)
		// leaves the count out rather than failing the plan.
		var named KnowledgeStats
		if err := r.nameMeetings(context.Background(), o, items, links, &named); err == nil {
			ks.Outdated.Names = named.Outdated.Names
		}
	}
	return nil
}
