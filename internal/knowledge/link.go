package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// LinkVersion changes with the linking rules or judge prompt.
const LinkVersion = "k15"

// Thresholds on cosine similarity between a statement and a claim's
// centroid (nomic-embed-text). Above sameAt with the same kind family, the
// statement joins without asking; between considerAt and sameAt the model
// judges. Tuned on the synthetic scenarios first; real data will move them.
// Measured on the synthetic corpus: unrelated statements sit around 0.59,
// one in ten pairs exceeds 0.70, and "ships Sept 22" vs "ships Sept 29"
// scores 0.99, so similarity alone never decides that two statements are the
// same: only near-identical text with identical numbers and dates joins
// without the judge.
const (
	sameAt     = 0.97
	considerAt = 0.72
	maxJudged  = 3
)

var relations = []string{"same", "contradicts", "refines", "supersedes", "answers", "unrelated"}

const judgePrompt = `You compare a new statement with earlier claims from one person's records (meetings, email, chat), to track how knowledge changes over time.

For each earlier claim, say how the NEW statement relates to it:
- same: asserts the same thing, in other words or with detail that doesn't change it
- contradicts: both cannot be true ("the cause is X" vs "it is not X"; "three years" vs "two years")
- refines: the same thing made more precise, or one detail corrected
- supersedes: a newer version of the same plan, decision, date or status that replaces it ("ships Sept 22" then "ships Sept 29")
- answers: gives the answer to it (only if the earlier claim is a question)
- unrelated: anything else

Being about the same project, customer or person is NOT enough for any relation: most earlier claims will be unrelated. Choose a relation only when it clearly holds for these two specific statements.

Give exactly one entry for EVERY earlier claim, in order, each judged on its own.
Reply as JSON: {"relations":[{"claim":1,"relation":"..."},{"claim":2,"relation":"..."}]}`

var judgeSchema = json.RawMessage(`{"type":"object","required":["relations"],"properties":{"relations":{"type":"array","items":{"type":"object","required":["claim","relation"],"properties":{"claim":{"type":"integer"},"relation":{"type":"string","enum":["same","contradicts","refines","supersedes","answers","unrelated"]}}}}}}`)

type liveClaim struct {
	store.ClaimRow
	sum []float32 // sum of member vectors
}

// Linker links statements to claims, in the order they were said.
type Linker struct {
	Store *store.Store
	LLM   provider.LLM // nil: near-identical matches only
	// Decider, when set, judges each candidate pair in one forward pass
	// instead of the LLM writing JSON verdicts for all of them.
	Decider provider.Decider
	// ConsiderAt and MaxJudged widen the candidates; 0 keeps the defaults.
	ConsiderAt float64
	MaxJudged  int
	Vecs       map[int64][]float32
	// Entities returns the organisations and projects a text names. Two
	// statements naming different ones (and none in common) are never
	// related: a requirement from one customer doesn't refine another's.
	Entities func(text string) []string
	// Trace receives one line per decision, for diagnostics and the eval.
	Trace func(format string, args ...any)

	claims []*liveClaim
	// df counts, per content word, the claims whose text has it, for
	// finding claims that share a rare word.
	df map[string]int
}

// rareFloor and maxRare: besides the most similar claims, up to maxRare
// claims that share a rare word with the statement (and are at least
// rareFloor similar) are judged too. In a meeting with many claims on one
// subject, the earlier claim a statement repeats ("throwaway R&D") can rank
// ninth by similarity, behind six claims that are merely about the same
// partner, and the cap would hide it.
const (
	rareFloor = 0.55
	maxRare   = 3
)

