// Package registry builds the people, organisations and terms that mentions
// resolve to. It is seeded from reference lists (a curated registry.toml,
// participants' email addresses) because matching against known entities is
// far more reliable than discovering them from text.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Version changes when the way entities or aliases are derived changes.
const Version = "g1"

// Curated is registry.toml: what the owner (or a directory export) knows.
type Curated struct {
	Person []struct {
		Email   string   `toml:"email"`
		Name    string   `toml:"name"`
		Aliases []string `toml:"aliases"`
	} `toml:"person"`
	// Level, on orgs and terms, makes the entity's existence sensitive:
	// a codename or an acquisition target is hidden from narrower scopes
	// even though it is curated.
	Org []struct {
		ID      string   `toml:"id"`
		Name    string   `toml:"name"`
		Domains []string `toml:"domains"`
		Aliases []string `toml:"aliases"`
		Level   string   `toml:"level"`
	} `toml:"org"`
	Term []struct {
		Name    string   `toml:"name"`
		Type    string   `toml:"type"`
		Aliases []string `toml:"aliases"`
		Level   string   `toml:"level"`
	} `toml:"term"`
}

type Registry struct {
	Entities []store.Entity
	Aliases  []store.Alias
	// Hash covers every input, so stages that depend on the registry re-run
	// exactly when it changes.
	Hash string

	owner    string
	byEmail  map[string]string
	byName   map[string]string // normalised full name → entity, unique names only
	orgByDom map[string]string
	ents     map[string]*store.Entity
	internal []string
}

var freeMail = map[string]bool{"gmail.com": true, "outlook.com": true, "hotmail.com": true, "yahoo.com": true, "icloud.com": true, "me.com": true, "proton.me": true}

