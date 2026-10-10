// Package eval scores a built knowledge base against hand-written cases:
// retrieval, leakage, classification, resolution and graph neighbours.
//
// The same harness runs on the committed synthetic corpus (CI gate, invented
// data) and on private cases over real data in testdata/private/.
//
// Leakage is checked twice: by cases that name what must not appear, and by
// an invariant re-check of every result against labels read directly, using
// sensitivity.Visible rather than the store's SQL filter. A disagreement
// between the two is a leak even if no case anticipated it.
package eval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Suite is a cases.toml file.
type Suite struct {
	// ProbeScopes are scopes every search case's query is also run under,
	// purely for the invariant check.
	ProbeScopes []string `toml:"probe_scopes"`
	// Now fixes the time belief is computed at, so decay is deterministic.
	Now   string `toml:"now"`
	Cases []Case `toml:"case"`
}

// Target picks out content: an item (id or id prefix) and/or a phrase its
// chunk must contain.
type Target struct {
	Item     string `toml:"item"`
	Contains string `toml:"contains"`
}

func (t Target) String() string {
	s := t.Item
	if t.Contains != "" {
		s += fmt.Sprintf(" ~%q", t.Contains)
	}
	return s
}

func (t Target) matches(itemID, text string) bool {
	if t.Item != "" && !strings.HasPrefix(itemID, t.Item) {
		return false
	}
	return t.Contains == "" || strings.Contains(strings.ToLower(text), strings.ToLower(t.Contains))
}

type Case struct {
	ID   string `toml:"id"`
	Kind string `toml:"kind"` // search, leak, classify, resolve, graph
	Note string `toml:"note"`

	// search and leak
	Query  string   `toml:"query"`
	Scope  string   `toml:"scope"`
	K      int      `toml:"k"`
	Expect []Target `toml:"expect"` // any one in the top K counts as found
	Forbid []Target `toml:"forbid"` // none may appear at any depth searched

	// classify and resolve
	Target     Target   `toml:"target"`
	Level      string   `toml:"level"`
	Categories []string `toml:"categories"`
	Surface    string   `toml:"surface"`
	// Entity is the expected entity id, "ambiguous", or "none" (no mention).
	Entity string `toml:"entity"`

	// knowledge: statement, claim, topic
	Kinds       []string   `toml:"kinds"`        // statement: any of these kinds
	Holder      string     `toml:"holder"`       // statement: holder entity id or name part
	TextHas     []string   `toml:"text_has"`     // statement: words the text must contain
	ClaimHas    []string   `toml:"claim_has"`    // claim: words a visible statement contains
	ClaimKind   string     `toml:"claim_kind"`   // claim: and of this kind
	Absent      bool       `toml:"absent"`       // claim: must not be visible in scope
	Statuses    []string   `toml:"statuses"`     // claim: any of these statuses
	MinEvidence int        `toml:"min_evidence"` // claim: visible statements
	MaxEvidence int        `toml:"max_evidence"`
	Relation    string     `toml:"relation"`    // claim: a relation of this kind...
	RelatedHas  []string   `toml:"related_has"` // ...to a claim containing these words
	Together    [][]string `toml:"together"`    // topic: claims (by words) in one topic
	Apart       [][]string `toml:"apart"`       // topic: two claims (by words) in different topics...
	LinkKinds   []string   `toml:"link_kinds"`  // ...whose topics are related as one of these
	LabelLacks  []string   `toml:"label_lacks"` // topic: words no visible topic label may contain
	At          string     `toml:"at"`          // claim: belief time, overriding the suite's

	// graph
	Node            string   `toml:"node"`
	Visible         *bool    `toml:"visible"`
	ExpectNeighbors []string `toml:"expect_neighbors"`
	ForbidNeighbors []string `toml:"forbid_neighbors"`
	ForbidItems     []string `toml:"forbid_items"`
}

