package stages

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/store"
)

// m3 (2026-11-12): a transcript's named speakers take part as speakers.
const ResolveVersion = "m3"

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
// commonWords are names that are also ordinary English words. As a single
// word they match only when capitalised mid-sentence in cased text, and
// never in lower-case text.
var commonWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`will mark may bill june april grant chase frank rich pat sue joy hope max art august dawn summer faith ray rob drew jack
sky lane page long young little new best day case price rose wood white black brown green king hill sun ha an do to
all per line cat can not and the for but are was one two man kit guy win won dot rod job bud dale dell duke fern
ford gray hall hart holly hunter ivy jade jewel judge lark lily love major marsh mercy miles moon north ocean park
penny pearl rain reed river robin rock rush sage stone storm street strong swift ward wells west wolf woods chip
trip lucky sunny sandy say set sign soon stern teal true wade walker wren wise lion more most many much first last
next well good great free fair happy just less mid mini top bay field glass lake mill moss pool sparks power bright
frost gold noble rice snow star sweet bell bond bush camp carter clay cook cross fox fry hale hay heath hood house
lamb lord meadows mills small spring stamp steel tower turner vine wall waters weeks while wing winter`) {
		m[w] = true
	}
	return m
}()

type word struct {
	norm       string
	start, end int
	upper      bool // first letter upper case
	allCaps    bool // every letter upper case, two or more letters: an acronym
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
		out = append(out, word{norm: norm, start: i, end: j, upper: unicode.IsUpper(r), allCaps: isAllCaps(raw), sentStart: sent})
		i = j
	}
	return out
}

func isAllCaps(s string) bool {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			if !unicode.IsUpper(r) {
				return false
			}
			n++
		}
	}
	return n >= 2
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

// Find returns longest-match alias mentions in text. cased says whether the
// source writes capitals at all (a live transcript may not); it is decided
// per item, since one short segment without capitals proves nothing.
func (m *Matcher) Find(text string, cased bool) []Candidate {
	ws := tokenize(text)
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
			if (a.Kind == "first" || a.Kind == "last") && w.allCaps {
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
func Resolve(it item.Item, linked *item.Item, chunks []store.Chunk, reg *registry.Registry, m *Matcher, prox *Proximity) ([]store.Mention, []store.ItemEntity) {
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
	for _, p := range transcriptSpeakers(it, linked) {
		addIE(reg.Participant(p), "speaker")
	}

	seqOf := map[int]int{}
	for _, c := range chunks {
		for s := c.SegFrom; s < c.SegTo; s++ {
			seqOf[s] = c.Seq
		}
	}
	cased := false
	for _, seg := range it.Segments {
		if hasUpper(seg.Text) {
			cased = true
			break
		}
	}
	others := make([]string, 0, len(parts))
	for id := range parts {
		if id != reg.Owner() {
			others = append(others, id)
		}
	}
	var out []store.Mention
	for si, seg := range it.Segments {
		seq, ok := seqOf[si]
		if !ok {
			continue
		}
		for _, c := range m.Find(seg.Text, cased) {
			out = append(out, pick(c, seq, si, parts, prox, others))
		}
	}
	return out, ies
}

// Proximity counts, for each pair of people, the items both took part in.
// It breaks ties between same-named people: the Mark who keeps meeting this
// meeting's participants is the likelier Mark.
type Proximity struct {
	pairs map[[2]string]int
}

// NewProximity builds pair counts from every item's resolved participants,
// leaving out the owner, who is in everything and would swamp the signal.
// Very large items (all-hands) are skipped for the same reason.
func NewProximity(items []item.Item, reg *registry.Registry) *Proximity {
	p := &Proximity{pairs: map[[2]string]int{}}
	for _, it := range items {
		ids := map[string]bool{}
		for _, pt := range it.Participants {
			if id := reg.Participant(pt); id != "" && id != reg.Owner() {
				ids[id] = true
			}
		}
		if len(ids) > 25 {
			continue
		}
		list := make([]string, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		for i := range list {
			for j := range list {
				if i != j {
					p.pairs[[2]string{list[i], list[j]}]++
				}
			}
		}
	}
	return p
}

func (p *Proximity) score(cand string, others []string) int {
	if p == nil {
		return 0
	}
	n := 0
	for _, o := range others {
		n += p.pairs[[2]string{cand, o}]
	}
	return n
}

func pick(c Candidate, seq, seg int, parts map[string]bool, prox *Proximity, others []string) store.Mention {
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
		// Tie: take the candidate with clearly the most shared meetings with
		// this item's participants (at least two, and twice the runner-up).
		var tied []opt
		for _, o := range opts {
			if o.score == opts[0].score {
				tied = append(tied, o)
			}
		}
		best, second, bestID := 0, 0, ""
		for _, o := range tied {
			switch n := prox.score(o.id, others); {
			case n > best:
				best, second, bestID = n, best, o.id
			case n > second:
				second = n
			}
		}
		if best >= 2 && best >= 2*second {
			m.EntityID, m.Confidence = bestID, 0.55
			return m
		}
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

// placeholderSpeaker is a label that names no one: a recorder's or a
// transcript's "Person 3", "Speaker 2", "Unknown".
var placeholderSpeaker = regexp.MustCompile(`(?i)^(person|speaker|unknown|guest)\s*\d*$`)

// transcriptSpeakers are the people a meeting's lines name, for a meeting
// whose source lists no speakers (a Teams transcript, an imported one):
// the people are the same as in a recording, so they take part as
// speakers too. A name is matched to the meeting's invitee of that name,
// for their email; placeholders and "You" are left out.
func transcriptSpeakers(it item.Item, linked *item.Item) []item.Participant {
	if it.Kind != item.KindMeeting {
		return nil
	}
	for _, p := range it.Participants {
		if p.Role == "speaker" {
			return nil // the source names its speakers itself
		}
	}
	known := map[string]item.Participant{}
	for _, p := range append(append([]item.Participant{}, it.Participants...), participantsOf(linked)...) {
		if p.Name != "" && p.Email != "" {
			known[strings.ToLower(strings.TrimSpace(p.Name))] = p
		}
	}
	var out []item.Participant
	seen := map[string]bool{}
	for _, s := range it.Segments {
		name := strings.TrimSpace(s.Speaker)
		key := strings.ToLower(name)
		if name == "" || seen[key] || key == "you" || placeholderSpeaker.MatchString(name) {
			continue
		}
		seen[key] = true
		p := item.Participant{Name: name, Email: s.Email, Role: "speaker"}
		if k, ok := known[key]; ok && p.Email == "" {
			p.Email = k.Email
		}
		out = append(out, p)
	}
	return out
}

func participantsOf(it *item.Item) []item.Participant {
	if it == nil {
		return nil
	}
	return it.Participants
}
