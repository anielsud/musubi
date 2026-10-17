package knowledge

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/anielsud/musubi/internal/store"
)

// CardIndex finds which topic a conversation is on, and which other topics
// belong on its card. Everything in it is learned from the knowledge layer:
// trigger terms are the proper names a topic's claims use and other topics
// mostly don't; companions are small topics that came up inside the same
// stretch of a meeting (a legal deadline raised during a programme review)
// or that share the topic's terms. It is unscoped and never shown: cards
// are read through Views, under a scope.
type CardIndex struct {
	topicClaims map[int64][]int64
	claimTopic  map[int64]int64
	// Claims nearest a query come from the store's code index, re-ranked
	// against their stored centroids: the index holds no vectors itself
	// (they took 1.3 GB at 400k claims).
	st         *store.Store
	terms      map[string][]int64 // lower-case term → topics, strongest first
	display    map[string]string  // lower-case term → as written
	topicTerms map[int64][]string
	companions map[int64][]int64
	host       map[int64]int64 // companion → the largest topic it belongs with
	termClaims map[string][]int64
	termTopics map[string][]int64 // strongest first
	termShare  map[string]float64 // share of the term's claims in its strongest topic
	diffuse    map[int64]bool     // spread over most of a meeting: a catch-all, not a discussion
	claimText  map[int64]string   // lower-case, for exact-phrase lookups
	// Threads (knowledge.grouping = "threads"): a topic is a thread, which
	// says itself what it holds and which of it is current.
	threads bool
	current map[int64]map[int64]bool // thread → claims stating its current position
}

// Anchor is what a card is about: a topic (with the topics raised during
// its discussion), or, for a term whose claims are spread over topics (an
// event people tell stories from), the claims that say the term.
type Anchor struct {
	Topic int64  `json:"topic,omitempty"`
	Term  string `json:"term,omitempty"`
}

func (a Anchor) Key() string {
	if a.Term != "" {
		return "term:" + strings.ToLower(a.Term)
	}
	return fmt.Sprintf("topic:%d", a.Topic)
}

func (a Anchor) IsZero() bool { return a.Topic == 0 && a.Term == "" }

// Term rules. A term needs minTermClaims claims in its topic, with at least
// minTermShare of all the claims that use it, and must not be so common
// that it names the company or the meeting rather than a subject.
const (
	minTermClaims = 3
	minTermShare  = 0.6
	// minTermCardClaims: a term spread over topics opens its own card only
	// with this many claims.
	minTermCardClaims = 4
	maxTermShare      = 0.05 // of all claims, once there are minClaimsForShare
	// A small corpus can't tell a subject from the company's own name.
	minClaimsForShare = 200
	// A statement is said "during" a topic's discussion when the topic has
	// statements on both sides of it, in the same meeting, at most
	// discussionGap apart.
	discussionGap = 10 * time.Minute
	// maxDiscussionCover: a topic discussed over more than this share of a
	// meeting is a catch-all.
	maxDiscussionCover = 0.4
)

// BuildCardIndex reads the knowledge layer into a card index.
func BuildCardIndex(st *store.Store) (*CardIndex, error) {
	rows, err := st.CardRows()
	if err != nil {
		return nil, err
	}
	aliases, err := st.PersonNameParts()
	if err != nil {
		return nil, err
	}
	people := map[string]bool{}
	for w := range aliases {
		people[w] = true
	}
	for _, r := range rows {
		for _, n := range append([]string{r.Holder}, r.Who...) {
			for _, w := range strings.Fields(n) {
				people[strings.ToLower(strings.Trim(w, "().,"))] = true
			}
		}
	}
	x := &CardIndex{topicClaims: map[int64][]int64{}, claimTopic: map[int64]int64{}, st: st,
		terms: map[string][]int64{}, display: map[string]string{}, topicTerms: map[int64][]string{}, companions: map[int64][]int64{}, host: map[int64]int64{},
		termClaims: map[string][]int64{}, termTopics: map[string][]int64{}, termShare: map[string]float64{}, diffuse: map[int64]bool{},
		claimText: map[int64]string{}}
	seenClaim := map[int64]bool{}
	for _, r := range rows {
		x.claimText[r.ClaimID] += " " + strings.ToLower(r.Text)
	}
	for _, r := range rows {
		if r.TopicID == 0 {
			continue
		}
		if !seenClaim[r.ClaimID] {
			seenClaim[r.ClaimID] = true
			x.topicClaims[r.TopicID] = append(x.topicClaims[r.TopicID], r.ClaimID)
			x.claimTopic[r.ClaimID] = r.TopicID
		}
	}
	members, err := st.ThreadMembers()
	if err != nil {
		return nil, err
	}
	if len(members) > 0 {
		// A thread holds what the threader put in it, including statements
		// it shares with another thread; nothing is inferred around it.
		x.threads = true
		x.current = map[int64]map[int64]bool{}
		x.topicClaims = map[int64][]int64{}
		seen := map[[2]int64]bool{}
		for _, m := range members {
			if !seen[[2]int64{m.Topic, m.Claim}] {
				seen[[2]int64{m.Topic, m.Claim}] = true
				x.topicClaims[m.Topic] = append(x.topicClaims[m.Topic], m.Claim)
			}
			if m.Current {
				if x.current[m.Topic] == nil {
					x.current[m.Topic] = map[int64]bool{}
				}
				x.current[m.Topic][m.Claim] = true
			}
		}
	} else {
		x.learnCompanions(rows)
	}
	x.learnTerms(rows, people)
	return x, nil
}