func contentWords(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
		if len(w) >= 4 && !stopWords[w] && !negations[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func (k *Linker) addDoc(text string) {
	if k.df == nil {
		k.df = map[string]int{}
	}
	for _, w := range contentWords(text) {
		k.df[w]++
	}
}

// rareShared reports whether a and b share a word that few claims use.
func (k *Linker) rareShared(a, b string) bool {
	limit := max(2, len(k.claims)/50)
	in := map[string]bool{}
	for _, w := range contentWords(b) {
		in[w] = true
	}
	for _, w := range contentWords(a) {
		if in[w] && k.df[w] <= limit {
			return true
		}
	}
	return false
}

type LinkStats struct {
	New, Joined, Judged, Related int
}

func (k *Linker) considerAt() float64 {
	if k.ConsiderAt > 0 {
		return k.ConsiderAt
	}
	return considerAt
}

func (k *Linker) maxJudged() int {
	if k.MaxJudged > 0 {
		return k.MaxJudged
	}
	return maxJudged
}

func (k *Linker) trace(f string, a ...any) {
	if k.Trace != nil {
		k.Trace(f, a...)
	}
}

// Load reads existing claims (with member vectors) into memory.
func (k *Linker) Load() error {
	rows, err := k.Store.Claims()
	if err != nil {
		return err
	}
	k.claims = nil
	for _, r := range rows {
		c := &liveClaim{ClaimRow: r}
		for _, m := range r.Members {
			if v := k.Vecs[m]; v != nil {
				c.sum = add(c.sum, v)
			}
		}
		if c.sum != nil {
			c.Centroid = normalized(c.sum)
		}
		k.claims = append(k.claims, c)
		k.addDoc(c.Text)
	}
	return nil
}

// Link places each statement (oldest first) in a claim.
func (k *Linker) Link(ctx context.Context, sts []store.Statement) (LinkStats, error) {
	var stats LinkStats
	for _, st := range sts {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		v := k.Vecs[st.ID]
		if v == nil {
			continue
		}
		type cand struct {
			c   *liveClaim
			sim float64
		}
		var cands []cand
		for _, c := range k.claims {
			if c.Centroid == nil {
				continue
			}
			if s := cos(v, c.Centroid); s >= k.considerAt() {
				cands = append(cands, cand{c, s})
			}
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
		var rest []cand
		if n := k.maxJudged(); len(cands) > n {
			rest = cands[n:]
			cands = cands[:n]
		}
		if k.Decider != nil {
			// Rare-word candidates, from those the cap cut and those just
			// under the similarity threshold.
			for _, c := range k.claims {
				if c.Centroid == nil {
					continue
				}
				if s := cos(v, c.Centroid); s >= rareFloor && s < k.considerAt() {
					rest = append(rest, cand{c, s})
				}
			}
			sort.Slice(rest, func(i, j int) bool { return rest[i].sim > rest[j].sim })
			added := 0
			for _, c := range rest {
				if added == maxRare {
					break
				}
				if k.rareShared(st.Text, c.c.Text) {
					cands = append(cands, c)
					added++
				}
			}
		}
		// Near-identical, the same kind, and the same numbers and dates:
		// join directly.
		if len(cands) > 0 && st.Grounded && cands[0].sim >= sameAt && cands[0].c.Kind == st.Kind && sameFigures(st.Text, cands[0].c.Text) &&
			valuesAgree(st.Value, cands[0].c.Value) {
			if err := k.join(st, cands[0].c, cands[0].sim, "near-identical"); err != nil {
				return stats, err
			}
			stats.Joined++
			k.trace("%q joins claim %d (similarity %.2f)", clip(st.Text, 80), cands[0].c.ID, cands[0].sim)
			continue
		}
		rels := map[int]string{}
		if len(cands) > 0 && (k.LLM != nil || k.Decider != nil) {
			var cs []*liveClaim
			for _, c := range cands {
				cs = append(cs, c.c)
			}
			var err error
			rels, err = k.judge(ctx, st, cs)
			if err != nil {
				return stats, err
			}
			stats.Judged++
		}
		// Joins the most similar claim judged "same"; otherwise starts one.
		var home *liveClaim
		for i, c := range cands {
			if rels[i] == "same" {
				home = c.c
				if err := k.join(st, c.c, c.sim, "judged"); err != nil {
					return stats, err
				}
				stats.Joined++
				k.trace("%q is the same claim as %d (judged; similarity %.2f)", clip(st.Text, 80), c.c.ID, c.sim)
				break
			}
		}
		if home == nil {
			id, err := k.Store.NewClaim(st, v)
			if err != nil {
				return stats, err
			}
			home = &liveClaim{ClaimRow: store.ClaimRow{ID: id, Kind: st.Kind, Centroid: normalized(v), Members: []int64{st.ID}, Text: st.Text, Holder: st.Holder, SaidAt: st.SaidAt, Value: st.Value}, sum: append([]float32(nil), v...)}
			k.claims = append(k.claims, home)
			k.addDoc(st.Text)
			stats.New++
		}
		for i, c := range cands {
			rel := rels[i]
			if rel == "" || rel == "same" || rel == "unrelated" || c.c.ID == home.ID {
				continue
			}
			if k.differentSubjects(st.Text, c.c.Text) {
				k.trace("claim %d %s claim %d dropped: about different organisations or projects", home.ID, rel, c.c.ID)
				continue
			}
			if err := k.Store.RelateClaims(home.ID, c.c.ID, rel, st.ID); err != nil {
				return stats, err
			}
			stats.Related++
			k.trace("claim %d %s claim %d: %q → %q", home.ID, rel, c.c.ID, clip(st.Text, 60), clip(c.c.Text, 60))
		}
	}
	return stats, nil
}

func (k *Linker) differentSubjects(a, b string) bool {
	if k.Entities == nil {
		return false
	}
	ea, eb := k.Entities(a), k.Entities(b)
	if len(ea) == 0 || len(eb) == 0 {
		return false
	}
	for _, x := range ea {
		for _, y := range eb {
			if x == y {
				return false
			}
		}
	}
	return true
}

func (k *Linker) join(st store.Statement, c *liveClaim, sim float64, how string) error {
	v := k.Vecs[st.ID]
	c.sum = add(c.sum, v)
	c.Centroid = normalized(c.sum)
	c.Members = append(c.Members, st.ID)
	return k.Store.JoinClaim(st.ID, c.ID, sim, how, c.Centroid)
}

// notes spells out what the model is unreliable at seeing for itself: how
// two values relate ("Q4" against "mid November") and a plain negation
// ("the cause is not X" against "the cause is X") between texts that share
// their key words.
func notes(st store.Statement, c *liveClaim) string {
	var b strings.Builder
	switch Compare(st.Value, c.Value) {
	case "within":
		fmt.Fprintf(&b, "  [value note: the new %q falls within %q]", st.Value.Raw, c.Value.Raw)
	case "contains":
		fmt.Fprintf(&b, "  [value note: the new %q is broader than %q]", st.Value.Raw, c.Value.Raw)
	case "differs":
		fmt.Fprintf(&b, "  [value note: the new %q differs from %q]", st.Value.Raw, c.Value.Raw)
	case "same":
		fmt.Fprintf(&b, "  [value note: same %s]", c.Value.Type)
	}
	if negated(st.Text) != negated(c.Text) && sharedWords(st.Text, c.Text) >= 2 {
		b.WriteString("  [note: one of the two says NOT]")
	}
	return b.String()
}

// settle applies the rules the model's choice must pass, given the kinds
// and values involved; "" means no relation.
func settle(rel string, st store.Statement, c *liveClaim) string {
	// A statement that settles an open question answers it, whatever else
	// the model called it.
	if c.Kind == "question" && st.Kind != "question" && (rel == "contradicts" || rel == "supersedes" || rel == "refines") {
		rel = "answers"
	}
	// A "newer version" that states the same date or figure confirms the
	// plan rather than replacing it.
	if rel == "supersedes" && Compare(st.Value, c.Value) == "same" {
		rel = "same"
	}
	if !fits(rel, st.Kind, c.Kind) {
		return ""
	}
	return rel
}

// linkSystem is the one system prompt every link decision uses: the pair
// judge and the checks that confirm its verdicts. Sharing it keeps the
// model's cached prompt prefix valid from call to call; alternating two
// system prompts made each call re-read it (464 ms against 106 ms per call).
const linkSystem = `You help track how knowledge in one person's work records (meetings, email, chat) changes over time. Each request shows two statements and asks one question; reply with exactly one of the words the question allows.

How a NEW statement can relate to an EARLIER one:
- same: asserts the same thing, in other words or with detail that doesn't change it
- contradicts: both make claims about the SAME specific thing (the same plan, figure, date, decision, cause or fact) and they cannot both be true ("the cause is X" vs "it is not X"; "three years" vs "two years"). Different aspects, moods, priorities or loosely related points do NOT contradict.
- refines: the same specific plan, fact, figure, decision or requirement, made more precise or with one detail corrected
- supersedes: a newer version of the same plan, decision, date or status that replaces it ("ships Sept 22" then "ships Sept 29")
- answers: gives the information an earlier question asks for, about the same subject
- unrelated: anything else

Being about the same project, customer or person is NOT enough for any relation; most pairs are unrelated. A relation holds only when it clearly holds for these two specific statements.`

// verifyQuestions confirm a proposed relation with a narrower yes/no
// question. The six-way choice over-claims on real conversation (most
// "contradicts" were differences of tone or subject; most "answers" were to
// other questions); the narrow question keeps the ones that hold. Measured
// on the pilot: answers 58 → 4, refines 167 → 76, contradicts 131 → 56.
var verifyQuestions = map[string]string{
	"contradicts": "Do NEW and EARLIER make incompatible claims about the same specific thing, so that both cannot be true? A guess, expectation or hypothesis that the other statement rules out counts. yes or no.",
	"answers":     "Does NEW give the information the EARLIER question asks for? yes or no.",
	"refines":     "Is NEW a more precise or corrected version of the same specific point as EARLIER? yes or no.",
	"supersedes":  "Is NEW a later update of the same plan, decision, position or status that replaces EARLIER? yes or no.",
}

func pairText(st store.Statement, c *liveClaim) string {
	return fmt.Sprintf("EARLIER (%s, %s, said %s): %s%s\n\nNEW (%s, %s, said %s): %s",
		c.Holder, c.Kind, c.SaidAt.Format("Jan 2 2006"), c.Text, notes(st, c),
		st.Holder, st.Kind, st.SaidAt.Format("Jan 2 2006"), st.Text)
}

func (k *Linker) verify(ctx context.Context, rel string, st store.Statement, c *liveClaim) (bool, error) {
	q, ok := verifyQuestions[rel]
	if !ok {
		return true, nil
	}
	p, err := k.Decider.Decide(ctx, []provider.Message{{Role: "system", Content: linkSystem}, {Role: "user", Content: pairText(st, c) + "\n\n" + q}}, []string{"yes", "no"})
	if err != nil {
		return false, err
	}
	k.trace("verify %s %q vs claim %d: %.2f", rel, clip(st.Text, 50), c.ID, p["yes"])
	return p["yes"] >= 0.5, nil
}

// minRelP is the probability a relation needs from the pair judge; below
// it the pair counts as unrelated.
const minRelP = 0.5

// judgeEach asks about each candidate on its own, one forward pass each,
// and reads the relation's probability rather than a generated label: no
// verdict can land on the wrong candidate, and a weak verdict can be
// refused.
func (k *Linker) judgeEach(ctx context.Context, st store.Statement, cs []*liveClaim) (map[int]string, error) {
	res := map[int]string{}
	for i, c := range cs {
		msg := pairText(st, c) + "\n\nHow does NEW relate to EARLIER? One of: same, contradicts, refines, supersedes, answers, unrelated."
		p, err := k.Decider.Decide(ctx, []provider.Message{{Role: "system", Content: linkSystem}, {Role: "user", Content: msg}}, relations)
		if err != nil {
			return nil, err
		}
		best, bp := "unrelated", 0.0
		for _, r := range relations {
			if p[r] > bp {
				best, bp = r, p[r]
			}
		}
		k.trace("judge %q vs claim %d: %s %.2f", clip(st.Text, 50), c.ID, best, bp)
		if bp < minRelP {
			continue
		}
		rel := settle(best, st, c)
		if rel == "" {
			continue
		}
		ok, err := k.verify(ctx, rel, st, c)
		if err != nil {
			return nil, err
		}
		if ok {
			res[i] = rel
		}
	}
	return res, nil
}

func (k *Linker) judge(ctx context.Context, st store.Statement, cs []*liveClaim) (map[int]string, error) {
	if k.Decider != nil {
		return k.judgeEach(ctx, st, cs)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "NEW (%s, %s, said %s): %s\n\nEarlier claims:\n", st.Holder, st.Kind, st.SaidAt.Format("Jan 2 2006"), st.Text)
	for i, c := range cs {
		fmt.Fprintf(&b, "%d. (%s, %s, said %s): %s%s\n", i+1, c.Holder, c.Kind, c.SaidAt.Format("Jan 2 2006"), c.Text, notes(st, c))
	}
	out, err := k.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: judgePrompt}, {Role: "user", Content: b.String()}},
		provider.ChatOptions{Schema: judgeSchema, Temperature: 0, MaxTokens: 200}, nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Relations []struct {
			Claim    int    `json:"claim"`
			Relation string `json:"relation"`
		} `json:"relations"`
	}
	if json.Unmarshal([]byte(out), &parsed) != nil {
		return map[int]string{}, nil
	}
	res := map[int]string{}
	// One relation per pair, the most consequential if the model gives
	// several ("contradicts" and "refines" for the same claim).
	rank := map[string]int{"same": 5, "supersedes": 4, "contradicts": 3, "answers": 2, "refines": 1, "unrelated": 0}
	for _, r := range parsed.Relations {
		rel := strings.ToLower(strings.TrimSpace(r.Relation))
		ok := false
		for _, x := range relations {
			ok = ok || rel == x
		}
		if ok && r.Claim >= 1 && r.Claim <= len(cs) {
			rel = settle(rel, st, cs[r.Claim-1])
		}
		if ok && rel != "" && r.Claim >= 1 && r.Claim <= len(cs) {
			if cur, had := res[r.Claim-1]; !had || rank[rel] > rank[cur] {
				res[r.Claim-1] = rel
			}
		}
	}
	return res, nil
}