// Build derives the registry from config, the curated file (may be absent)
// and every item's participants.
func Build(cfg config.Config, curatedPath string, items []item.Item) (*Registry, error) {
	var cur Curated
	raw, err := os.ReadFile(curatedPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(raw) > 0 {
		if err := toml.Unmarshal(raw, &cur); err != nil {
			return nil, err
		}
	}
	r := &Registry{byEmail: map[string]string{}, byName: map[string]string{}, orgByDom: map[string]string{}, ents: map[string]*store.Entity{}}
	for _, d := range cfg.Owner.InternalDomains {
		r.internal = append(r.internal, strings.ToLower(d))
	}
	h := sha256.New()
	h.Write([]byte(Version))
	h.Write(raw)
	h.Write([]byte(cfg.Owner.Email + "|" + cfg.Owner.Name + "|" + strings.Join(r.internal, ",")))

	aliasSeen := map[[2]string]bool{}
	alias := func(a, id, kind string) {
		a = store.NormalizeAlias(a)
		if a == "" || aliasSeen[[2]string{a, id}] {
			return
		}
		aliasSeen[[2]string{a, id}] = true
		r.Aliases = append(r.Aliases, store.Alias{Alias: a, EntityID: id, Kind: kind})
	}
	addEnt := func(id, typ, name, origin string) *store.Entity {
		if e := r.ents[id]; e != nil {
			return e
		}
		e := &store.Entity{ID: id, Type: typ, Name: name, Origin: origin}
		r.ents[id] = e
		return e
	}
	person := func(email, name, origin string) string {
		email = strings.ToLower(strings.TrimSpace(email))
		id := "person:" + email
		e := r.ents[id]
		if e == nil {
			if name == "" {
				name = nameFromEmail(email)
			}
			e = addEnt(id, "person", name, origin)
			r.byEmail[email] = id
		} else if origin == "registry" {
			e.Name, e.Origin = name, origin
		}
		return id
	}

	// Curated first: its names and origins win.
	for _, p := range cur.Person {
		id := person(p.Email, p.Name, "registry")
		for _, a := range p.Aliases {
			alias(a, id, "full")
		}
	}
	for _, o := range cur.Org {
		id := "org:" + o.ID
		lvl, err := parseLevel(o.Level)
		if err != nil {
			return nil, err
		}
		addEnt(id, "org", o.Name, "registry").Level = lvl
		alias(o.Name, id, "full")
		for _, a := range o.Aliases {
			alias(a, id, "full")
		}
		for _, d := range o.Domains {
			r.orgByDom[strings.ToLower(d)] = id
		}
	}
	for _, t := range cur.Term {
		typ := t.Type
		if typ == "" {
			typ = "term"
		}
		id := "term:" + slug(t.Name)
		lvl, err := parseLevel(t.Level)
		if err != nil {
			return nil, err
		}
		addEnt(id, typ, t.Name, "registry").Level = lvl
		alias(t.Name, id, "full")
		for _, a := range t.Aliases {
			alias(a, id, "full")
		}
	}
	if cfg.Owner.Email != "" {
		r.owner = person(cfg.Owner.Email, cfg.Owner.Name, "registry")
	}

	// Participants with email addresses become directory people; their
	// domains, if external, become organisations.
	var participantKeys []string
	for _, it := range items {
		for _, p := range it.Participants {
			participantKeys = append(participantKeys, p.Email+"|"+p.Name)
			if p.Email == "" {
				continue
			}
			person(p.Email, p.Name, "directory")
			dom := domainOf(p.Email)
			if dom == "" || r.isInternal(dom) || freeMail[dom] || r.orgByDom[dom] != "" {
				continue
			}
			id := "org:" + dom
			addEnt(id, "org", orgName(dom), "domain")
			r.orgByDom[dom] = id
			alias(strings.Split(dom, ".")[0], id, "domain")
		}
	}
	sort.Strings(participantKeys)
	h.Write([]byte(strings.Join(participantKeys, "\n")))

	// Person aliases, then the unique-name index speakers resolve through.
	// Each key maps to the set of people it could mean; only keys meaning
	// exactly one person are used to join name-only speakers.
	nameIDs := map[string]map[string]bool{}
	note := func(k, id string) {
		if nameIDs[k] == nil {
			nameIDs[k] = map[string]bool{}
		}
		nameIDs[k][id] = true
	}
	for id, e := range r.ents {
		if e.Type != "person" {
			continue
		}
		full := store.NormalizeAlias(e.Name)
		note(full, id)
		note("#"+strings.ReplaceAll(full, " ", ""), id)
		if local := strings.Split(strings.TrimPrefix(id, "person:"), "@")[0]; local != "" {
			note("#"+strings.Map(keepAlnum, local), id)
		}
		alias(e.Name, id, "full")
		if split := splitCamel(e.Name); split != e.Name {
			alias(split, id, "full")
		}
		parts := strings.Fields(full)
		if len(parts) >= 2 {
			alias(parts[0], id, "first")
			if len(parts[len(parts)-1]) >= 3 {
				alias(parts[len(parts)-1], id, "last")
			}
		}
	}
	for k, ids := range nameIDs {
		if len(ids) == 1 {
			for id := range ids {
				r.byName[k] = id
			}
		}
	}

	// Tomoe speakers carry only a name. Join them to a directory person when
	// the name matches uniquely; otherwise they stay name-only people.
	for _, it := range items {
		for _, p := range it.Participants {
			if p.Email != "" || p.Owner || p.Name == "" {
				continue
			}
			if r.lookupName(p.Name) != "" {
				continue
			}
			id := "person:name:" + store.NormalizeAlias(p.Name)
			if r.ents[id] == nil {
				addEnt(id, "person", p.Name, "speaker")
				alias(p.Name, id, "full")
				parts := strings.Fields(store.NormalizeAlias(p.Name))
				if len(parts) >= 2 {
					alias(parts[0], id, "first")
					if len(parts[len(parts)-1]) >= 3 {
						alias(parts[len(parts)-1], id, "last")
					}
				}
			}
		}
	}

	for _, e := range r.ents {
		r.Entities = append(r.Entities, *e)
	}
	sort.Slice(r.Entities, func(i, j int) bool { return r.Entities[i].ID < r.Entities[j].ID })
	sort.Slice(r.Aliases, func(i, j int) bool {
		a, b := r.Aliases[i], r.Aliases[j]
		if a.Alias != b.Alias {
			return a.Alias < b.Alias
		}
		return a.EntityID < b.EntityID
	})
	r.Hash = hex.EncodeToString(h.Sum(nil)[:16])
	return r, nil
}

func (r *Registry) lookupName(name string) string {
	full := store.NormalizeAlias(name)
	if id := r.byName[full]; id != "" {
		return id
	}
	return r.byName["#"+strings.ReplaceAll(full, " ", "")]
}

// Owner is the owner's entity id ("" if not configured).
func (r *Registry) Owner() string { return r.owner }

// OwnerPrincipal is the owner's audience principal.
func (r *Registry) OwnerPrincipal() string {
	return r.Principal(r.owner)
}

// Participant resolves a participant to an entity id ("" if unknown).
func (r *Registry) Participant(p item.Participant) string {
	switch {
	case p.Owner:
		return r.owner
	case p.Email != "":
		return r.byEmail[strings.ToLower(p.Email)]
	case p.Name != "":
		if id := r.lookupName(p.Name); id != "" {
			return id
		}
		id := "person:name:" + store.NormalizeAlias(p.Name)
		if r.ents[id] != nil {
			return id
		}
	}
	return ""
}

// Principal is the audience form of an entity: its email, or name:<name>.
func (r *Registry) Principal(entityID string) string {
	if strings.HasPrefix(entityID, "person:name:") {
		return sensitivity.NormalizePrincipal("name:" + strings.TrimPrefix(entityID, "person:name:"))
	}
	if strings.HasPrefix(entityID, "person:") {
		return sensitivity.NormalizePrincipal(strings.TrimPrefix(entityID, "person:"))
	}
	return ""
}

// OrgFor returns the organisation an email address belongs to, if external.
func (r *Registry) OrgFor(email string) string { return r.orgByDom[domainOf(email)] }

func (r *Registry) isInternal(dom string) bool {
	for _, d := range r.internal {
		if dom == d || strings.HasSuffix(dom, "."+d) {
			return true
		}
	}
	return false
}

func domainOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// nameFromEmail guesses a display name: "dana.reyes@x" → "Dana Reyes".
func nameFromEmail(email string) string {
	local := strings.Split(email, "@")[0]
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// splitCamel turns "NguyenNgoc" into "Nguyen Ngoc", for names derived from
// email addresses that ran words together.
func splitCamel(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, c := range rs {
		if i > 0 && unicode.IsUpper(c) && unicode.IsLower(rs[i-1]) {
			b.WriteRune(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func orgName(dom string) string {
	n := strings.Split(dom, ".")[0]
	return strings.ToUpper(n[:1]) + n[1:]
}

func slug(s string) string { return strings.ReplaceAll(store.NormalizeAlias(s), " ", "-") }

func keepAlnum(r rune) rune {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return unicode.ToLower(r)
	}
	return -1
}

func parseLevel(s string) (sensitivity.Level, error) {
	if s == "" {
		return sensitivity.Public, nil
	}
	return sensitivity.ParseLevel(s)
}