func (x *CardIndex) learnTerms(rows []store.CardRow, people map[string]bool) {
	words := wordUse(rows)
	// Claims per term, per topic.
	byTerm := map[string]map[int64]map[int64]bool{}
	written := map[string]map[string]int{}
	for _, r := range rows {
		if r.TopicID == 0 {
			continue
		}
		for _, w := range termCandidates(r.Text) {
			l := strings.ToLower(w)
			if people[l] || words.common(l) || words.personLike(l) || nearName(l, people) {
				continue
			}
			if byTerm[l] == nil {
				byTerm[l], written[l] = map[int64]map[int64]bool{}, map[string]int{}
			}
			if byTerm[l][r.TopicID] == nil {
				byTerm[l][r.TopicID] = map[int64]bool{}
			}
			byTerm[l][r.TopicID][r.ClaimID] = true
			written[l][w]++
		}
	}
	nClaims := len(x.claimTopic)
	for term, topics := range byTerm {
		total := 0
		for _, cs := range topics {
			total += len(cs)
		}
		if nClaims >= minClaimsForShare && float64(total) > maxTermShare*float64(nClaims) {
			continue
		}
		type tc struct {
			id int64
			n  int
		}
		var ts []tc
		for id, cs := range topics {
			ts = append(ts, tc{id, len(cs)})
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].n > ts[j].n || ts[i].n == ts[j].n && ts[i].id < ts[j].id })
		claims := map[int64]bool{}
		for _, cs := range topics {
			for c := range cs {
				claims[c] = true
			}
		}
		if len(claims) < minTermClaims {
			continue
		}
		for _, t := range ts {
			x.terms[term] = append(x.terms[term], t.id)
		}
		for c := range claims {
			x.termClaims[term] = append(x.termClaims[term], c)
		}
		sort.Slice(x.termClaims[term], func(i, j int) bool { return x.termClaims[term][i] < x.termClaims[term][j] })
		x.termShare[term] = float64(ts[0].n) / float64(total)
		if !x.anchorsTopic(term) {
			// Spread over topics: a term card, not one topic's. It needs
			// more evidence, and the word must stand on its own rather
			// than end a longer name.
			if len(claims) < minTermCardClaims || 2*words.tail[term] > words.total[term] {
				delete(x.terms, term)
				delete(x.termClaims, term)
				continue
			}
			x.display[term] = displayOf(written[term])
			continue
		}
		best := displayOf(written[term])
		x.display[term] = best
		x.topicTerms[ts[0].id] = append(x.topicTerms[ts[0].id], best)
		// Topics that use the term too are about the same subject.
		for _, t := range ts[1:] {
			if t.n >= 2 {
				x.addCompanion(ts[0].id, t.id)
			}
		}
	}
	for id := range x.topicTerms {
		sort.Strings(x.topicTerms[id])
	}
}