// fits rejects relations that make no sense for the kinds involved; the
// judge proposes some anyway ("answers" a statement that isn't a question).
func fits(rel, newKind, oldKind string) bool {
	switch rel {
	case "answers":
		return oldKind == "question" && newKind != "question"
	case "supersedes", "refines", "contradicts":
		return newKind != "question" && oldKind != "question"
	case "same":
		return (newKind == "question") == (oldKind == "question")
	}
	return true
}

var negations = map[string]bool{"not": true, "no": true, "never": true, "isn't": true, "wasn't": true, "aren't": true, "weren't": true,
	"won't": true, "don't": true, "doesn't": true, "didn't": true, "cannot": true, "can't": true, "couldn't": true, "shouldn't": true,
	"wouldn't": true, "haven't": true, "hasn't": true, "hadn't": true, "ain't": true, "neither": true, "nor": true, "ruled": true,
	"unlikely": true, "nobody": true, "nothing": true, "none": true}

func negated(s string) bool {
	s = strings.NewReplacer("’", "'", "‘", "'").Replace(s)
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if negations[strings.Trim(w, ".,;:!?\"'")] {
			return true
		}
	}
	return false
}

// sharedWords counts content words (four letters or more) both texts use.
func sharedWords(a, b string) int {
	set := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(a), func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
		if len(w) >= 4 && !negations[w] {
			set[w] = true
		}
	}
	n := 0
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(b), func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
		if set[w] && !seen[w] {
			seen[w] = true
			n++
		}
	}
	return n
}

