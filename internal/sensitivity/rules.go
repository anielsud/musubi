package sensitivity

import (
	"fmt"
	"regexp"
	"strings"
)

// RulesVersion changes whenever rule behavior changes, so the classify stage
// re-runs everything it labelled with older rules.
const RulesVersion = "r3"

// ItemFacts is what the item-level rules look at.
type ItemFacts struct {
	Kind  string // meeting, email, chat, event
	Title string
	// Native holds sensitivity markings from the source: "sensitivity"
	// (calendar private/confidential or a Purview label name), "showAs".
	Native map[string]string
	// Audience is every principal party to the item, owner included.
	Audience []string
	// UnknownParticipants is true when people were present whom we could not
	// identify (a Tomoe "Person 3" with no matched calendar event).
	UnknownParticipants bool
	// Broadcast is true for mail to distribution lists or very wide audiences.
	Broadcast       bool
	InternalDomains []string
	// InternalMinAudience is the audience size from which an all-internal
	// group is labelled Internal rather than Confidential. Group size is a
	// weak signal (an executive team meeting is a group), so the default
	// keeps every non-broadcast conversation Confidential.
	InternalMinAudience int
}

// ClassifyItem assigns the item-level label. When nothing determines the
// level it fails closed at Confidential.
func ClassifyItem(f ItemFacts) Label {
	if l, ok := nativeLabel(f.Native); ok {
		l.Categories |= matchCategories(f.Title)
		return l
	}
	l := Label{Origin: OriginRule, Confidence: 0.7}
	external := externalCount(f.Audience, f.InternalDomains)
	switch {
	case f.UnknownParticipants:
		l.Level, l.Reason, l.Confidence = Confidential, "participants not all identified", 0.5
	case f.Broadcast:
		l.Level, l.Reason = Internal, "broadcast to a wide internal audience"
		if external > 0 {
			l.Level, l.Reason = Confidential, "broadcast including external recipients"
		}
	case external > 0:
		l.Level, l.Reason = Confidential, fmt.Sprintf("%d external participant(s)", external)
	case len(f.Audience) <= 2:
		l.Level, l.Reason = Confidential, "one-to-one"
	case f.InternalMinAudience > 0 && len(f.Audience) >= f.InternalMinAudience:
		l.Level, l.Reason = Internal, fmt.Sprintf("internal group of %d", len(f.Audience))
	default:
		l.Level, l.Reason = Confidential, fmt.Sprintf("working group of %d", len(f.Audience))
	}
	if c := matchCategories(f.Title); c != 0 {
		l.Categories |= c
		l.Level = maxLevel(l.Level, levelFor(c))
		l.Reason += "; title: " + c.String()
	}
	return l
}

// ClassifyText returns the escalation a passage of text earns on its own
// (categories found and the level they imply), or ok=false if none.
func ClassifyText(text string) (Label, bool) {
	var c Categories
	var hits []string
	for _, r := range textRules {
		if m := r.match(text); m != "" {
			c |= r.cat
			hits = append(hits, r.cat.String()+":"+m)
		}
	}
	if c == 0 {
		return Label{}, false
	}
	return Label{Level: levelFor(c), Categories: c, Origin: OriginRule, Confidence: 0.6, Reason: strings.Join(hits, "; ")}, true
}

func nativeLabel(n map[string]string) (Label, bool) {
	s := strings.ToLower(strings.TrimSpace(n["sensitivity"]))
	l := Label{Origin: OriginNative, Confidence: 0.95, Reason: "source marked " + s}
	switch {
	case s == "":
		return Label{}, false
	case strings.Contains(s, "highly confidential") || strings.Contains(s, "restricted"):
		l.Level = Restricted
	case s == "private" || s == "personal" || strings.Contains(s, "confidential"):
		l.Level = Confidential
	case s == "general" || s == "internal" || s == "normal":
		// "normal" is Graph's default, which says nothing; let the rules decide.
		if s == "normal" {
			return Label{}, false
		}
		l.Level = Internal
	case s == "public":
		l.Level = Public
	default:
		return Label{}, false
	}
	return l, true
}