// learnCompanions finds small topics raised during a topic's discussion:
// at least half of their statements were said between two of the topic's
// statements in the same meeting, at most discussionGap apart.
func (x *CardIndex) learnCompanions(rows []store.CardRow) {
	type at struct {
		topic int64
		said  time.Time
	}
	byItem := map[string][]at{}
	for _, r := range rows {
		if r.TopicID != 0 {
			byItem[r.ItemID] = append(byItem[r.ItemID], at{r.TopicID, r.SaidAt})
		}
	}
	size := map[int64]int{}
	for id, cs := range x.topicClaims {
		size[id] = len(cs)
	}
	// A topic whose discussion runs cover most of a meeting is a catch-all
	// ("AI strategy"), not a discussion: it hosts nothing. Measured: requiring
	// two such meetings (so a one-meeting deep dive isn't one) put 13 points
	// fewer card lines on their subject (docs/research-log.md, 2026-10-14).
	wide := map[int64]int{}
	for _, list := range byItem {
		if len(list) < 2 {
			continue
		}
		span := list[len(list)-1].said.Sub(list[0].said)
		if span < 3*discussionGap {
			continue // too short to tell a catch-all from the whole meeting's subject
		}
		byTopic := map[int64][]time.Time{}
		for _, s := range list {
			byTopic[s.topic] = append(byTopic[s.topic], s.said)
		}
		for t, ts := range byTopic {
			covered := time.Duration(0)
			start, last := ts[0], ts[0]
			for _, at := range ts[1:] {
				if at.Sub(last) > discussionGap {
					covered += last.Sub(start)
					start = at
				}
				last = at
			}
			covered += last.Sub(start)
			if float64(covered) > maxDiscussionCover*float64(span) {
				wide[t]++
			}
		}
	}
	for t, n := range wide {
		if n >= 1 {
			x.diffuse[t] = true
		}
	}
	inside := map[[2]int64]int{} // (host, guest) → guest statements inside host's discussion
	total := map[int64]int{}
	for _, list := range byItem {
		for i, s := range list {
			total[s.topic]++
			hosts := map[int64]bool{}
			for j := i - 1; j >= 0 && s.said.Sub(list[j].said) <= discussionGap; j-- {
				h := list[j].topic
				if h == s.topic || hosts[h] || x.diffuse[h] {
					continue
				}
				for k := i + 1; k < len(list) && list[k].said.Sub(list[j].said) <= discussionGap; k++ {
					if list[k].topic == h {
						hosts[h] = true
						break
					}
				}
			}
			for h := range hosts {
				inside[[2]int64{h, s.topic}]++
			}
		}
	}
	for k, n := range inside {
		host, guest := k[0], k[1]
		if size[guest] < size[host] && 2*n >= total[guest] {
			x.addCompanion(host, guest)
		}
	}
}

func (x *CardIndex) addCompanion(host, guest int64) {
	if x.diffuse[host] {
		return
	}
	for _, g := range x.companions[host] {
		if g == guest {
			return
		}
	}
	x.companions[host] = append(x.companions[host], guest)
	if h, ok := x.host[guest]; !ok || len(x.topicClaims[host]) > len(x.topicClaims[h]) {
		x.host[guest] = host
	}
}

// Host is the topic whose card a topic appears on: itself, unless it is a
// small topic raised within a larger one's discussion ("can we use the
// savings for hires" belongs on the programme's card).
func (x *CardIndex) Host(topic int64) int64 {
	if h, ok := x.host[topic]; ok && len(x.topicClaims[h]) > len(x.topicClaims[topic]) {
		return h
	}
	return topic
}

