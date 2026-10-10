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
const LinkVersion = "k3"

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

Reply as JSON: {"relations":[{"claim":1,"relation":"..."}]}`

type liveClaim struct {
	store.ClaimRow
	sum []float32 // sum of member vectors
}

// Linker links statements to claims, in the order they were said.
type Linker struct {
	Store *store.Store
	LLM   provider.LLM // nil: near-identical matches only
	Vecs  map[int64][]float32
	// Trace receives one line per decision, for diagnostics and the eval.
	Trace func(format string, args ...any)

	claims []*liveClaim
}

type LinkStats struct {
	New, Joined, Judged, Related int
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
			if s := cos(v, c.Centroid); s >= considerAt {
				cands = append(cands, cand{c, s})
			}
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
		if len(cands) > maxJudged {
			cands = cands[:maxJudged]
		}
		// Near-identical, the same kind, and the same numbers and dates:
		// join directly.
		if len(cands) > 0 && cands[0].sim >= sameAt && cands[0].c.Kind == st.Kind && sameFigures(st.Text, cands[0].c.Text) {
			if err := k.join(st, cands[0].c, cands[0].sim, "near-identical"); err != nil {
				return stats, err
			}
			stats.Joined++
			k.trace("%q joins claim %d (similarity %.2f)", clip(st.Text, 80), cands[0].c.ID, cands[0].sim)
			continue
		}
		rels := map[int]string{}
		if len(cands) > 0 && k.LLM != nil {
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
			home = &liveClaim{ClaimRow: store.ClaimRow{ID: id, Kind: st.Kind, Centroid: normalized(v), Members: []int64{st.ID}, Text: st.Text, Holder: st.Holder, SaidAt: st.SaidAt}, sum: append([]float32(nil), v...)}
			k.claims = append(k.claims, home)
			stats.New++
		}
		for i, c := range cands {
			rel := rels[i]
			if rel == "" || rel == "same" || rel == "unrelated" || c.c.ID == home.ID {
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

func (k *Linker) join(st store.Statement, c *liveClaim, sim float64, how string) error {
	v := k.Vecs[st.ID]
	c.sum = add(c.sum, v)
	c.Centroid = normalized(c.sum)
	c.Members = append(c.Members, st.ID)
	return k.Store.JoinClaim(st.ID, c.ID, sim, how, c.Centroid)
}

func (k *Linker) judge(ctx context.Context, st store.Statement, cs []*liveClaim) (map[int]string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "NEW (%s, %s, said %s): %s\n\nEarlier claims:\n", st.Holder, st.Kind, st.SaidAt.Format("Jan 2 2006"), st.Text)
	for i, c := range cs {
		fmt.Fprintf(&b, "%d. (%s, %s, said %s): %s\n", i+1, c.Holder, c.Kind, c.SaidAt.Format("Jan 2 2006"), c.Text)
	}
	out, err := k.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: judgePrompt}, {Role: "user", Content: b.String()}},
		provider.ChatOptions{JSON: true, Temperature: 0, MaxTokens: 200}, nil)
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
		if ok && r.Claim >= 1 && r.Claim <= len(cs) && fits(rel, st.Kind, cs[r.Claim-1].Kind) {
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
