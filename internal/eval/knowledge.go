package eval

import (
	"fmt"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

func hasAll(text string, words []string) bool {
	t := strings.ToLower(text)
	for _, w := range words {
		if !strings.Contains(t, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

func (h *Harness) now(c Case, s *Suite) time.Time {
	for _, v := range []string{c.At, s.Now} {
		if v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t
			}
		}
	}
	if !h.Now.IsZero() {
		return h.Now
	}
	return time.Now()
}

func (h *Harness) knowledgeCase(c Case, s *Suite) (Result, error) {
	r := Result{ID: c.ID, Kind: c.Kind}
	sc, err := sensitivity.ParseScope(c.Scope, h.InternalDomains)
	if err != nil {
		return r, err
	}
	v := knowledge.Views{Store: h.Store, Scope: sc, Now: h.now(c, s)}
	switch c.Kind {
	case "statement":
		return h.statementCase(c)
	case "claim":
		cv, err := findClaimKind(v, c.ClaimHas, c.ClaimKind)
		if err != nil {
			return r, err
		}
		if c.Absent {
			r.Pass = cv == nil
			if cv != nil {
				r.Detail = fmt.Sprintf("LEAK claim %d visible: %q", cv.ID, cv.Text)
			}
			return r, nil
		}
		if cv == nil {
			r.Detail = fmt.Sprintf("no visible claim with %v", c.ClaimHas)
			return r, nil
		}
		r.Pass = true
		var notes []string
		if len(c.Statuses) > 0 && !contains(c.Statuses, cv.Belief.Status) {
			r.Pass = false
			notes = append(notes, fmt.Sprintf("status %s (%s), want %v", cv.Belief.Status, cv.Belief.Why, c.Statuses))
		}
		n := len(cv.Statements)
		if c.MinEvidence > 0 && n < c.MinEvidence {
			r.Pass = false
			notes = append(notes, fmt.Sprintf("%d visible statements, want ≥%d", n, c.MinEvidence))
		}
		if c.MaxEvidence > 0 && n > c.MaxEvidence {
			r.Pass = false
			notes = append(notes, fmt.Sprintf("LEAK %d visible statements, want ≤%d", n, c.MaxEvidence))
		}
		if c.Relation != "" {
			found := false
			for _, rel := range cv.Relations {
				if rel.Rel == c.Relation && hasAll(rel.Text, c.RelatedHas) {
					found = true
				}
			}
			if !found {
				r.Pass = false
				var got []string
				for _, rel := range cv.Relations {
					got = append(got, rel.Direction+" "+rel.Rel+" "+clip(rel.Text, 50))
				}
				notes = append(notes, fmt.Sprintf("no %s relation to %v (has: %s)", c.Relation, c.RelatedHas, strings.Join(got, "; ")))
			}
		}
		if !r.Pass {
			notes = append(notes, fmt.Sprintf("claim %d %q", cv.ID, clip(cv.Text, 70)))
		}
		r.Detail = strings.Join(notes, "; ")
		return r, nil
	case "topic":
		r.Pass = true
		var notes []string
		if len(c.Together) > 0 {
			var topic int64 = -1
			for _, words := range c.Together {
				cv, err := findClaim(v, words)
				if err != nil {
					return r, err
				}
				switch {
				case cv == nil:
					r.Pass = false
					notes = append(notes, fmt.Sprintf("no claim with %v", words))
				case cv.TopicID == 0:
					r.Pass = false
					notes = append(notes, fmt.Sprintf("claim %v has no topic", words))
				case topic == -1:
					topic = cv.TopicID
				case cv.TopicID != topic:
					r.Pass = false
					notes = append(notes, fmt.Sprintf("claim %v in topic %d, not %d", words, cv.TopicID, topic))
				}
			}
		}
		if len(c.Apart) == 2 {
			a, err := findClaim(v, c.Apart[0])
			if err != nil {
				return r, err
			}
			b, err := findClaim(v, c.Apart[1])
			if err != nil {
				return r, err
			}
			switch {
			case a == nil || b == nil:
				r.Pass = false
				notes = append(notes, fmt.Sprintf("claims not found (%v, %v)", a != nil, b != nil))
			case a.TopicID == 0 || b.TopicID == 0:
				r.Pass = false
				notes = append(notes, fmt.Sprintf("a claim has no topic (%d, %d)", a.TopicID, b.TopicID))
			case a.TopicID == b.TopicID:
				r.Pass = false
				notes = append(notes, fmt.Sprintf("both in topic %d", a.TopicID))
			case len(c.LinkKinds) > 0:
				tv, err := v.Topic(a.TopicID)
				if err != nil {
					return r, err
				}
				found := ""
				for _, rel := range tv.Related {
					if rel.ID == b.TopicID {
						found = rel.Kind
					}
				}
				if !contains(c.LinkKinds, found) {
					r.Pass = false
					notes = append(notes, fmt.Sprintf("topics %d and %d related as %q, want %v", a.TopicID, b.TopicID, found, c.LinkKinds))
				}
			}
		}
		if len(c.LabelLacks) > 0 {
			ts, err := v.Topics()
			if err != nil {
				return r, err
			}
			for _, t := range ts {
				for _, w := range c.LabelLacks {
					if strings.Contains(strings.ToLower(t.Label+" "+t.Summary+" "+strings.Join(t.Keywords, " ")), strings.ToLower(w)) {
						r.Pass = false
						notes = append(notes, fmt.Sprintf("LEAK topic %d label %q shows %q", t.ID, t.Label, w))
					}
				}
			}
		}
		r.Detail = strings.Join(notes, "; ")
		return r, nil
	}
	return r, fmt.Errorf("unknown kind")
}

func findClaim(v knowledge.Views, words []string) (*knowledge.ClaimView, error) {
	return findClaimKind(v, words, "")
}

// findClaimKind returns the visible claim (of kind, if given) whose
// statements contain the words, preferring the one with the most matching
// statements.
func findClaimKind(v knowledge.Views, words []string, kind string) (*knowledge.ClaimView, error) {
	ids, err := v.Store.AllClaimIDs()
	if err != nil {
		return nil, err
	}
	cs, err := v.Claims(ids)
	if err != nil {
		return nil, err
	}
	var best *knowledge.ClaimView
	bestN := 0
	for i := range cs {
		if kind != "" && cs[i].Kind != kind {
			continue
		}
		n := 0
		for _, st := range cs[i].Statements {
			if hasAll(st.Text, words) {
				n++
			}
		}
		if n > bestN {
			best, bestN = &cs[i], n
		}
	}
	return best, nil
}

func (h *Harness) statementCase(c Case) (Result, error) {
	r := Result{ID: c.ID, Kind: c.Kind}
	ch, err := h.findChunk(c.Target)
	if err != nil {
		r.Detail = err.Error()
		return r, nil
	}
	ids, err := h.Store.AllClaimIDs()
	if err != nil {
		return r, err
	}
	ev, err := h.Store.Evidence(sensitivity.Owner(), ids)
	if err != nil {
		return r, err
	}
	var got []string
	for _, e := range ev {
		for _, st := range e.Statements {
			if st.ChunkID != ch.ID {
				continue
			}
			okKind := len(c.Kinds) == 0 || contains(c.Kinds, st.Kind)
			okHolder := c.Holder == "" || st.HolderID == c.Holder || strings.Contains(strings.ToLower(st.Holder), strings.ToLower(c.Holder))
			if okKind && okHolder && hasAll(st.Text, c.TextHas) {
				r.Pass = true
				return r, nil
			}
			got = append(got, fmt.Sprintf("%s/%s: %s", st.Kind, st.Holder, clip(st.Text, 60)))
		}
	}
	r.Detail = fmt.Sprintf("no %v statement by %q with %v (got: %s)", c.Kinds, c.Holder, c.TextHas, strings.Join(got, " | "))
	return r, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

var _ = store.Statement{}
