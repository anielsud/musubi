// Package sensitivity defines labels (level, categories, audience), the
// scopes every read must declare, and the rules that assign labels.
//
// The store enforces scopes in SQL; Visible here is an independent Go
// implementation of the same rule so the eval harness can re-check every
// result the store returns. Keep the two in step.
package sensitivity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Level orders how widely something may be shown. Higher is narrower.
type Level int

const (
	Public Level = iota
	Internal
	Confidential
	Restricted
)

var levelNames = []string{"public", "internal", "confidential", "restricted"}

func (l Level) String() string {
	if l < 0 || int(l) >= len(levelNames) {
		return fmt.Sprintf("level(%d)", int(l))
	}
	return levelNames[l]
}

func ParseLevel(s string) (Level, error) {
	for i, n := range levelNames {
		if strings.EqualFold(s, n) {
			return Level(i), nil
		}
	}
	return 0, fmt.Errorf("unknown sensitivity level %q", s)
}

// Categories is a bit set. A category restricts an item whatever its level:
// a scope must allow every category an item carries.
type Categories uint32

const (
	HR Categories = 1 << iota
	Compensation
	Legal
	Security
	Financial
	DealTerms
	CustomerPII
	Health
	Strategy
	// Personal is someone's life outside work (family, travel, where they
	// are from), kept because it helps, but shown only to the owner.
	Personal
)

// AllCategories is every defined category.
const AllCategories = HR | Compensation | Legal | Security | Financial | DealTerms | CustomerPII | Health | Strategy | Personal

var categoryNames = map[Categories]string{
	HR: "hr", Compensation: "compensation", Legal: "legal", Security: "security",
	Financial: "financial", DealTerms: "deal-terms", CustomerPII: "customer-pii",
	Health: "health", Strategy: "strategy", Personal: "personal",
}

func (c Categories) Names() []string {
	var out []string
	for bit, n := range categoryNames {
		if c&bit != 0 {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (c Categories) String() string {
	if c == 0 {
		return "-"
	}
	return strings.Join(c.Names(), ",")
}

func ParseCategories(names []string) (Categories, error) {
	var c Categories
	for _, n := range names {
		found := false
		for bit, name := range categoryNames {
			if strings.EqualFold(n, name) {
				c |= bit
				found = true
			}
		}
		if !found {
			return 0, fmt.Errorf("unknown sensitivity category %q", n)
		}
	}
	return c, nil
}

// Origin says who assigned a label. Higher origins win: a user override beats
// a source's native marking, which beats a rule, which beats a model guess.
type Origin string

const (
	OriginModel  Origin = "model"
	OriginRule   Origin = "rule"
	OriginNative Origin = "native"
	OriginUser   Origin = "user"
)

// Label is the sensitivity of one item, or the escalation of one chunk
// within it. Audience is held separately (per item) by the store.
type Label struct {
	Level      Level      `json:"level"`
	Categories Categories `json:"categories"`
	Origin     Origin     `json:"origin,omitempty"`
	Confidence float64    `json:"confidence,omitempty"`
	Reason     string     `json:"reason,omitempty"`
}

// MarshalJSON writes the level's name, for the UI.
func (l Level) MarshalJSON() ([]byte, error) { return []byte(`"` + l.String() + `"`), nil }

// MarshalJSON writes category names, for the UI.
func (c Categories) MarshalJSON() ([]byte, error) {
	names := c.Names()
	if names == nil {
		names = []string{}
	}
	return json.Marshal(names)
}

// Combine returns the narrower of two labels: the higher level and the union
// of categories. Used for a chunk's effective label (item label + chunk
// escalation) and for anything derived from several inputs.
func Combine(a, b Label) Label {
	out := a
	if b.Level > out.Level {
		out.Level = b.Level
	}
	out.Categories |= b.Categories
	return out
}
