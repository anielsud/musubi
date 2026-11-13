package sensitivity

import (
	"fmt"
	"strings"
)

// Share is a plain verdict on who may hear something, for a consumer that
// sees everything (the owner's own assistant) but may be writing for
// others. Narrower verdicts sort later.
type Share string

const (
	SharePublic   Share = "public"
	ShareInternal Share = "internal"
	ShareParty    Share = "party-only"
	ShareOwner    Share = "owner-only"
)

var shareRank = map[Share]int{SharePublic: 0, ShareInternal: 1, ShareParty: 2, ShareOwner: 3}

// Narrower reports whether a is narrower than b.
func (a Share) Narrower(b Share) bool { return shareRank[a] > shareRank[b] }

// ShareOf gives a label's verdict. It follows ReplyTo and Visible, so the
// verdict and "fits" never disagree: personal details stay out of anything
// drafted for others; anything else confidential, restricted or
// categorised may go to the people who were party to it, never further.
func ShareOf(l Label) Share {
	switch {
	case l.Categories&Personal != 0:
		return ShareOwner
	case l.Categories != 0 || l.Level >= Confidential:
		return ShareParty
	case l.Level == Internal:
		return ShareInternal
	}
	return SharePublic
}

// ParseShare reads a verdict's name.
func ParseShare(s string) (Share, error) {
	sh := Share(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := shareRank[sh]; !ok {
		return "", fmt.Errorf("unknown share %q (public, internal, party-only, owner-only)", s)
	}
	return sh, nil
}

// UpTo is the scope that returns everything whose verdict is sh or wider:
// owner-only is the owner's scope, which hides nothing.
func UpTo(sh Share) Scope {
	switch sh {
	case SharePublic:
		return Scope{Name: "up-to:public", MaxLevel: Public}
	case ShareInternal:
		return Scope{Name: "up-to:internal", MaxLevel: Internal}
	case ShareParty:
		return Scope{Name: "up-to:party-only", MaxLevel: Restricted, Allowed: AllCategories &^ Personal}
	}
	return Owner()
}

// noOne stands for an audience party to nothing, so only open material
// fits it.
const noOne = "\x00no-one"

// ForAudience is the scope of something written for the given recipients:
// email addresses, "internal" (colleagues not named) or "external" (anyone
// outside the company). Addresses use ReplyTo. "internal" or "external"
// alongside addresses means people party to nothing read it too, so only
// open material fits: internal for colleagues, public for outsiders.
func ForAudience(internal []string, recipients []string) (Scope, error) {
	var addrs []string
	external, colleague := false, false
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		switch strings.ToLower(r) {
		case "":
		case "internal":
			colleague = true
		case "external":
			external = true
		default:
			if !strings.Contains(r, "@") {
				return Scope{}, fmt.Errorf("recipient %q: give an email address, \"internal\" or \"external\"", r)
			}
			addrs = append(addrs, r)
		}
	}
	if len(addrs) > 0 {
		sc := ReplyTo(internal, addrs...)
		if external {
			sc.Open = Public
		}
		if external || colleague {
			sc.Audience = append(sc.Audience, noOne)
		}
		return sc, nil
	}
	if !external && !colleague {
		return Scope{}, fmt.Errorf("no recipients given")
	}
	open := Internal
	if external {
		open = Public
	}
	return Scope{Name: "for:" + open.String(), MaxLevel: Restricted, Allowed: AllCategories &^ Personal, Audience: []string{noOne}, Open: open}, nil
}

// Why says in a few words why a label is as narrow as it is, for a reader
// deciding what to pass on (the MCP's results, the phone's briefs). party
// is how many people were party to the source; 0 for something drawn
// from many.
func Why(lab Label, party int) string {
	var parts []string
	if lab.Categories != 0 {
		parts = append(parts, "about "+strings.Join(lab.Categories.Names(), ", "))
	}
	switch {
	case lab.Level == Restricted:
		parts = append(parts, "restricted: take care even with people who were party to it")
	case lab.Level == Confidential && party == 2:
		parts = append(parts, "a one-to-one")
	case lab.Level == Confidential && party > 0:
		parts = append(parts, fmt.Sprintf("confidential source with %d people party to it", party))
	case lab.Level == Confidential:
		parts = append(parts, "drawn from confidential sources")
	case len(parts) == 0:
		parts = append(parts, lab.Level.String()+" source")
	}
	return strings.Join(parts, "; ")
}
