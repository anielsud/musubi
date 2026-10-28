package engine

import (
	"context"
	"time"

	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Cards finds and builds topic cards (docs/topic-cards.md).
type Cards struct {
	Index *knowledge.CardIndex
	eng   *Engine
}

// LoadCards builds the card index from the knowledge layer. Rebuild it
// after the knowledge layer changes.
func (e *Engine) LoadCards() (*Cards, error) {
	return e.LoadCardsBefore(time.Time{})
}

// LoadCardsBefore builds it from what was said before a time (zero: all).
func (e *Engine) LoadCardsBefore(before time.Time) (*Cards, error) {
	x, err := knowledge.BuildCardIndexBefore(e.Store, before)
	if err != nil {
		return nil, err
	}
	return &Cards{Index: x, eng: e}, nil
}

// Find returns the card a text opens: a trigger term it says, else
// (unless termsOnly) the topic of the claims nearest to it. how says which.
func (c *Cards) Find(ctx context.Context, text string, termsOnly bool) (a knowledge.Anchor, how string, err error) {
	var embed func(string) ([]float32, error)
	if !termsOnly && c.eng.Embedder != nil {
		embed = func(t string) ([]float32, error) {
			vs, err := c.eng.Embedder.Embed(ctx, []string{t}, true)
			if err != nil || len(vs) == 0 {
				return nil, err
			}
			return vs[0], nil
		}
	}
	return c.Index.Find(text, embed)
}

// Near ranks the topics nearest a text, best first, at most n (Find's
// claims, with no choice made: candidates for the live picker).
func (c *Cards) Near(ctx context.Context, text string, n int) []int64 {
	if c.eng.Embedder == nil {
		return nil
	}
	vs, err := c.eng.Embedder.Embed(ctx, []string{text}, true)
	if err != nil || len(vs) == 0 || vs[0] == nil {
		return nil
	}
	return c.Index.TopicsNear(vs[0], n, nearMinSim)
}

// nearMinSim: claims less similar than this don't vote in Near (unrelated
// statements sit around 0.59 with nomic-embed-text).
const nearMinSim = 0.6

// Card builds a topic's card under a scope, from what was said by now;
// nil when nothing is visible.
func (c *Cards) Card(sc sensitivity.Scope, a knowledge.Anchor, now time.Time) (*knowledge.Card, error) {
	v := knowledge.Views{Store: c.eng.Store, Scope: sc, Now: now}
	return v.Card(c.Index, a, c.eng.OwnerEntity(), c.eng.Cfg.Owner.Name)
}
