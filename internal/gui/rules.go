package gui

import (
	"fmt"
	"io"
	"strings"

	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/store"
)

// SetAliasRule decides, or decides again, what an alias means: "always"
// (every use, new meetings too), "these" (the statements given; reviewed
// are all the uses the owner looked at), "apart" (both forms are right),
// or "review" (undecided again). keep, when it differs from the alias's
// kept form, is the right form the owner typed: the rule is made for it,
// and a proposal is kept as replaced. It returns the rule's alias id.
func (a *App) SetAliasRule(id int64, mode, keep string, statements, reviewed []int64) (int64, error) {
	if a.eng == nil {
		return 0, fmt.Errorf("not ready: %s", a.err)
	}
	as, err := a.eng.Store.LearnedAliases("")
	if err != nil {
		return 0, err
	}
	var x *store.LearnedAlias
	for i := range as {
		if as[i].ID == id {
			x = &as[i]
		}
	}
	if x == nil {
		return 0, fmt.Errorf("no alias %d", id)
	}
	rule := id
	if keep = strings.TrimSpace(keep); keep != "" && keep != x.Keep && mode != store.RuleReview {
		if strings.EqualFold(keep, x.Variant) {
			return 0, fmt.Errorf("the right form is the one written: choose “both are right”")
		}
		if rule, err = a.eng.Store.AddAlias(x.Variant, keep, "owner"); err != nil {
			return 0, err
		}
		// The old pairing gives way (a proposal, or a rule decided again
		// with another right form), kept so it isn't proposed again.
		if err := a.eng.Store.MarkAliasReplaced(id, rule, "owner"); err != nil {
			return 0, err
		}
	}
	// Uses of a rule for some uses that weren't listed (past the review
	// list's length) keep what was decided for them.
	if mode == store.RuleThese && rule == id && x.Mode() == store.RuleThese {
		listed := map[int64]bool{}
		for _, s := range reviewed {
			listed[s] = true
		}
		for _, s := range x.Only() {
			if !listed[s] {
				statements = append(statements, s)
			}
		}
	}
	if err := a.eng.Store.SetAliasRule(rule, mode, statements, reviewed, "owner"); err != nil {
		return 0, err
	}
	a.ruleChanged(rule)
	if rule != id {
		a.ruleChanged(id)
	}
	return rule, nil
}

// StatementEdits lists the owner's corrections to statements.
func (a *App) StatementEdits() ([]store.StatementEdit, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	return a.eng.Store.StatementEdits()
}

// UndoStatementEdit puts a statement back as it was before a correction,
// mending the graph around it.
func (a *App) UndoStatementEdit(id int64) error {
	if a.eng == nil {
		return fmt.Errorf("not ready: %s", a.err)
	}
	r, err := a.eng.Runner(nil, io.Discard)
	if err != nil {
		return err
	}
	return r.UndoStatementEdit(a.ctx, pipeline.KnowledgeOptions{LLM: a.eng.Chat, Embedder: a.eng.Embedder}, id)
}

// ruleChanged brings the panel and what is derived from the statements
// using the alias's variant in line with the rules now, in the background:
// their vectors and search text, and their threads' statuses
// (pipeline.RefreshSpellings). The views refresh when it is done.
func (a *App) ruleChanged(id int64) {
	if a.live != nil {
		a.live.Reload()
	}
	if !a.eng.Cfg.Knowledge.ApplySpellings {
		return
	}
	as, err := a.eng.Store.LearnedAliases("")
	if err != nil {
		return
	}
	var words []string
	for _, x := range as {
		if x.ID == id {
			words = append(words, x.Variant)
		}
	}
	if len(words) == 0 {
		return
	}
	go func() {
		a.spellMu.Lock()
		defer a.spellMu.Unlock()
		msg := "spelling rule"
		r, err := a.eng.Runner(nil, io.Discard)
		if err == nil {
			var n int
			n, err = r.RefreshSpellings(a.ctx, pipeline.KnowledgeOptions{LLM: a.eng.Chat, Embedder: a.eng.Embedder}, words)
			msg += fmt.Sprintf("; %d statements refreshed", n)
		}
		if err != nil {
			msg += "; refresh: " + err.Error()
		}
		a.emit("update:done", msg)
	}()
}
