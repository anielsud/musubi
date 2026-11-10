package knowledge

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// Prep gathers what to bring to a meeting with some people: the subjects
// they are part of and where each stands, what moved since you last met,
// what is open between you, and what was said elsewhere that they likely
// haven't heard. It reads in the owner's scope; what it suggests sharing
// leaves out anything about people's jobs, pay, health, legal matters or
// private life, and anything restricted.
type PrepRequest struct {
	// Template is a past meeting to prepare the next one of: its attendees,
	// and the time it happened as "since".
	Template string `json:"template,omitempty"`
	// People and Since are used when there is no template.
	People []string  `json:"people,omitempty"`
	Since  time.Time `json:"since,omitempty"`
	Title  string    `json:"title,omitempty"`
	// Now is when the meeting is (zero: now); open items are aged to it.
	Now time.Time `json:"now,omitempty"`
	// QuietAfter: an open item last raised longer ago than this, with
	// nothing since, is shown as gone quiet (listed after the live ones)
	// rather than as open. Zero turns it off.
	QuietAfter time.Duration `json:"quietAfter,omitempty"`
	// MarkPrivate marks subjects about people's jobs, pay, health, legal
	// matters or private lives (the sharing rule's threshold), and, in a
	// meeting with more than one other person, lists them last.
	MarkPrivate bool `json:"markPrivate,omitempty"`
	// Last is the previous meeting of the series (a recording): its recap
	// and the to-dos said in it anchor the prep in what this group last
	// discussed.
	Last string `json:"last,omitempty"`
}

type PrepView struct {
	Title    string        `json:"title"`
	People   []string      `json:"people"`
	Since    time.Time     `json:"since"`
	Met      int           `json:"met"` // meetings with any of them in the record
	Subjects []PrepSubject `json:"subjects"`
	Open     []PrepOpen    `json:"open"`
	Share    []PrepShare   `json:"share"`
	// Last is the recap of the previous meeting of the series (with its
	// to-dos and how they stand now), when it has one the scope may see.
	Last *MeetingPage `json:"last,omitempty"`
}

type PrepSubject struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Why    string  `json:"why"`
	Status *Status `json:"status,omitempty"`
	// Private: for the owner, not the room (MarkPrivate).
	Private  bool               `json:"private,omitempty"`
	AsOf     time.Time          `json:"asOf"`
	NewSince []store.ThreadItem `json:"newSince"`
	Items    []store.ThreadItem `json:"items"` // every visible item, for citations
}

type PrepOpen struct {
	Text   string             `json:"text"`
	Who    string             `json:"who,omitempty"`
	Due    string             `json:"due,omitempty"`
	Thread int64              `json:"thread"`
	Name   string             `json:"name"`
	Items  []store.ThreadItem `json:"items"`
	// Raised is when it was last said; Quiet, that nothing has been said
	// of it for QuietAfter; Overdue, that its due date has passed.
	Raised  time.Time `json:"raised"`
	Quiet   bool      `json:"quiet,omitempty"`
	Overdue bool      `json:"overdue,omitempty"`
	Private bool      `json:"private,omitempty"`
}

type PrepShare struct {
	Thread int64            `json:"thread"`
	Name   string           `json:"name"`
	Item   store.ThreadItem `json:"item"`
}

const (
	prepSubjects = 8
	prepShare    = 10
	prepSharePer = 2
	// prepSensitiveShare: a thread with this share of sensitive items is
	// never suggested for sharing.
	prepSensitiveShare = 0.15
	prepHalfLife       = 30 * 24 * time.Hour
)

