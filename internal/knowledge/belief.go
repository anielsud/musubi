package knowledge

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/anielsud/musubi/internal/store"
)

// Weights and half-lives (docs/architecture.md). These are the first
// settings; the eval scenarios pin their behaviour, real data tunes them.
var (
	kindWeight = map[string]float64{
		"fact": 1, "decision": 1, "requirement": 1, "commitment": 0.9,
		"opinion": 0.5, "hypothesis": 0.3, "question": 0,
		// insight kinds (docs/insights-direction.md)
		"metric": 1, "outside_fact": 1, "personal": 1, "sensitivity_cue": 1, "rule": 0.9,
		"position": 0.5, "disagreement": 0.5, "framing": 0.5, "issue": 0,
	}
	// sourceWeight: written words are more deliberate than speech.
	sourceWeight = map[string]float64{"email": 1.2, "document": 1.3, "chat": 1.0, "meeting": 1.0}
	halfLife     = map[string]time.Duration{
		"question": 14 * day, "hypothesis": 30 * day, "opinion": 60 * day,
		"fact": 90 * day, "requirement": 180 * day, "commitment": 30 * day,
		"metric": 90 * day, "outside_fact": 90 * day, "position": 60 * day, "disagreement": 60 * day,
		"framing": 180 * day, "rule": 365 * day, "personal": 365 * day, "sensitivity_cue": 30 * day, "issue": 14 * day,
		// decisions don't fade; they change by being superseded
	}
)

const day = 24 * time.Hour

// speculativeKind: views rather than facts; a claim held only as these is
// "speculative" until something firmer joins it.
var speculativeKind = map[string]bool{"opinion": true, "hypothesis": true, "question": true,
	"position": true, "disagreement": true, "framing": true, "issue": true}

// Contribution is one statement's part in a claim's belief.
type Contribution struct {
	StatementID int64   `json:"statementId"`
	Holder      string  `json:"holder"`
	Kind        string  `json:"kind"`
	Weight      float64 `json:"weight"` // before decay
	Decayed     float64 `json:"decayed"`
	Against     bool    `json:"against"`
}

// Belief is how much to believe a claim now, from what is visible.
type Belief struct {
	Score        float64        `json:"score"`
	Support      float64        `json:"support"`
	Against      float64        `json:"against"`
	Status       string         `json:"status"`
	Holders      int            `json:"holders"`
	Written      bool           `json:"written"`
	Last         time.Time      `json:"last"`
	SupersededBy []int64        `json:"supersededBy,omitempty"`
	AnsweredBy   []int64        `json:"answeredBy,omitempty"`
	PendingBy    []int64        `json:"pendingBy,omitempty"` // promises to find out
	CompletedBy  []int64        `json:"completedBy,omitempty"`
	Verdict      string         `json:"verdict,omitempty"`
	Why          string         `json:"why"`
	Parts        []Contribution `json:"parts"`
}

func evidence(st store.Statement, now time.Time) (raw, decayed float64) {
	c := st.Certainty
	if c <= 0 || c > 1 {
		c = 0.7
	}
	raw = kindWeight[st.Kind] * c
	if w, ok := sourceWeight[st.ItemKind]; ok {
		raw *= w
	}
	raw = math.Min(raw, 1)
	// A statement that didn't check out against its source counts for
	// little until someone verifies it.
	if !st.Grounded {
		raw *= 0.3
	}
	decayed = raw
	if hl, ok := halfLife[st.Kind]; ok {
		age := now.Sub(st.SaidAt)
		if age > 0 {
			decayed = raw * math.Pow(0.5, float64(age)/float64(hl))
		}
	}
	return raw, decayed
}

// combine merges evidence per holder first, then across holders: one
// person repeating themselves adds little; independent people add a lot.
func combine(parts []Contribution) (score float64, holders int) {
	by := map[string][]float64{}
	for _, p := range parts {
		by[p.Holder] = append(by[p.Holder], p.Decayed)
	}
	notAll := 1.0
	for _, es := range by {
		sort.Float64s(es)
		top := es[len(es)-1]
		h := 0.6 * top * (1 + 0.15*math.Min(float64(len(es)-1), 2))
		notAll *= 1 - math.Min(h, 0.9)
	}
	return 1 - notAll, len(by)
}

