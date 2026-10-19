package gui

import (
	"fmt"
	"io"
	"strings"

	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/store"
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
	// NewUses: confirmed for some uses only, and used since in places not
	// yet reviewed (they are listed for review).
	NewUses int `json:"newUses,omitempty"`
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
	// To review: the proposed ones, and those confirmed for some uses that
	// have been used since where nobody has looked.
	var newUses map[int64]int
	if status == "proposed" {
		partial, err := a.eng.Store.LearnedAliases("confirmed")
		if err != nil {
			return nil, err
		}
		newUses = map[int64]int{}
		for _, x := range partial {
			if r := x.Reviewed(); r != nil {
				us, err := a.eng.Store.UsagesOf(x, 0)
				if err != nil {
					return nil, err
				}
				for _, u := range us {
					if !r[u.Statement] {
						newUses[x.ID]++
					}
				}
				if newUses[x.ID] > 0 {
					as = append([]store.LearnedAlias{x}, as...)
				}
			}
		}
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
			Statements: im.Statements, Items: im.Items, Threads: len(im.Threads), NewLinks: len(im.NewLinks), Sample: im.Sample, NewUses: newUses[x.ID]})
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

// AliasUsages lists where an alias's variant is used, for the review
// sidebar.
func (a *App) AliasUsages(id int64, limit int) ([]store.AliasUsage, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	as, err := a.eng.Store.LearnedAliases("")
	if err != nil {
		return nil, err
	}
	for _, x := range as {
		if x.ID != id {
			continue
		}
		us, err := a.eng.Store.UsagesOf(x, 0)
		if err != nil {
			return nil, err
		}
		// Confirmed for some uses: only those not yet reviewed.
		if r := x.Reviewed(); r != nil {
			var fresh []store.AliasUsage
			for _, u := range us {
				if !r[u.Statement] {
					fresh = append(fresh, u)
				}
			}
			us = fresh
		}
		if limit > 0 && len(us) > limit {
			us = us[:limit]
		}
		return us, nil
	}
	return nil, fmt.Errorf("no alias %d", id)
}

// ConfirmAliasIn confirms an alias for the checked uses of those reviewed;
// with every use checked (and none reviewed and left out before) it is
// confirmed everywhere, future meetings included.
func (a *App) ConfirmAliasIn(id int64, statements, reviewed []int64, all bool) error {
	if a.eng == nil {
		return fmt.Errorf("not ready: %s", a.err)
	}
	partial := false
	if as, err := a.eng.Store.LearnedAliases("confirmed"); err == nil {
		for _, x := range as {
			if x.ID == id && x.Reviewed() != nil {
				partial = true
			}
		}
	}
	var err error
	if all && !partial {
		err = a.eng.Store.DecideAlias(id, true, "owner")
	} else {
		err = a.eng.Store.ConfirmAliasIn(id, statements, reviewed, "owner")
	}
	if err == nil && a.live != nil {
		a.live.Reload()
	}
	return err
}