// PrepNameKey normalises a person's name: "Ruiz, Dana" and "dana ruiz" are
// the same person.
func PrepNameKey(n string) string {
	n = strings.TrimSpace(n)
	if i := strings.Index(n, ","); i > 0 {
		n = strings.TrimSpace(n[i+1:]) + " " + strings.TrimSpace(n[:i])
	}
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(n) {
		switch {
		case unicode.IsLetter(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		default:
			space = true
		}
	}
	return b.String()
}

// people matches the names a meeting or a statement gives: a full name, an
// email's local part, or a first name no one else in the group has.
type people struct {
	full  map[string]string // key → display name
	first map[string]string
}

func newPeople(names []string) people {
	p := people{full: map[string]string{}, first: map[string]string{}}
	firsts := map[string]int{}
	for _, n := range names {
		k := PrepNameKey(n)
		if k == "" {
			continue
		}
		p.full[k] = n
		firsts[strings.Fields(k)[0]]++
	}
	for k, n := range p.full {
		if f := strings.Fields(k)[0]; firsts[f] == 1 && strings.Contains(k, " ") {
			p.first[f] = n
		}
	}
	return p
}

// mentioned: text names one of them, by full name or by a first name
// only one of them has.
func (p people) mentioned(text string) bool {
	t := " " + PrepNameKey(text) + " "
	for k := range p.full {
		if strings.Contains(t, " "+k+" ") {
			return true
		}
	}
	for f := range p.first {
		if len(f) >= 3 && strings.Contains(t, " "+f+" ") {
			return true
		}
	}
	return false
}

func (p people) match(name string) (string, bool) {
	k := PrepNameKey(name)
	if k == "" {
		return "", false
	}
	if n, ok := p.full[k]; ok {
		return n, true
	}
	if n, ok := p.first[k]; ok {
		return n, true
	}
	if f := strings.Fields(k); len(f) > 1 {
		if n, ok := p.first[f[0]]; ok && !strings.Contains(PrepNameKey(n), " ") {
			return n, true
		}
	}
	return "", false
}

func participantName(pt item.Participant) string {
	if pt.Name != "" {
		return pt.Name
	}
	if i := strings.Index(pt.Email, "@"); i > 0 {
		return strings.ReplaceAll(pt.Email[:i], ".", " ")
	}
	return pt.Email
}

// shareable: never suggest sharing what is about people's jobs, pay,
// health, legal matters or private lives, or anything restricted.
func shareable(l sensitivity.Label) bool {
	return l.Categories == 0 && l.Level < sensitivity.Restricted
}

// Prep builds the view. items are every item (for attendees); owner is the
// owner's name, left out of the group.
func Prep(st *store.Store, sc sensitivity.Scope, req PrepRequest, items []item.Item, owner string) (*PrepView, error) {
	v := &PrepView{Title: req.Title, Since: req.Since}
	if req.Last != "" {
		now := req.Now
		if now.IsZero() {
			now = time.Now()
		}
		last, err := Views{Store: st, Scope: sc, Now: now}.MeetingPage(req.Last, 3, req.QuietAfter, owner)
		if err != nil {
			return nil, err
		}
		v.Last = last
	}
	sp, err := st.ConfirmedSpellings()
	if err != nil {
		return nil, err
	}
	spell := Spellings(sp)
	scoped, err := st.ScopedSpellings()
	if err != nil {
		return nil, err
	}
	// The spellings for one statement: the global ones and those confirmed
	// for it alone.
	spellFor := func(id int64) Spellings {
		if len(scoped[id]) == 0 {
			return spell
		}
		m := Spellings{}
		for k, v := range spell {
			m[k] = v
		}
		for k, v := range scoped[id] {
			m[k] = v
		}
		return m
	}
	var names []string
	byID := map[string]*item.Item{}
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	ownerKey := PrepNameKey(owner)
	if t := byID[req.Template]; t != nil {
		v.Title, v.Since = t.Title, t.OccurredAt
		for _, pt := range t.Participants {
			if !pt.Owner && PrepNameKey(participantName(pt)) != ownerKey {
				names = append(names, participantName(pt))
			}
		}
	} else {
		names = req.People
	}
	if len(names) == 0 {
		return v, nil
	}
	group := newPeople(names)
	for _, n := range group.full {
		v.People = append(v.People, n)
	}
	sort.Strings(v.People)
	// Which meetings any of them attended, and how small each meeting
	// was: what is said in a 1:1 is theirs more than what is said in a
	// large staff meeting they sat in.
	attended := map[string]bool{}
	small := map[string]float64{}
	for _, it := range items {
		small[it.ID] = meetingWeight(len(it.Participants))
	}
	for _, it := range items {
		if it.Kind == item.KindEvent {
			continue
		}
		for _, pt := range it.Participants {
			if _, ok := group.match(participantName(pt)); ok {
				attended[it.ID] = true
				v.Met++
				break
			}
		}
	}
	idx, err := st.ThreadIndex()
	if err != nil {
		return nil, err
	}
	name := map[int64]string{}
	var all []int64
	for _, r := range idx {
		if r.LabelLevel <= sc.MaxLevel {
			name[r.ID] = r.Name
			all = append(all, r.ID)
		}
	}
	tis, err := st.VisibleThreadItems(sc, all)
	if err != nil {
		return nil, err
	}
	statuses, err := st.ThreadStatuses(sc)
	if err != nil {
		return nil, err
	}
	var latest time.Time
	for _, its := range tis {
		for _, it := range its {
			if it.SaidAt.After(latest) {
				latest = it.SaidAt
			}
		}
	}
	if v.Since.IsZero() {
		v.Since = latest.Add(-14 * 24 * time.Hour)
	}
	// A thread is theirs by what they said in it, what was said about them
	// (a role change is mostly others' words about the person), and the
	// meetings with them it was discussed in; small meetings and recent
	// items count more, and a thread named after them most.
	type scored struct {
		id                   int64
		score                float64
		said, about, inMeets int
		holders              map[string]int
	}
	var ss []scored
	for tid, its := range tis {
		s := scored{id: tid, holders: map[string]int{}}
		meets := map[string]bool{}
		for _, it := range its {
			w := math.Pow(0.5, latest.Sub(it.SaidAt).Hours()/prepHalfLife.Hours()) * small[it.ItemID]
			whos := it.Who
			if len(whos) == 0 && it.Holder != "" {
				whos = []string{it.Holder}
			}
			for _, wn := range whos {
				if n, ok := group.match(spellFor(it.ID).Canonical(wn)); ok {
					s.score += 2 * w
					s.said++
					s.holders[n]++
					break
				}
			}
			if group.mentioned(spellFor(it.ID).Canonical(it.Text)) {
				s.score += 1.5 * w
				s.about++
			}
			if attended[it.ItemID] {
				s.score += w
				meets[it.ItemID] = true
			}
		}
		s.inMeets = len(meets)
		if group.mentioned(name[tid]) {
			s.score *= 2
			s.about++
		}
		if s.score > 0 && s.said+s.about > 0 {
			ss = append(ss, s)
		}
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].score > ss[j].score })
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	// The share of a thread's items about people's jobs, pay, health,
	// legal matters or private lives (see "Worth sharing" below).
	private := func(tid int64) bool {
		bad := 0
		for _, it := range tis[tid] {
			if !shareable(it.Label) {
				bad++
			}
		}
		return float64(bad) >= prepSensitiveShare*float64(len(tis[tid]))
	}
	mine := map[int64]bool{}
	for _, s := range ss[:min(len(ss), prepSubjects)] {
		its := tis[s.id]
		sub := PrepSubject{ID: s.id, Name: name[s.id], Items: its, AsOf: its[len(its)-1].SaidAt}
		sub.Why = whyLine(s.holders, s.inMeets)
		sub.Private = req.MarkPrivate && private(s.id)
		if stt, ok := statuses[s.id]; ok {
			var x Status
			if json.Unmarshal([]byte(stt.Body), &x) == nil {
				sub.Status, sub.AsOf = &x, stt.AsOf
				for _, o := range x.Open {
					_, theirs := group.match(o.Who)
					if theirs || strings.Contains(PrepNameKey(o.Who), ownerKey) || mentions(group, o.Who) {
						po := PrepOpen{Text: o.Text, Who: o.Who, Due: o.Due, Thread: s.id, Name: sub.Name, Items: pick(its, o.IDs), Private: sub.Private}
						for _, it := range po.Items {
							if it.SaidAt.After(po.Raised) {
								po.Raised = it.SaidAt
							}
						}
						if po.Raised.IsZero() {
							po.Raised = sub.AsOf
						}
						po.Quiet = req.QuietAfter > 0 && now.Sub(po.Raised) > req.QuietAfter
						if d, ok := dueDate(o.Due); ok && now.After(d.Add(24*time.Hour)) {
							po.Overdue = true
						}
						v.Open = append(v.Open, po)
					}
				}
			}
		}
		for i := len(its) - 1; i >= 0 && len(sub.NewSince) < 5; i-- {
			if its[i].SaidAt.After(v.Since) {
				sub.NewSince = append(sub.NewSince, its[i])
			}
		}
		v.Subjects = append(v.Subjects, sub)
		mine[s.id] = true
	}
	// In a meeting with more than one other person, what is for the owner
	// alone goes last, so a shared screen opens on what the room can see.
	if req.MarkPrivate && len(v.People) > 1 {
		sort.SliceStable(v.Subjects, func(i, j int) bool { return !v.Subjects[i].Private && v.Subjects[j].Private })
	}
	// Live items first, then those gone quiet; with more than one other
	// person, what is for the owner alone after the rest (as subjects); the
	// most recently raised first within each.
	inGroup := req.MarkPrivate && len(v.People) > 1
	sort.SliceStable(v.Open, func(i, j int) bool {
		a, b := v.Open[i], v.Open[j]
		if a.Quiet != b.Quiet {
			return !a.Quiet
		}
		if inGroup && a.Private != b.Private {
			return !a.Private
		}
		return a.Raised.After(b.Raised)
	})
	v.Open = dedupeOpen(v.Open)
	// Worth sharing: the latest word on their subjects, said since, in
	// meetings none of them attended. A thread whose items are often about
	// people's jobs, pay, health, legal matters or private lives is left
	// out whole: the labeller misses some of a subject's items (about a
	// quarter), and a reduction plan's schedule is as sensitive as its
	// names. On September, such subjects ran 38–49% labelled, ordinary
	// work 4–6% (research log 2026-10-17).
	for _, s := range ss {
		if private(s.id) {
			continue
		}
		for _, it := range tis[s.id] {
			if it.Current && it.SaidAt.After(v.Since) && !attended[it.ItemID] && shareable(it.Label) {
				v.Share = append(v.Share, PrepShare{Thread: s.id, Name: name[s.id], Item: it})
			}
		}
	}
	// Their subjects first, then decisions and commitments, then the
	// latest; a few per subject, so one busy thread doesn't fill the list.
	weight := func(s PrepShare) int {
		switch s.Item.Kind {
		case "decision":
			return 0
		case "commitment":
			return 1
		}
		return 2
	}
	sort.SliceStable(v.Share, func(i, j int) bool {
		a, b := v.Share[i], v.Share[j]
		if mine[a.Thread] != mine[b.Thread] {
			return mine[a.Thread]
		}
		if weight(a) != weight(b) {
			return weight(a) < weight(b)
		}
		return a.Item.SaidAt.After(b.Item.SaidAt)
	})
	per := map[int64]int{}
	var kept []PrepShare
	for _, sh := range v.Share {
		if per[sh.Thread] < prepSharePer && len(kept) < prepShare {
			per[sh.Thread]++
			kept = append(kept, sh)
		}
	}
	v.Share = kept
	return v, nil
}

