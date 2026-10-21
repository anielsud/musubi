package store

import (
	"regexp"
	"strings"
)

// Spellings are confirmed spelling aliases (variant → kept form, the
// variant lower-cased: ConfirmedSpellings). They are applied when reading:
// stored text is never rewritten (docs/incremental-design.md, "Entities
// and aliases").
type Spellings map[string]string

var wordRe = regexp.MustCompile(`[\pL\pN']+`)

// Canonical rewrites the whole-word variants in s to their kept forms.
func (sp Spellings) Canonical(s string) string {
	if len(sp) == 0 {
		return s
	}
	return wordRe.ReplaceAllStringFunc(s, func(w string) string {
		l := strings.ToLower(w)
		if k, ok := sp[l]; ok {
			return k
		}
		// A possessive is the word: "Ilka's" is read as the kept form's.
		if i := len(l) - 2; i > 0 && l[i:] == "'s" {
			if k, ok := sp[l[:i]]; ok {
				return k + w[i:]
			}
		}
		return w
	})
}

// Expand adds the kept forms of any variants in s, so a search finds both
// spellings.
func (sp Spellings) Expand(s string) string {
	if c := sp.Canonical(s); c != s {
		return s + " " + c
	}
	return s
}

// Corrector applies the owner's spelling rules to statements: those
// confirmed for every use (All) and those confirmed for some statements
// only (Only: statement → variant → kept form).
type Corrector struct {
	All  Spellings
	Only map[int64]map[string]string
}

// Corrector loads the confirmed spelling rules: none when the store
// doesn't read with them (ReadWithRules).
func (s *Store) Corrector() (Corrector, error) {
	if !s.ReadWithRules {
		return Corrector{}, nil
	}
	all, err := s.ConfirmedSpellings()
	if err != nil {
		return Corrector{}, err
	}
	only, err := s.ScopedSpellings()
	if err != nil {
		return Corrector{}, err
	}
	return Corrector{All: all, Only: only}, nil
}

// For is the spellings that apply to one statement.
func (c Corrector) For(id int64) Spellings {
	if len(c.Only[id]) == 0 {
		return c.All
	}
	m := Spellings{}
	for k, v := range c.All {
		m[k] = v
	}
	for k, v := range c.Only[id] {
		m[k] = v
	}
	return m
}

// Text is a statement's text as the rules read it.
func (c Corrector) Text(id int64, text string) string { return c.For(id).Canonical(text) }

// Fix applies the rules to a statement for display, keeping what was
// written in Original when they change it.
func (c Corrector) Fix(st *Statement) {
	if t := c.Text(st.ID, st.Text); t != st.Text {
		st.Original, st.Text = st.Text, t
	}
}
