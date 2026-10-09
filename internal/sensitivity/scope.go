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
	// plus open material (Open or below with no categories). This is how a
	// drafted reply to someone avoids repeating what they weren't told.
	Audience []string
	// Open is the highest level shown to an audience that wasn't party to
	// the source: Internal for colleagues, Public once anyone outside the
	// company is in the audience.
	Open Level
}

// Owner sees everything. Used by the owner's own explorer and by audits.
func Owner() Scope {
	return Scope{Name: "owner", MaxLevel: Restricted, Allowed: AllCategories}
}

// SharedScreen is for the side panel while the owner's screen is visible to
// others: nothing above internal, nothing categorised.
func SharedScreen() Scope {
	return Scope{Name: "shared-screen", MaxLevel: Internal}
}

// ReplyTo is for text drafted to the given people. internal lists the
// company's email domains; if any recipient is outside them, only public
// material is open to them.
func ReplyTo(internal []string, principals ...string) Scope {
	aud := make([]string, len(principals))
	open := Internal
	for i, p := range principals {
		aud[i] = NormalizePrincipal(p)
		if !isInternalPrincipal(aud[i], internal) {
			open = Public
		}
	}
	return Scope{Name: "reply:" + strings.Join(aud, ","), MaxLevel: Restricted, Allowed: AllCategories, Audience: aud, Open: open}
}

// isInternalPrincipal: name-only principals are unknown, so treated as external.
func isInternalPrincipal(p string, internal []string) bool {
	at := strings.LastIndex(p, "@")
	if at < 0 {
		return false
	}
	return InternalDomain(p[at+1:], internal)
}

// InternalDomain reports whether dom belongs to the company, including its
// other country domains: if x.com is internal, so are x.no and x.co.uk, but
// not x.agency.com, which anyone could register.
func InternalDomain(dom string, internal []string) bool {
	dom = strings.ToLower(dom)
	for _, d := range internal {
		d = strings.ToLower(d)
		if dom == d || strings.HasSuffix(dom, "."+d) || sibling(dom, d) {
			return true
		}
	}
	return false
}

func sibling(dom, d string) bool {
	dl, il := strings.Split(dom, "."), strings.Split(d, ".")
	if len(dl) < 2 || dl[0] != il[0] {
		return false
	}
	for _, l := range dl[1:] {
		if len(l) > 3 {
			return false
		}
	}
	return true
}

// Cloud is the default policy for text sent to a cloud provider.
func Cloud() Scope {
	return Scope{Name: "cloud", MaxLevel: Internal}
}

// ParseScope reads the CLI form: owner, shared-screen, cloud, reply:<email>[,<email>].
func ParseScope(s string, internal []string) (Scope, error) {
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
		return ReplyTo(internal, ps...), nil
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
	if l.Level <= s.Open && l.Categories == 0 {
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
