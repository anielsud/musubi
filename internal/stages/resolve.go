package stages

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/store"
)

const ResolveVersion = "m1"

// Matcher finds alias mentions in text. Built once per registry.
type Matcher struct {
	aliases map[string][]store.Alias
	maxN    int
}

func NewMatcher(as []store.Alias) *Matcher {
	m := &Matcher{aliases: map[string][]store.Alias{}}
	for _, a := range as {
		m.aliases[a.Alias] = append(m.aliases[a.Alias], a)
		if n := len(strings.Fields(a.Alias)); n > m.maxN {
			m.maxN = n
		}
	}
	return m
}

// commonWords are first names that are also ordinary words. They match only
// when capitalised mid-sentence, and never in lower-case (live) text.
var commonWords = map[string]bool{
	"will": true, "mark": true, "may": true, "bill": true, "june": true, "april": true, "grant": true, "chase": true,
	"frank": true, "rich": true, "pat": true, "sue": true, "joy": true, "hope": true, "max": true, "art": true,
	"august": true, "dawn": true, "summer": true, "faith": true, "ray": true, "rob": true, "drew": true, "jack": true,
	"sky": true, "lane": true, "page": true, "long": true, "young": true, "little": true, "new": true, "best": true,
	"day": true, "case": true, "price": true, "rose": true, "wood": true, "white": true, "black": true, "brown": true,
	"green": true, "king": true, "hill": true, "sun": true, "li": true, "ha": true, "an": true, "do": true, "to": true,
}

type word struct {
	norm       string
	start, end int
	upper      bool // first letter upper case
	sentStart  bool
}

func tokenize(s string) []word {
	var out []word
	i := 0
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '&') {
			i += sz
			continue
		}
		j := i
		for j < len(s) {
			r2, sz2 := utf8.DecodeRuneInString(s[j:])
			if unicode.IsLetter(r2) || unicode.IsDigit(r2) || r2 == '&' || (r2 == '\'' && j+sz2 < len(s)) {
				j += sz2
				continue
			}
			break
		}
		raw := s[i:j]
		norm := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(raw, "'s"), "'"))
		before := strings.TrimRight(s[:i], " \t")
		sent := before == "" || strings.HasSuffix(before, ".") || strings.HasSuffix(before, "?") || strings.HasSuffix(before, "!") || strings.HasSuffix(before, ":") || strings.HasSuffix(before, "\n")
		out = append(out, word{norm: norm, start: i, end: j, upper: unicode.IsUpper(r), sentStart: sent})
		i = j
	}
	return out
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// Candidate is a mention before disambiguation.
type Candidate struct {
	Surface    string
	Start, End int
	Aliases    []store.Alias
}

// Find returns longest-match alias mentions in text.
func (m *Matcher) Find(text string) []Candidate {
	ws := tokenize(text)
	cased := hasUpper(text)
	var out []Candidate
	for i := 0; i < len(ws); {
		matched := 0
		for n := min(m.maxN, len(ws)-i); n >= 1; n-- {
			parts := make([]string, n)
			for k := 0; k < n; k++ {
				parts[k] = ws[i+k].norm
			}
			as := m.aliases[strings.Join(parts, " ")]
			if len(as) == 0 {
				continue
			}
			if n == 1 {
				as = filterSingle(as, ws[i], cased)
				if len(as) == 0 {
					continue
				}
			}
			out = append(out, Candidate{Surface: text[ws[i].start:ws[i+n-1].end], Start: ws[i].start, End: ws[i+n-1].end, Aliases: as})
			matched = n
			break
		}
		if matched == 0 {
			matched = 1
		}
		i += matched
	}
	return out
}

// filterSingle applies the case rules to one-word matches of person names:
// in cased text they must be capitalised; common words must also not start a
// sentence; in lower-case text common words never match.
func filterSingle(as []store.Alias, w word, cased bool) []store.Alias {
	var keep []store.Alias
	for _, a := range as {
		if a.Kind == "first" || a.Kind == "last" || a.Kind == "full" {
			if cased && !w.upper {
				continue
			}
			if commonWords[w.norm] && (!cased || w.sentStart) {
				continue
			}
		}
		keep = append(keep, a)
	}
	return keep
}

var kindRank = map[string]int{"full": 4, "domain": 3, "last": 2, "first": 1}

// Resolve finds mentions in an item's chunks and the entities taking part
// in it. Ambiguous mentions are kept with their candidates and no entity.
func Resolve(it item.Item, linked *item.Item, chunks []store.Chunk, reg *registry.Registry, m *Matcher) ([]store.Mention, []store.ItemEntity) {
	parts := map[string]bool{}
	var ies []store.ItemEntity
	seenIE := map[[2]string]bool{}
	addIE := func(id, role string) {
		if id == "" || seenIE[[2]string{id, role}] {
			return
		}
		seenIE[[2]string{id, role}] = true
		parts[id] = true
		ies = append(ies, store.ItemEntity{EntityID: id, Role: role})
	}
	addParts := func(ps []item.Participant) {
		for _, p := range ps {
			id := reg.Participant(p)
			addIE(id, p.Role)
			if p.Email != "" {
				addIE(reg.OrgFor(p.Email), "org")
			}
		}
	}
	addParts(it.Participants)
	if linked != nil {
		addParts(linked.Participants)
	}

	seqOf := map[int]int{}
	for _, c := range chunks {
		for s := c.SegFrom; s < c.SegTo; s++ {
			seqOf[s] = c.Seq
		}
	}
	var out []store.Mention
	for si, seg := range it.Segments {
		seq, ok := seqOf[si]
		if !ok {
			continue
		}
		for _, c := range m.Find(seg.Text) {
			out = append(out, pick(c, seq, si, parts))
		}
	}
	return out, ies
}

func pick(c Candidate, seq, seg int, parts map[string]bool) store.Mention {
	type opt struct {
		id    string
		score int
		kind  string
	}
	best := map[string]opt{}
	for _, a := range c.Aliases {
		s := kindRank[a.Kind]
		if parts[a.EntityID] {
			s += 10
		}
		if o, ok := best[a.EntityID]; !ok || s > o.score {
			best[a.EntityID] = opt{a.EntityID, s, a.Kind}
		}
	}
	opts := make([]opt, 0, len(best))
	for _, o := range best {
		opts = append(opts, o)
	}
	sort.Slice(opts, func(i, j int) bool {
		if opts[i].score != opts[j].score {
			return opts[i].score > opts[j].score
		}
		return opts[i].id < opts[j].id
	})
	m := store.Mention{ChunkSeq: seq, Surface: c.Surface, Seg: seg, Start: c.Start, End: c.End}
	if len(opts) > 1 && opts[0].score == opts[1].score {
		for _, o := range opts {
			if o.score == opts[0].score {
				m.Candidates = append(m.Candidates, o.id)
			}
		}
		return m
	}
	top := opts[0]
	m.EntityID = top.id
	switch {
	case top.kind == "full" || top.kind == "domain":
		m.Confidence = 0.95
	case parts[top.id]:
		m.Confidence = 0.85
	default:
		m.Confidence = 0.6
	}
	return m
}
