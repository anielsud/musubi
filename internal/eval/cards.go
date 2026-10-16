package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// cardCase checks the card the live panel would show for what was said:
// the topic found, the lines on it (by section), what must stay off it,
// whether it is collapsed, and that a scope which mustn't see it gets none.
func (h *Harness) cardCase(ctx context.Context, c Case, s *Suite) (Result, error) {
	r := Result{ID: c.ID, Kind: c.Kind, Recall: -1}
	if h.cards == nil {
		x, err := knowledge.BuildCardIndex(h.Store)
		if err != nil {
			return r, err
		}
		h.cards = x
	}
	sc, err := sensitivity.ParseScope(c.Scope, h.InternalDomains)
	if err != nil {
		return r, err
	}
	var embed func(string) ([]float32, error)
	if h.Embedder != nil && !c.TermsOnly {
		embed = func(t string) ([]float32, error) {
			vs, err := h.Embedder.Embed(ctx, []string{t}, true)
			if err != nil || len(vs) == 0 {
				return nil, err
			}
			return vs[0], nil
		}
	}
	a, how, err := h.cards.Find(c.Query, embed)
	if err != nil {
		return r, err
	}
	var card *knowledge.Card
	if !a.IsZero() {
		v := knowledge.Views{Store: h.Store, Scope: sc, Now: h.now(c, s)}
		if card, err = v.Card(h.cards, a, h.OwnerID, h.OwnerName); err != nil {
			return r, err
		}
	}
	if c.Absent {
		r.Pass = card == nil
		if card != nil {
			r.Detail = fmt.Sprintf("LEAK card %q shown under %s (%d lines)", card.Title, sc.Name, card.Lines())
		}
		return r, nil
	}
	if card == nil && len(c.CardExpect) == 0 {
		r.Pass, r.Detail = true, fmt.Sprintf("no card for %q, as wanted", c.Query)
		return r, nil
	}
	if card == nil {
		r.Recall = 0
		r.Detail = fmt.Sprintf("no card for %q (%s)", c.Query, a.Key())
		return r, nil
	}
	var missing, unwanted []string
	found := 0
	for _, e := range c.CardExpect {
		if cardHas(card, e.Section, e.Has) {
			found++
		} else {
			missing = append(missing, strings.TrimSpace(e.Section+" "+strings.Join(e.Has, "+")))
		}
	}
	for _, ws := range c.CardLacks {
		if cardHas(card, "", ws) {
			unwanted = append(unwanted, strings.Join(ws, "+"))
		}
	}
	if len(c.CardExpect) > 0 {
		r.Recall = float64(found) / float64(len(c.CardExpect))
	}
	collapsedOK := c.Collapsed == nil || *c.Collapsed == card.Collapsed
	r.Pass = len(missing) == 0 && len(unwanted) == 0 && collapsedOK
	var d []string
	d = append(d, fmt.Sprintf("%q via %s: %d/%d lines", card.Title, how, found, len(c.CardExpect)))
	if len(missing) > 0 {
		d = append(d, "missing "+strings.Join(missing, ", "))
	}
	if len(unwanted) > 0 {
		d = append(d, "unwanted "+strings.Join(unwanted, ", "))
	}
	if !collapsedOK {
		d = append(d, fmt.Sprintf("collapsed %v, want %v", card.Collapsed, *c.Collapsed))
	}
	r.Detail = strings.Join(d, "; ")
	return r, nil
}

func cardHas(card *knowledge.Card, section string, words []string) bool {
	for _, s := range card.Sections {
		if section != "" && !strings.EqualFold(s.Name, section) {
			continue
		}
		for _, l := range s.Lines {
			if hasAll(l.Text+" "+l.Due, words) {
				return true
			}
		}
	}
	return false
}