// Compute works out belief in a claim from its visible evidence at now.
func Compute(e *store.ClaimEvidence, now time.Time) Belief {
	var b Belief
	var support []Contribution
	allSpeculative := true
	var rawMax, decMax float64
	for _, st := range e.Statements {
		raw, dec := evidence(st, now)
		c := Contribution{StatementID: st.ID, Holder: holderKey(st), Kind: st.Kind, Weight: raw, Decayed: dec}
		support = append(support, c)
		if !speculativeKind[st.Kind] {
			allSpeculative = false
		}
		if st.ItemKind == "email" || st.ItemKind == "document" {
			b.Written = true
		}
		if st.SaidAt.After(b.Last) {
			b.Last = st.SaidAt
		}
		rawMax, decMax = math.Max(rawMax, raw), math.Max(decMax, dec)
	}
	b.Support, b.Holders = combine(support)

	// Relations: contradictions count against; a later visible claim that
	// supersedes this one, or answers it, changes its status.
	var against []Contribution
	var againstLast time.Time
	for _, r := range e.Relations {
		other := r.To
		if other == e.ClaimID {
			other = r.From
		}
		sts := e.Related[other]
		if len(sts) == 0 {
			continue // not visible in this scope
		}
		switch {
		case r.Rel == "contradicts":
			for _, st := range sts {
				raw, dec := evidence(st, now)
				against = append(against, Contribution{StatementID: st.ID, Holder: holderKey(st), Kind: st.Kind, Weight: raw, Decayed: dec, Against: true})
				if st.SaidAt.After(againstLast) {
					againstLast = st.SaidAt
				}
			}
		case r.Rel == "supersedes" && r.To == e.ClaimID:
			b.SupersededBy = append(b.SupersededBy, r.From)
		case r.Rel == "answers" && r.To == e.ClaimID:
			b.AnsweredBy = append(b.AnsweredBy, r.From)
		case r.Rel == "pending" && r.To == e.ClaimID:
			b.PendingBy = append(b.PendingBy, r.From)
		case r.Rel == "completes" && r.To == e.ClaimID:
			b.CompletedBy = append(b.CompletedBy, r.From)
		}
	}
	b.Against, _ = combine(against)
	b.Parts = append(support, against...)
	b.Score = b.Support * (1 - 0.7*b.Against)

	for _, j := range e.Judgments {
		b.Verdict = j.Verdict
	}
	switch {
	case b.Verdict == "false":
		b.Status, b.Why = "disproven", "you marked it false"
	case b.Verdict == "verified":
		b.Status, b.Why = "established", "you verified it"
	case b.Verdict == "disputed":
		b.Status, b.Why = "contested", "you disputed it"
	case len(b.SupersededBy) > 0:
		b.Status, b.Why = "superseded", "a later claim replaces it"
	case len(b.CompletedBy) > 0:
		b.Status, b.Why = "done", "a later statement says it was done"
	case e.Kind == "question" && len(b.AnsweredBy) > 0:
		b.Status, b.Why = "answered", "a later statement answers it"
	case e.Kind == "question" && len(b.PendingBy) > 0:
		b.Status, b.Why = "open", "open; someone has committed to find out"
	case e.Kind == "question":
		b.Status, b.Why = "open", "an open question"
		if b.Last.Before(now.Add(-2 * halfLife["question"])) {
			b.Status, b.Why = "stale", "an old question nobody returned to"
		}
	case b.Against >= 0.3 && b.Against > b.Support && againstLast.After(b.Last):
		b.Status, b.Why = "disproven", "contradicted by stronger, later evidence"
	case b.Support >= 0.3 && b.Against >= 0.3:
		b.Status, b.Why = "contested", "evidence on both sides"
	case rawMax > 0 && decMax < 0.25*rawMax:
		b.Status, b.Why = "stale", "nothing has reinforced it for a long time"
	case allSpeculative && b.Holders >= 2 && b.Score >= 0.35:
		// Still a view, never established, but one several people hold
		// carries more than one person's guess.
		b.Status, b.Why = "likely", fmt.Sprintf("a view %d people share", b.Holders)
	case allSpeculative:
		b.Status, b.Why = "speculative", "only opinions or guesses so far"
	case b.Score >= 0.55 && (b.Holders >= 2 || b.Written):
		b.Status, b.Why = "established", "independent or written confirmation"
	case b.Score >= 0.35:
		b.Status, b.Why = "likely", "stated, not yet confirmed independently"
	default:
		b.Status, b.Why = "uncertain", "weak evidence"
	}
	return b
}

// holderKey identifies a holder: their entity when resolved, else the name.
func holderKey(st store.Statement) string {
	if st.HolderID != "" {
		return st.HolderID
	}
	return st.Holder
}