func Load(path string) (*Suite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Suite
	if err := toml.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Result is one case's outcome.
type Result struct {
	ID, Kind, Mode string
	Pass           bool
	Rank           int // search: rank of first expected hit (1-based), 0 if missing
	Detail         string
	// Under is a classify result that labelled less sensitive than expected,
	// the error that matters most.
	Under bool
}

type Report struct {
	Results    []Result
	Violations []string // invariant failures: results that should not have been visible
	Summary    map[string]float64
}

type Harness struct {
	Store *store.Store
	// Now is when belief is computed (from the suite, or the wall clock).
	Now time.Time
	// InternalDomains decide whether a reply scope's audience is internal.
	InternalDomains []string
	Embedder        provider.Embedder // nil: keyword only
	Modes           []store.SearchMode
}

// searchDepth is how deep forbidden targets are looked for; deeper than any
// K a case uses so a leak can't hide just below the cut.
const searchDepth = 50

func (h *Harness) Run(ctx context.Context, s *Suite) (*Report, error) {
	rep := &Report{Summary: map[string]float64{}}
	modes := h.Modes
	if len(modes) == 0 {
		modes = []store.SearchMode{store.ModeHybrid}
	}
	if h.Embedder == nil {
		modes = []store.SearchMode{store.ModeKeyword}
	}
	for _, c := range s.Cases {
		switch c.Kind {
		case "search", "leak":
			for _, m := range modes {
				r, err := h.search(ctx, c, m, rep)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", c.ID, err)
				}
				rep.Results = append(rep.Results, r)
			}
			for _, ps := range s.ProbeScopes {
				pc := c
				pc.Scope = ps
				if _, err := h.searchHits(ctx, pc, modes[0], rep); err != nil {
					return nil, fmt.Errorf("%s probe %s: %w", c.ID, ps, err)
				}
			}
		case "classify":
			r, err := h.classify(c)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.ID, err)
			}
			rep.Results = append(rep.Results, r)
		case "resolve":
			r, err := h.resolve(c)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.ID, err)
			}
			rep.Results = append(rep.Results, r)
		case "graph":
			r, err := h.graph(c, rep)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.ID, err)
			}
			rep.Results = append(rep.Results, r)
		case "statement", "claim", "topic":
			r, err := h.knowledgeCase(c, s)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.ID, err)
			}
			rep.Results = append(rep.Results, r)
		default:
			return nil, fmt.Errorf("%s: unknown kind %q", c.ID, c.Kind)
		}
	}
	rep.summarize()
	return rep, nil
}

func (h *Harness) searchHits(ctx context.Context, c Case, mode store.SearchMode, rep *Report) ([]store.Hit, error) {
	sc, err := sensitivity.ParseScope(c.Scope, h.InternalDomains)
	if err != nil {
		return nil, err
	}
	q := store.SearchQuery{Text: c.Query, K: searchDepth, Mode: mode}
	if h.Embedder != nil && mode != store.ModeKeyword {
		v, err := h.Embedder.Embed(ctx, []string{c.Query}, true)
		if err != nil {
			return nil, err
		}
		q.Vec, q.Model = v[0], h.Embedder.Model()
	}
	hits, err := h.Store.Search(sc, q)
	if err != nil {
		return nil, err
	}
	for _, hit := range hits {
		l, aud, err := h.Store.ChunkLabel(hit.ChunkID)
		if err != nil || !sensitivity.Visible(l, aud, sc) {
			rep.Violations = append(rep.Violations, fmt.Sprintf("%s [%s] returned %s chunk %d labelled %s/%s", c.ID, sc.Name, hit.ItemID, hit.Seq, l.Level, l.Categories))
		}
	}
	return hits, nil
}

func (h *Harness) search(ctx context.Context, c Case, mode store.SearchMode, rep *Report) (Result, error) {
	r := Result{ID: c.ID, Kind: c.Kind, Mode: string(mode), Pass: true}
	hits, err := h.searchHits(ctx, c, mode, rep)
	if err != nil {
		return r, err
	}
	k := c.K
	if k == 0 {
		k = 5
	}
	var notes []string
	if len(c.Expect) > 0 {
		for i, hit := range hits {
			if i >= k {
				break
			}
			if anyMatch(c.Expect, hit) {
				r.Rank = i + 1
				break
			}
		}
		if r.Rank == 0 {
			r.Pass = false
			top := []string{}
			for i, hit := range hits {
				if i >= 3 {
					break
				}
				top = append(top, fmt.Sprintf("%s#%d", hit.ItemID, hit.Seq))
			}
			notes = append(notes, fmt.Sprintf("expected not in top %d (top: %s)", k, strings.Join(top, ", ")))
		}
	}
	for _, hit := range hits {
		for _, f := range c.Forbid {
			if f.matches(hit.ItemID, hit.Text) {
				r.Pass = false
				notes = append(notes, fmt.Sprintf("LEAK %s at chunk %d", f, hit.Seq))
			}
		}
	}
	r.Detail = strings.Join(notes, "; ")
	return r, nil
}