// dedupeOpen drops an open item that says what an earlier one in the list
// says: related threads (a programme and its vendor's) often each carry
// the same unresolved point. Most of the shorter one's words in the other
// (word endings trimmed) is the same point.
func dedupeOpen(open []PrepOpen) []PrepOpen {
	words := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(StripIDs(s)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if len(w) > 3 {
				for _, suf := range []string{"ing", "ed", "es", "s"} {
					if len(w) > len(suf)+4 && strings.HasSuffix(w, suf) {
						w = strings.TrimSuffix(w, suf)
						break
					}
				}
				m[w] = true
			}
		}
		return m
	}
	var kept []PrepOpen
	var seen []map[string]bool
	for _, o := range open {
		w := words(o.Text)
		dup := false
		for _, k := range seen {
			both := 0
			for x := range w {
				if k[x] {
					both++
				}
			}
			// Measured against the shorter: a point restated with more
			// detail is still the same point. Short ones are left alone.
			short := min(len(w), len(k))
			if short >= 5 && float64(both)/float64(short) >= 0.6 {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, o)
			seen = append(seen, w)
		}
	}
	return kept
}

// dueDate reads a due date the status gives as a date ("2026-09-25",
// "2026-09-25 (later today)"); "end of quarter" and the like aren't read.
func dueDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", s[:10])
	return d, err == nil
}