// valuesAgree: statements with typed values join without the judge only if
// the values are the same (or neither has one).
func valuesAgree(a, b store.Value) bool {
	switch Compare(a, b) {
	case "same":
		return true
	case "unknown":
		return (a.Type == "none" || a.Type == "") && (b.Type == "none" || b.Type == "")
	}
	return false
}

// sameFigures reports whether two texts carry the same numbers and date
// words: near-identical sentences differing only there say different things.
func sameFigures(a, b string) bool {
	fa, fb := figures(a), figures(b)
	if len(fa) != len(fb) {
		return false
	}
	for k := range fa {
		if !fb[k] {
			return false
		}
	}
	return true
}

var monthsAndDays = map[string]bool{
	"january": true, "february": true, "march": true, "april": true, "may": true, "june": true, "july": true,
	"august": true, "september": true, "october": true, "november": true, "december": true,
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true,
	"today": true, "tomorrow": true, "yesterday": true, "next": true, "last": true,
}

func figures(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '%') }) {
		w = strings.Trim(w, ".")
		if monthsAndDays[w] || strings.IndexFunc(w, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0 {
			out[w] = true
		}
	}
	return out
}

func add(a, b []float32) []float32 {
	if a == nil {
		return append([]float32(nil), b...)
	}
	for i := range a {
		a[i] += b[i]
	}
	return a
}

func normalized(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	if s == 0 {
		return out
	}
	n := float32(1 / math.Sqrt(s))
	for i, x := range v {
		out[i] = x * n
	}
	return out
}

func cos(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return d / math.Sqrt(na*nb)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