func anyMatch(ts []Target, hit store.Hit) bool {
	for _, t := range ts {
		if t.matches(hit.ItemID, hit.Text) {
			return true
		}
	}
	return false
}

// findChunk returns the first chunk of the target item containing the phrase.
func (h *Harness) findChunk(t Target) (*store.Chunk, error) {
	id, err := h.Store.ResolveItemID(t.Item)
	if err != nil {
		return nil, err
	}
	chunks, err := h.Store.Chunks(id)
	if err != nil {
		return nil, err
	}
	for _, c := range chunks {
		if t.matches(c.ItemID, c.Text) {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("no chunk of %s contains %q", t.Item, t.Contains)
}

func (h *Harness) classify(c Case) (Result, error) {
	r := Result{ID: c.ID, Kind: c.Kind}
	ch, err := h.findChunk(c.Target)
	if err != nil {
		r.Detail = err.Error()
		return r, nil
	}
	got, _, err := h.Store.ChunkLabel(ch.ID)
	if err != nil {
		return r, err
	}
	want, err := sensitivity.ParseLevel(c.Level)
	if err != nil {
		return r, err
	}
	wantCats, err := sensitivity.ParseCategories(c.Categories)
	if err != nil {
		return r, err
	}
	r.Pass = got.Level == want && got.Categories == wantCats
	r.Under = got.Level < want || wantCats&^got.Categories != 0
	if !r.Pass {
		r.Detail = fmt.Sprintf("got %s/%s want %s/%s", got.Level, got.Categories, want, wantCats)
	}
	return r, nil
}

// resolve looks for the mention in every chunk matching the target, since
// the phrase can also appear in a speaker label, where mentions aren't taken.
func (h *Harness) resolve(c Case) (Result, error) {
	r := Result{ID: c.ID, Kind: c.Kind}
	id, err := h.Store.ResolveItemID(c.Target.Item)
	if err != nil {
		r.Detail = err.Error()
		return r, nil
	}
	chunks, err := h.Store.Chunks(id)
	if err != nil {
		return r, err
	}
	seqs := map[int]bool{}
	for _, ch := range chunks {
		if c.Target.matches(ch.ItemID, ch.Text) {
			seqs[ch.Seq] = true
		}
	}
	ms, err := h.Store.Mentions(id)
	if err != nil {
		return r, err
	}
	var found *store.Mention
	for i, m := range ms {
		if seqs[m.ChunkSeq] && strings.EqualFold(m.Surface, c.Surface) {
			found = &ms[i]
			break
		}
	}
	switch {
	case c.Entity == "none":
		r.Pass = found == nil
		if found != nil {
			r.Detail = fmt.Sprintf("%q matched %s", c.Surface, orAmbiguous(*found))
		}
	case found == nil:
		r.Detail = fmt.Sprintf("no mention %q found", c.Surface)
	case c.Entity == "ambiguous":
		r.Pass = found.EntityID == ""
		if !r.Pass {
			r.Detail = "resolved to " + found.EntityID
		}
	default:
		r.Pass = found.EntityID == c.Entity
		if !r.Pass {
			r.Detail = "got " + orAmbiguous(*found)
		}
	}
	return r, nil
}

func orAmbiguous(m store.Mention) string {
	if m.EntityID == "" {
		return "ambiguous(" + strings.Join(m.Candidates, ",") + ")"
	}
	return m.EntityID
}

func (h *Harness) graph(c Case, rep *Report) (Result, error) {
	r := Result{ID: c.ID, Kind: c.Kind, Pass: true}
	sc, err := sensitivity.ParseScope(c.Scope, h.InternalDomains)
	if err != nil {
		return r, err
	}
	g, err := h.Store.EntityGraph(sc, c.Node, nil, 0)
	if err != nil {
		return r, err
	}
	var notes []string
	if c.Visible != nil && *c.Visible != (g != nil) {
		r.Pass = false
		if g != nil {
			notes = append(notes, "LEAK entity visible, want hidden")
		} else {
			notes = append(notes, "entity hidden, want visible")
		}
	}
	if g == nil {
		if len(c.ExpectNeighbors) > 0 {
			r.Pass = false
			notes = append(notes, "entity not visible")
		}
		r.Detail = strings.Join(notes, "; ")
		return r, nil
	}
	for _, it := range g.Items {
		l, aud, err := h.Store.ItemLabel(it.ID)
		if err != nil {
			return r, err
		}
		// A chunk's label is never below its item's, so any item reached
		// through a visible chunk or visible participation is visible itself.
		if !sensitivity.Visible(l, aud, sc) {
			rep.Violations = append(rep.Violations, fmt.Sprintf("%s [%s] graph item %s labelled %s", c.ID, sc.Name, it.ID, l.Level))
		}
	}
	nb := map[string]bool{}
	for _, n := range g.Neighbors {
		nb[n.ID] = true
	}
	var missing []string
	for _, e := range c.ExpectNeighbors {
		if !nb[e] {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		r.Pass = false
		notes = append(notes, "missing neighbours "+strings.Join(missing, ","))
	}
	for _, e := range c.ForbidNeighbors {
		if nb[e] {
			r.Pass = false
			notes = append(notes, "LEAK neighbour "+e)
		}
	}
	for _, it := range g.Items {
		for _, f := range c.ForbidItems {
			if strings.HasPrefix(it.ID, f) {
				r.Pass = false
				notes = append(notes, "LEAK item "+it.ID)
			}
		}
	}
	r.Detail = strings.Join(notes, "; ")
	return r, nil
}

func (rep *Report) summarize() {
	type acc struct {
		n, pass, under int
		rr             float64
	}
	by := map[string]*acc{}
	for _, r := range rep.Results {
		key := r.Kind
		if r.Mode != "" {
			key += "/" + r.Mode
		}
		a := by[key]
		if a == nil {
			a = &acc{}
			by[key] = a
		}
		a.n++
		if r.Pass {
			a.pass++
		}
		if r.Under {
			a.under++
		}
		if r.Rank > 0 {
			a.rr += 1 / float64(r.Rank)
		}
	}
	for k, a := range by {
		rep.Summary[k+".pass"] = float64(a.pass) / float64(a.n)
		rep.Summary[k+".n"] = float64(a.n)
		if strings.HasPrefix(k, "search") {
			rep.Summary[k+".mrr"] = a.rr / float64(a.n)
		}
		if k == "classify" {
			rep.Summary[k+".under"] = float64(a.under)
		}
	}
	rep.Summary["violations"] = float64(len(rep.Violations))
}

// Leaks counts forbidden content that appeared, plus invariant violations.
// It must be zero.
func (rep *Report) Leaks() int {
	n := len(rep.Violations)
	for _, r := range rep.Results {
		if strings.Contains(r.Detail, "LEAK") {
			n++
		}
	}
	return n
}

// Print writes a human-readable report, failures first.
func (rep *Report) Print(w *os.File, baseline map[string]float64) {
	keys := make([]string, 0, len(rep.Summary))
	for k := range rep.Summary {
		if !strings.HasSuffix(k, ".n") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "metric                         value   n")
	for _, k := range keys {
		n := rep.Summary[strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(k, ".pass"), ".mrr"), ".under")+".n"]
		line := fmt.Sprintf("%-30s %6.3f  %3.0f", k, rep.Summary[k], n)
		if b, ok := baseline[k]; ok && b != rep.Summary[k] {
			line += fmt.Sprintf("   (was %.3f, %+.3f)", b, rep.Summary[k]-b)
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "\nleaks: %d\n", rep.Leaks())
	for _, v := range rep.Violations {
		fmt.Fprintln(w, "  VIOLATION", v)
	}
	var failed []Result
	for _, r := range rep.Results {
		if !r.Pass {
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(w, "\nfailed (%d):\n", len(failed))
		for _, r := range failed {
			mode := ""
			if r.Mode != "" {
				mode = "/" + r.Mode
			}
			fmt.Fprintf(w, "  %-6s %-8s %s\n", r.ID, r.Kind+mode, r.Detail)
		}
	}
}
