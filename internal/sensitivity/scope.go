package sensitivity

import (
	"fmt"
	"strings"
)

// Scope describes who will see the result of a read. Every store query takes
// one; there is no unscoped read.
type Scope struct {
	Name string
	// MaxLevel is the highest level that may be returned.
	MaxLevel Level
	// Allowed lists the categories that may be returned. An item carrying any
	// other category is hidden.
	Allowed Categories
	// Audience, when set, limits results to what every member was party to,
	// plus open material (OpenLevel or below with no categories). This is how
	// a drafted reply to someone avoids repeating what they weren't told.
	Audience []string
}

// OpenLevel is the highest level that audience-scoped reads may show to
// people who were not party to the source.
const OpenLevel = Internal

// Owner sees everything. Used by the owner's own explorer and by audits.
func Owner() Scope {
	return Scope{Name: "owner", MaxLevel: Restricted, Allowed: AllCategories}
}

// SharedScreen is for the side panel while the owner's screen is visible to
// others: nothing above internal, nothing categorised.
func SharedScreen() Scope {
	return Scope{Name: "shared-screen", MaxLevel: Internal}
}

// ReplyTo is for text drafted to the given people.
func ReplyTo(principals ...string) Scope {
	aud := make([]string, len(principals))
	for i, p := range principals {
		aud[i] = NormalizePrincipal(p)
	}
	return Scope{Name: "reply:" + strings.Join(aud, ","), MaxLevel: Restricted, Allowed: AllCategories, Audience: aud}
}

// Cloud is the default policy for text sent to a cloud provider.
func Cloud() Scope {
	return Scope{Name: "cloud", MaxLevel: Internal}
}

// ParseScope reads the CLI form: owner, shared-screen, cloud, reply:<email>[,<email>].
func ParseScope(s string) (Scope, error) {
	switch {
	case s == "" || s == "owner":
		return Owner(), nil
	case s == "shared-screen":
		return SharedScreen(), nil
	case s == "cloud":
		return Cloud(), nil
	case strings.HasPrefix(s, "reply:"):
		ps := strings.Split(strings.TrimPrefix(s, "reply:"), ",")
		if len(ps) == 0 || ps[0] == "" {
			return Scope{}, fmt.Errorf("reply scope needs at least one address")
		}
		return ReplyTo(ps...), nil
	}
	return Scope{}, fmt.Errorf("unknown scope %q (owner, shared-screen, cloud, reply:<email>)", s)
}

// NormalizePrincipal gives the form principals are stored and compared in:
// lower-cased email, or "name:<lower-cased name>" when no email is known.
func NormalizePrincipal(p string) string {
	p = strings.TrimSpace(strings.ToLower(p))
	if strings.HasPrefix(p, "name:") || strings.Contains(p, "@") {
		return p
	}
	return "name:" + p
}

// Visible reports whether something with this effective label and audience
// may be shown in scope s. The store's SQL filter must agree with this.
func Visible(l Label, audience []string, s Scope) bool {
	if l.Level > s.MaxLevel {
		return false
	}
	if l.Categories&^s.Allowed != 0 {
		return false
	}
	if len(s.Audience) == 0 {
		return true
	}
	if l.Level <= OpenLevel && l.Categories == 0 {
		return true
	}
	have := make(map[string]bool, len(audience))
	for _, a := range audience {
		have[a] = true
	}
	for _, a := range s.Audience {
		if !have[a] {
			return false
		}
	}
	return true
}