// termCandidates are the capitalised words of a text that could name a
// project, product or programme: not sentence starts, not short acronyms,
// not calendar words.
func termCandidates(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range properNames(text) {
		if len(w) < 4 || seen[w] || calendarWords[strings.ToLower(w)] {
			continue
		}
		if strings.ToUpper(w) == w {
			continue // ARR, GDR, ELT: metrics and teams, said everywhere
		}
		if !unicode.IsUpper([]rune(w)[0]) {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

var calendarWords = map[string]bool{}

func init() {
	for m := range months {
		calendarWords[m] = true
	}
	for d := range weekdays {
		calendarWords[d] = true
	}
}

// TermHit is a trigger term found in text, with the topic it opens.
type TermHit struct {
	Term   string `json:"term"`
	Topic  int64  `json:"topic"`
	Anchor Anchor `json:"anchor"`
}

// TermsIn returns the trigger terms a text says, each with its topic.
func (x *CardIndex) TermsIn(text string) []TermHit {
	if x == nil {
		return nil
	}
	var out []TermHit
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' }) {
		if ts := x.terms[w]; len(ts) > 0 && !seen[w] {
			seen[w] = true
			out = append(out, TermHit{Term: x.display[w], Topic: ts[0], Anchor: x.AnchorFor(w)})
		}
	}
	return out
}

// TopicTerms are a topic's trigger terms.
func (x *CardIndex) TopicTerms(topic int64) []string { return x.topicTerms[topic] }

// Companions are the topics that belong on a topic's card besides itself.
func (x *CardIndex) Companions(topic int64) []int64 { return x.companions[topic] }

// TopicClaims are a topic's claims.
func (x *CardIndex) TopicClaims(topic int64) []int64 { return x.topicClaims[topic] }

// TopicNear votes for the topic of the claims nearest a query vector: the
// topic holding most of the k nearest claims, if it holds at least two of
// them and the nearest is close enough to be about the same thing.
func (x *CardIndex) TopicNear(q []float32, k int, minSim float64) (int64, float64) {
	type hit struct {
		id  int64
		sim float64
	}
	// Candidates by code, compared exactly: well over k, so the exact
	// nearest k are among them.
	ix, err := x.st.ClaimIndex()
	if err != nil {
		return 0, 0
	}
	cents, err := x.st.ClaimCentroidsOf(ix.Nearest(q, max(400, 20*k)))
	if err != nil {
		return 0, 0
	}
	var hs []hit
	for id, c := range cents {
		if x.claimTopic[id] == 0 || len(c) != len(q) {
			continue
		}
		hs = append(hs, hit{id, cos(q, c)})
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].sim > hs[j].sim || hs[i].sim == hs[j].sim && hs[i].id < hs[j].id })
	if len(hs) == 0 || hs[0].sim < minSim {
		return 0, 0
	}
	if len(hs) > k {
		hs = hs[:k]
	}
	votes := map[int64]float64{}
	count := map[int64]int{}
	for _, h := range hs {
		t := x.claimTopic[h.id]
		votes[t] += h.sim
		count[t]++
	}
	best, bv := int64(0), 0.0
	for t, v := range votes {
		if v > bv || v == bv && t < best {
			best, bv = t, v
		}
	}
	if count[best] < 2 && x.claimTopic[hs[0].id] != best {
		return 0, 0
	}
	return best, hs[0].sim
}

// AllTerms lists every trigger term with the topic it opens, for checking
// what the index learned.
func (x *CardIndex) AllTerms() []TermHit {
	var out []TermHit
	for t, ts := range x.terms {
		out = append(out, TermHit{Term: x.display[t], Topic: ts[0], Anchor: x.AnchorFor(t)})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Topic < out[j].Topic || out[i].Topic == out[j].Topic && out[i].Term < out[j].Term
	})
	return out
}

func displayOf(written map[string]int) string {
	best, bn := "", 0
	for w, n := range written {
		if n > bn || n == bn && w < best {
			best, bn = w, n
		}
	}
	return best
}

// anchorsTopic: a term opens its strongest topic's card when that topic
// holds most of its claims and is a discussion, not a catch-all.
func (x *CardIndex) anchorsTopic(term string) bool {
	ts := x.terms[term]
	return len(ts) > 0 && x.termShare[term] >= minTermShare && !x.diffuse[ts[0]]
}

// AnchorFor is the card a term opens.
func (x *CardIndex) AnchorFor(term string) Anchor {
	l := strings.ToLower(term)
	if x.anchorsTopic(l) {
		return Anchor{Topic: x.Host(x.terms[l][0])}
	}
	return Anchor{Term: x.display[l]}
}

// TermClaims are the claims that say a term.
func (x *CardIndex) TermClaims(term string) []int64 { return x.termClaims[strings.ToLower(term)] }

// usage counts how a word is written across the corpus: in lower case
// (an ordinary word: "project", "cloud") and right before a speech verb
// ("Jo said": a person the directory doesn't know).
type usage struct {
	lower, total, speaker, tail map[string]int
}

var speechVerbs = map[string]bool{"said": true, "says": true, "asked": true, "asks": true, "thinks": true, "noted": true,
	"wants": true, "raised": true, "confirmed": true, "suggested": true, "proposed": true, "mentioned": true, "argued": true,
	"added": true, "explained": true, "agreed": true, "warned": true, "countered": true, "replied": true, "framed": true}

func wordUse(rows []store.CardRow) usage {
	u := usage{map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}}
	for _, r := range rows {
		ws := strings.FieldsFunc(r.Text, func(c rune) bool { return !unicode.IsLetter(c) && c != '\'' })
		for i, w := range ws {
			w = strings.TrimSuffix(w, "'s")
			if w == "" {
				continue // a bare "'s" or "'"
			}
			l := strings.ToLower(w)
			u.total[l]++
			if w == l {
				u.lower[l]++
			}
			if i+1 < len(ws) && capitalised(w) && (speechVerbs[ws[i+1]] || ws[i+1] == "and" && i+2 < len(ws) && capitalised(ws[i+2])) {
				u.speaker[l]++
			}
			if strings.HasSuffix(ws[i], "'s") && i+1 < len(ws) && ws[i+1] == "team" {
				u.speaker[l]++
			}
			// "New York", "San Francisco": the second word of a longer name.
			if i > 0 && capitalised(w) && capitalised(ws[i-1]) {
				u.tail[l]++
			}
		}
	}
	return u
}

