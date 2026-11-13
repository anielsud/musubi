package mcpserver

import (
	"fmt"
	"strings"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Sens is the sensitivity block on every result.
type Sens struct {
	Level      string   `json:"level"`
	Categories []string `json:"categories,omitempty"`
	// Party is how many people were party to the source; 0 for something
	// drawn from many sources.
	Party int               `json:"party,omitempty"`
	Share sensitivity.Share `json:"share"`
	Why   string            `json:"why"`
	// Fits is set when the call named an audience ("for").
	Fits *bool `json:"fits,omitempty"`
}

// labeller labels one call's results, counting them for the summary line.
type labeller struct {
	st *store.Store
	// fit is the named audience's scope, nil when none was named.
	fit *sensitivity.Scope
	aud map[string][]string
	own map[string]sensitivity.Label
	n   int
	by  map[sensitivity.Share]int
	top sensitivity.Share
	// fits counts results that fit the named audience.
	fits int
}

func (s *Server) labeller(recipients []string) (*labeller, error) {
	l := &labeller{st: s.e.Store, aud: map[string][]string{}, own: map[string]sensitivity.Label{}, by: map[sensitivity.Share]int{}, top: sensitivity.SharePublic}
	if len(recipients) > 0 {
		sc, err := sensitivity.ForAudience(s.e.Cfg.Owner.InternalDomains, recipients)
		if err != nil {
			return nil, fmt.Errorf("for: %w", err)
		}
		l.fit = &sc
	}
	return l, nil
}

// source reads an item's own label and audience once per call. Anything
// from an item is at least as sensitive as the item, so its label is
// combined in: a read path that forgot to set a label (zero reads as
// public) still fails closed. An item with no label is confidential, and
// no audience reads as party to nothing, which only narrows "fits".
func (l *labeller) source(itemID string) (sensitivity.Label, []string) {
	if a, ok := l.aud[itemID]; ok {
		return l.own[itemID], a
	}
	own, a, err := l.st.ItemLabel(itemID)
	if err != nil {
		own = sensitivity.Label{Level: sensitivity.Confidential}
	}
	l.own[itemID], l.aud[itemID] = own, a
	return own, a
}

// item labels something from one source item.
func (l *labeller) item(lab sensitivity.Label, itemID string) Sens {
	own, aud := l.source(itemID)
	s := l.block(sensitivity.Combine(own, lab), aud)
	s.Party = len(aud)
	return s
}

// mixed labels something drawn from many items (a status, a summary of
// several meetings): it fits an audience only if it is open to anyone in
// it, the rule thread statuses follow in scoped reads.
func (l *labeller) mixed(lab sensitivity.Label) Sens {
	return l.block(lab, nil)
}

func (l *labeller) block(lab sensitivity.Label, aud []string) Sens {
	sh := sensitivity.ShareOf(lab)
	s := Sens{Level: lab.Level.String(), Categories: lab.Categories.Names(), Share: sh, Why: sensitivity.Why(lab, len(aud))}
	l.n++
	l.by[sh]++
	if sh.Narrower(l.top) {
		l.top = sh
	}
	if l.fit != nil {
		f := sensitivity.Visible(lab, aud, *l.fit)
		s.Fits = &f
		if f {
			l.fits++
		}
	}
	return s
}

// summary is the line a result opens with.
func (l *labeller) summary() string {
	if l.n == 0 {
		return "No results."
	}
	var parts []string
	for _, sh := range []sensitivity.Share{sensitivity.SharePublic, sensitivity.ShareInternal, sensitivity.ShareParty, sensitivity.ShareOwner} {
		if n := l.by[sh]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sh))
		}
	}
	out := fmt.Sprintf("Sensitivity of %d labelled parts: %s.", l.n, strings.Join(parts, ", "))
	if l.fit != nil {
		out += fmt.Sprintf(" %d of %d fit the named audience.", l.fits, l.n)
	}
	return out
}