// mentions: an owner field like "ELT / Dana Ruiz" names one of the group.
func mentions(g people, s string) bool {
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == ',' || r == ';' || r == '&' }) {
		if _, ok := g.match(strings.TrimSpace(part)); ok {
			return true
		}
	}
	return false
}

func pick(its []store.ThreadItem, ids []int64) []store.ThreadItem {
	var out []store.ThreadItem
	for _, id := range ids {
		for _, it := range its {
			if it.ID == id {
				out = append(out, it)
			}
		}
	}
	return out
}

func whyLine(holders map[string]int, meets int) string {
	type hc struct {
		n string
		c int
	}
	var hs []hc
	for n, c := range holders {
		hs = append(hs, hc{n, c})
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].c > hs[j].c })
	var parts []string
	for _, h := range hs[:min(len(hs), 3)] {
		parts = append(parts, h.n)
	}
	out := strings.Join(parts, ", ")
	if meets > 0 {
		out += " · discussed in " + plural(meets, "meeting") + " with them"
	}
	return out
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return strconv.Itoa(n) + " " + w + "s"
}

// PersonIn finds the person a question names among the people the owner
// has met at least twice: a full name, or a first name only one of them
// has. "" when none, or when it is ambiguous.
func PersonIn(question string, items []item.Item, owner string) string {
	met := map[string]int{}
	display := map[string]string{}
	ownerKey := PrepNameKey(owner)
	for _, it := range items {
		seen := map[string]bool{}
		for _, pt := range it.Participants {
			k := PrepNameKey(participantName(pt))
			if pt.Owner || k == "" || k == ownerKey || seen[k] || !strings.Contains(k, " ") {
				continue
			}
			seen[k] = true
			met[k]++
			display[k] = participantName(pt)
		}
	}
	q := " " + PrepNameKey(question) + " "
	firsts := map[string][]string{}
	for k, n := range met {
		if n < 2 {
			continue
		}
		if strings.Contains(q, " "+k+" ") {
			return display[k]
		}
		f := strings.Fields(k)[0]
		firsts[f] = append(firsts[f], k)
	}
	var found []string
	for f, ks := range firsts {
		if len(f) >= 3 && strings.Contains(q, " "+f+" ") {
			if len(ks) > 1 {
				return "" // two people share the name
			}
			found = append(found, ks[0])
		}
	}
	if len(found) != 1 {
		return ""
	}
	return display[found[0]]
}

// meetingWeight: a 1:1 counts three times a six-person meeting, a large
// staff meeting under half (research log 2026-10-19: a year of ELT
// subjects crowded the 1:1 ones out of "what's open with X").
func meetingWeight(people int) float64 {
	if people < 2 {
		people = 2
	}
	return math.Min(3, 6/float64(people))
}
