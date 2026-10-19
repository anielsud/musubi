package gui

import (
	"fmt"
	"io"
	"strings"

	"github.com/anielsud/musubi/internal/pipeline"
)

// AliasView is an alias with what it would touch, for review.
type AliasView struct {
	ID         int64    `json:"id"`
	Keep       string   `json:"keep"`
	Variant    string   `json:"variant"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	Score      float64  `json:"score"`
	Statements int      `json:"statements"`
	Items      int      `json:"items"`
	Threads    int      `json:"threads"`
	NewLinks   int      `json:"newLinks"`
	Sample     []string `json:"sample"`
}

// Aliases lists aliases with a status ("proposed", "confirmed", "rejected")
// and their impact. The owner's view: corrections are the owner's call.
func (a *App) Aliases(status string, limit int) ([]AliasView, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	as, err := a.eng.Store.LearnedAliases(status)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(as) > limit {
		as = as[:limit]
	}
	out := make([]AliasView, 0, len(as))
	for _, x := range as {
		im, err := a.eng.Store.ImpactOf(x)
		if err != nil {
			return nil, err
		}
		out = append(out, AliasView{ID: x.ID, Keep: x.Keep, Variant: x.Variant, Kind: x.Kind, Status: x.Status, Score: x.Score,
			Statements: im.Statements, Items: im.Items, Threads: len(im.Threads), NewLinks: len(im.NewLinks), Sample: im.Sample})
	}
	return out, nil
}

// DecideAlias confirms or rejects an alias; confirmed spellings apply when
// reading (nothing is rewritten), and the live panel picks them up.
func (a *App) DecideAlias(id int64, confirm bool) error {
	if a.eng == nil {
		return fmt.Errorf("not ready: %s", a.err)
	}
	if err := a.eng.Store.DecideAlias(id, confirm, "owner"); err != nil {
		return err
	}
	if a.live != nil {
		a.live.Reload()
	}
	return nil
}

// CorrectStatement applies the owner's correction to a statement's text
// or speakers (who: comma-separated; empty keeps it), and mends the graph
// around it. The owner's view only.
func (a *App) CorrectStatement(id int64, text, who string) error {
	if a.eng == nil {
		return fmt.Errorf("not ready: %s", a.err)
	}
	var names []string
	for _, n := range strings.Split(who, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	r, err := a.eng.Runner(nil, io.Discard)
	if err != nil {
		return err
	}
	_, err = r.CorrectStatement(a.ctx, pipeline.KnowledgeOptions{LLM: a.eng.Chat, Embedder: a.eng.Embedder}, id, strings.TrimSpace(text), names)
	return err
}