func externalCount(audience, internal []string) int {
	n := 0
	for _, p := range audience {
		at := strings.LastIndex(p, "@")
		if at < 0 {
			continue // name-only principals: unknown domain, counted by UnknownParticipants
		}
		if !InternalDomain(p[at+1:], internal) {
			n++
		}
	}
	return n
}

// levelFor is the minimum level a set of categories implies. Personal
// matters (HR, pay, health, legal) and strategy go to Restricted; commercial
// and security detail to Confidential.
func levelFor(c Categories) Level {
	if c&(HR|Compensation|Health|Legal|Strategy) != 0 {
		return Restricted
	}
	if c != 0 {
		return Confidential
	}
	return Public
}

func maxLevel(a, b Level) Level {
	if a > b {
		return a
	}
	return b
}

func matchCategories(text string) Categories {
	var c Categories
	for _, r := range textRules {
		if r.match(text) != "" {
			c |= r.cat
		}
	}
	return c
}

type textRule struct {
	cat Categories
	re  *regexp.Regexp
	// not, when set, discards a match whose surrounding text matches it, e.g.
	// "customer acquisition" is marketing, not M&A.
	not *regexp.Regexp
}

func (r textRule) match(text string) string {
	for _, loc := range r.re.FindAllStringIndex(text, -1) {
		if r.not != nil {
			lo, hi := max(0, loc[0]-24), min(len(text), loc[1]+24)
			if r.not.MatchString(text[lo:hi]) {
				continue
			}
		}
		return text[loc[0]:loc[1]]
	}
	return ""
}

func rx(s string) *regexp.Regexp { return regexp.MustCompile(`(?i)\b(?:` + s + `)\b`) }

// textRules favor recall: missing a sensitive passage is worse than
// over-labelling one, and the owner still sees everything. Precision is
// measured by the eval harness on real data before rules get looser.
var textRules = []textRule{
	{cat: Compensation, re: rx(`salary|salaries|pay raise|merit increase|bonus(?:es)? (?:payout|target|pool)|equity (?:grant|refresh)|stock options|RSUs?|comp(?:ensation)? (?:band|review|package|adjustment|increase)|base pay|pay band|offer letter|raise to \$?\d[\d,.]*k?`)},
	{cat: HR, re: regexp.MustCompile(`\bPIP\b`)},
	{cat: HR, re: rx(`performance (?:review|improvement plan|issue|concern)s?|disciplinary|terminat(?:e|ed|ing|ion) (?:him|her|them|employment|his|her|their)|let (?:him|her|them) go|laid off|layoffs?|RIF|reduction in force|harassment|grievance|resign(?:ed|ation)|notice period|promotion case|HR (?:complaint|investigation|case)`)},
	{cat: Legal, re: rx(`litigation|lawsuit|sued|subpoena|legal hold|attorney[- ]client|privileged and confidential|outside counsel|settlement agreement|cease and desist`)},
	{cat: Health, re: rx(`medical leave|sick leave|diagnos(?:is|ed)|surgery|maternity leave|paternity leave|mental health|disability|FMLA`)},
	{cat: Strategy, re: rx(`acqui(?:re|ring|sition)s?|M&A|merger|due diligence|term sheet|board (?:meeting|deck)|reorg(?:anization)?|codename`), not: rx(`customer|user|lead|talent|data|traffic|new logo|client`)},
	{cat: DealTerms, re: rx(`\d+ ?(?:%|percent) (?:discount|off|uplift)|discount of|price increase|uplift|contract value|ACV|TCV|renewal price|list price|net price|multi-year (?:deal|commit(?:ment)?)|payment terms|order form`)},
	{cat: Financial, re: rx(`revenue (?:forecast|target|miss)|EBITDA|burn rate|bookings|quota attainment|pipeline coverage|budget cuts?|earnings|guidance`)},
	{cat: Security, re: rx(`vulnerabilit(?:y|ies)|CVE-\d+|security incident|data breach|exploit(?:ed)?|pen ?test|credentials? (?:leak|leaked|exposed)|API key|secret key|password`)},
	{cat: CustomerPII, re: rx(`social security number|SSN|date of birth|credit card number|home address|passport number|\d{3}[-. ]\d{3}[-. ]\d{4}`)},
}
