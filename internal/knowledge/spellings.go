package knowledge

import (
	"regexp"
	"strings"
)

// Spellings are confirmed spelling aliases (variant → kept form, the
// variant lower-cased: store.ConfirmedSpellings). They are applied when
// reading, for matching only: stored text is never rewritten
// (docs/incremental-design.md, "Entities and aliases").
type Spellings map[string]string

var wordRe = regexp.MustCompile(`[\pL\pN']+`)

// Canonical rewrites the whole-word variants in s to their kept forms.
func (sp Spellings) Canonical(s string) string {
	if len(sp) == 0 {
		return s
	}
	return wordRe.ReplaceAllStringFunc(s, func(w string) string {
		if k, ok := sp[strings.ToLower(w)]; ok {
			return k
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