func capitalised(w string) bool {
	for _, c := range w {
		return unicode.IsUpper(c)
	}
	return false
}

// common: written in lower case at least a fifth of the time.
func (u usage) common(l string) bool { return u.total[l] > 0 && 5*u.lower[l] >= u.total[l] }

// personLike: seen as the one who said, asked or owns a team.
func (u usage) personLike(l string) bool {
	return u.speaker[l] >= 2 || u.speaker[l] >= 1 && u.total[l] <= 4 || u.total[l] > 0 && 4*u.speaker[l] >= u.total[l]
}

// nearName: one letter off a known name ("Jon" for "John"), or the short
// form of one ("Kate" for "Katherine").
func nearName(l string, people map[string]bool) bool {
	if len(l) < 3 {
		return false
	}
	for p := range people {
		if len(p) > len(l) && strings.HasPrefix(p, l) {
			return true
		}
		if len(l) < 4 {
			continue
		}
		if len(p) != len(l) || p[0] != l[0] {
			continue
		}
		diff := 0
		for k := range p {
			if p[k] != l[k] {
				diff++
			}
		}
		if diff <= 1 {
			return true
		}
	}
	return false
}

// Nearest-claim lookup: the closest claim must be at least CardMinSim
// similar (nomic-embed-text; unrelated statements sit around 0.59), and the
// topic is voted on by the nearest cardVoters.
const (
	CardMinSim = 0.66
	cardVoters = 6
)

// Find returns the card a text opens: a trigger term it says, else (with
// embed) the topic of the claims nearest to it. how says which.
func (x *CardIndex) Find(text string, embed func(string) ([]float32, error)) (a Anchor, how string, err error) {
	if hs := x.TermsIn(text); len(hs) > 0 {
		return hs[0].Anchor, "says " + hs[0].Term, nil
	}
	// The query's own words, said in a few claims, beat its embedding: a
	// short phrase ("brand packs") embeds poorly, and the nearest-claims vote
	// went to another thread (research-log.md, 2026-10-16).
	if t, n := x.phraseTopic(text); t != 0 {
		return Anchor{Topic: x.Host(t)}, fmt.Sprintf("says %q (%d claims)", strings.TrimSpace(text), n), nil
	}
	if embed == nil {
		return Anchor{}, "", nil
	}
	q, err := embed(text)
	if err != nil || q == nil {
		return Anchor{}, "", err
	}
	t, sim := x.TopicNear(q, cardVoters, CardMinSim)
	if t == 0 || x.diffuse[t] {
		return Anchor{}, "", nil
	}
	return Anchor{Topic: x.Host(t)}, fmt.Sprintf("near claims (%.2f)", sim), nil
}

// Current reports whether a claim states a thread's current position;
// always true outside thread mode, where nothing says otherwise.
func (x *CardIndex) Current(topic, claim int64) bool {
	if !x.threads {
		return true
	}
	return x.current[topic][claim]
}

// maxPhraseClaims: a phrase in more claims than this is too common to
// point at one subject.
const maxPhraseClaims = 12

// phraseTopic finds the topic of the claims that contain the whole query
// (two or more content words, in order), when few claims do and most of
// them share a topic.
func (x *CardIndex) phraseTopic(text string) (int64, int) {
	words := contentWords(text)
	if len(words) < 2 || len(words) > 5 {
		return 0, 0
	}
	phrase := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	votes := map[int64]int{}
	n := 0
	for id, t := range x.claimText {
		if !strings.Contains(t, phrase) {
			continue
		}
		n++
		if topic := x.claimTopic[id]; topic != 0 {
			votes[topic]++
		}
	}
	if n == 0 || n > maxPhraseClaims {
		return 0, 0
	}
	best, bv := int64(0), 0
	for t, v := range votes {
		if v > bv || v == bv && t < best {
			best, bv = t, v
		}
	}
	if 2*bv < n || x.diffuse[best] {
		return 0, 0
	}
	return best, n
}
