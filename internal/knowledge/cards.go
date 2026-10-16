package knowledge

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// A card is what someone needs about a subject at the moment it comes up:
// where it stands, what is due, what is theirs, what is open, the rules
// that apply, what must not be repeated, and who to ask. It is assembled
// from claims (docs/topic-cards.md), ranked by salience rather than
// belief: belief says whether something is true, salience whether it
// belongs on the card. No model is involved, so a card is ready within the
// live panel's fast lane.

// CardLine is one claim on a card.
type CardLine struct {
	ClaimID int64     `json:"claimId"`
	TopicID int64     `json:"topicId"`
	Kind    string    `json:"kind"`
	Text    string    `json:"text"`
	Who     string    `json:"who,omitempty"`
	Said    time.Time `json:"said"`
	Meeting string    `json:"meeting,omitempty"`
	// Due is when a commitment or plan falls due, in words relative to now
	// ("due tomorrow", "was due 1 Oct"); DueAt is that date.
	Due   string    `json:"due,omitempty"`
	DueAt time.Time `json:"dueAt,omitempty"`
	// Status is the claim's belief status; Note what changed it ("someone
	// has committed to find out").
	Status   string  `json:"status"`
	Note     string  `json:"note,omitempty"`
	Mine     bool    `json:"mine,omitempty"`
	Salience float64 `json:"salience"`
}

type CardSection struct {
	Name  string     `json:"name"`
	Lines []CardLine `json:"lines"`
}

type Card struct {
	TopicID  int64             `json:"topicId"`
	Anchor   Anchor            `json:"anchor"`
	Title    string            `json:"title"`
	Terms    []string          `json:"terms,omitempty"`
	Last     time.Time         `json:"last"`
	Meetings int               `json:"meetings"`
	Label    sensitivity.Label `json:"label"`
	// Collapsed: restricted, or about people's jobs, pay, health, legal
	// matters or private lives. The panel shows the title until clicked.
	Collapsed bool          `json:"collapsed"`
	Sections  []CardSection `json:"sections"`
	People    []string      `json:"people,omitempty"`
	Topics    []int64       `json:"topics"`
}

// Lines counts the card's lines.
func (c *Card) Lines() int {
	n := 0
	for _, s := range c.Sections {
		n += len(s.Lines)
	}
	return n
}

// Section names, in display order, with how many lines each shows.
var cardSections = []struct {
	name string
	max  int
}{
	{"Watch", 2}, {"Now", 4}, {"Due", 4}, {"Yours", 3}, {"Open", 3}, {"Rules", 3},
}

// sectionFor places a claim; "" leaves it off the card.
func sectionFor(kind string) string {
	switch kind {
	case "sensitivity_cue":
		return "Watch"
	case "metric", "fact", "decision", "outside_fact", "disagreement", "framing":
		return "Now"
	case "commitment":
		return "Due"
	case "question", "issue":
		return "Open"
	case "rule", "requirement":
		return "Rules"
	case "position", "opinion", "hypothesis":
		return "Now"
	}
	return "" // personal details stay off topic cards
}

var cardKindSalience = map[string]float64{
	"sensitivity_cue": 1, "metric": 1, "decision": 0.95, "commitment": 0.9, "rule": 0.9, "requirement": 0.85,
	"question": 0.8, "issue": 0.8, "disagreement": 0.75, "outside_fact": 0.7, "fact": 0.5,
	"position": 0.4, "opinion": 0.4, "hypothesis": 0.3, "framing": 0.3,
}

// logistics marks lines about running the meeting rather than the subject.
var logistics = []string{"agenda", "dial in", "dialing in", "dialling in", "dialed in", "share her screen", "share his screen",
	"share my screen", "next item", "next agenda", "welcom", "the session", "thanked", "joined the call", "on the call"}

// cardHalfLife: older lines matter less, but a programme's state from six
// weeks ago still beats nothing.
const cardHalfLife = 45 * 24 * time.Hour

// Card builds the card for a topic, with its companions, from what is
// visible in scope. ownerID and ownerName mark the owner's own lines. It
// returns nil when nothing on the card is visible.
func (v Views) Card(x *CardIndex, a Anchor, ownerID, ownerName string) (*Card, error) {
	var topics, ids []int64
	topic := a.Topic
	if a.Term != "" {
		ids = x.TermClaims(a.Term)
		seen := map[int64]bool{}
		for _, id := range ids {
			if t := x.claimTopic[id]; t != 0 && !seen[t] {
				seen[t] = true
				topics = append(topics, t)
			}
		}
	} else {
		topics = append([]int64{topic}, x.Companions(topic)...)
		for _, t := range topics {
			ids = append(ids, x.TopicClaims(t)...)
		}
	}
	ev, err := v.Store.Evidence(v.Scope, ids)
	if err != nil {
		return nil, err
	}
	if len(ev) == 0 {
		return nil, nil
	}
	infos, err := v.Store.TopicInfos()
	if err != nil {
		return nil, err
	}
	card := &Card{TopicID: topic, Anchor: a, Terms: x.TopicTerms(topic), Topics: topics}
	if a.Term != "" {
		card.Title, card.Terms = a.Term, []string{a.Term}
	} else if t := infos[topic]; t != nil && labelVisible(v.Scope, t.LabelLevel) {
		card.Title = t.Label
	}
	if card.Title == "" && len(card.Terms) > 0 {
		card.Title = card.Terms[0]
	}
	meetings := map[string]bool{}
	people := map[string]int{}
	bySection := map[string][]CardLine{}
	refinedBy := map[int64][]int64{} // claim → claims that refine it
	for _, id := range ids {
		e := ev[id]
		if e == nil {
			continue
		}
		// Only what was known by Now: a replay of a past meeting must not
		// see what later meetings said.
		known := e.Statements[:0:0]
		for _, st := range e.Statements {
			if !st.SaidAt.After(v.Now) {
				known = append(known, st)
			}
		}
		e.Statements = known
		if len(e.Statements) == 0 {
			continue
		}
		for _, st := range e.Statements {
			card.Label = sensitivity.Combine(card.Label, st.Label)
			meetings[st.ItemID] = true
			if st.SaidAt.After(card.Last) {
				card.Last = st.SaidAt
			}
		}
		for _, r := range e.Relations {
			if r.Rel == "refines" && r.To == id {
				refinedBy[id] = append(refinedBy[id], r.From)
			}
		}
		b := Compute(e, v.Now)
		if b.Status == "superseded" || b.Status == "disproven" || b.Status == "answered" || b.Status == "done" {
			continue
		}
		last := e.Statements[len(e.Statements)-1]
		sec := sectionFor(e.Kind)
		if sec == "" {
			continue
		}
		if e.Kind == "question" && b.Status != "open" {
			continue
		}
		if aboutTheMeeting(last.Text) {
			continue
		}
		if a.Term == "" && !x.Current(topic, id) && !x.Current(e.TopicID, id) {
			continue // the thread no longer counts it as where things stand
		}
		ln := CardLine{ClaimID: id, TopicID: e.TopicID, Kind: e.Kind, Text: last.Text, Said: last.SaidAt, Meeting: last.ItemTitle, Status: b.Status}
		if len(last.Who) > 0 {
			ln.Who = strings.Join(last.Who, ", ")
		} else {
			ln.Who = last.Holder
		}
		for _, w := range last.Who {
			people[w]++
		}
		if last.Holder != "" && len(last.Who) == 0 {
			people[last.Holder]++
		}
		if len(b.PendingBy) > 0 {
			ln.Note = "someone has committed to find out"
		}
		ln.Mine = mine(last, ownerID, ownerName)
		ln.Salience = salience(e.Kind, last, v.Now)
		if e.Kind == "commitment" || e.Kind == "decision" {
			ln.DueAt, ln.Due = due(last.Text, last.SaidAt, v.Now)
			if e.Kind == "decision" && !ln.DueAt.IsZero() && last.Status == "proposed" {
				sec = "Due"
			}
		}
		if ln.Mine && sec != "Watch" {
			sec = "Yours"
		}
		bySection[sec] = append(bySection[sec], ln)
	}
	// A state line that repeats a rule or a cue gives way to it: the rule
	// says what to do about it.
	var guidance []CardLine
	for _, sec := range []string{"Watch", "Rules"} {
		guidance = append(guidance, bySection[sec]...)
	}
	kept := bySection["Now"][:0]
	for _, l := range bySection["Now"] {
		dup := false
		for _, g := range guidance {
			o := wordOverlapIgnoring(l.Text, g.Text, card.Terms)
			dup = dup || o >= 0.4 || o >= 0.25 && l.TopicID == g.TopicID
		}
		if !dup {
			kept = append(kept, l)
		}
	}
	bySection["Now"] = kept
	// Lines are picked best first. A line that a picked line refines gives
	// way to it (the more precise one says it), but only to a line that
	// makes the card: a refinement cut by the section's limit hides nothing.
	picked := map[int64]bool{}
	refines := func(a, b int64) bool { // a refines b
		for _, x := range refinedBy[b] {
			if x == a && a != b {
				return true
			}
		}
		return false
	}
	for _, s := range cardSections {
		cands := dedupeLines(bySection[s.name])
		if s.name == "Due" {
			sortDue(cands, v.Now)
		} else {
			sort.SliceStable(cands, func(i, j int) bool { return cands[i].Salience > cands[j].Salience })
		}
		var lines []CardLine
		for _, l := range cands {
			if len(lines) == s.max {
				break
			}
			skip := false
			for id := range picked {
				skip = skip || refines(id, l.ClaimID)
			}
			if skip {
				continue
			}
			kept := lines[:0]
			for _, o := range lines {
				if refines(l.ClaimID, o.ClaimID) {
					delete(picked, o.ClaimID)
					continue
				}
				kept = append(kept, o)
			}
			lines = append(kept, l)
			picked[l.ClaimID] = true
		}
		if s.name == "Due" {
			sortDue(lines, v.Now)
		} else {
			sort.SliceStable(lines, func(i, j int) bool { return lines[i].Salience > lines[j].Salience })
		}
		if len(lines) > 0 {
			card.Sections = append(card.Sections, CardSection{Name: s.name, Lines: lines})
		}
	}
	if card.Lines() == 0 {
		return nil, nil
	}
	card.Meetings = len(meetings)
	card.Collapsed = card.Label.Level >= sensitivity.Restricted ||
		card.Label.Categories&(sensitivity.HR|sensitivity.Compensation|sensitivity.Health|sensitivity.Legal|sensitivity.Personal) != 0
	type pc struct {
		n string
		c int
	}
	var ps []pc
	for n, c := range people {
		if n != "" && !strings.EqualFold(n, ownerName) {
			ps = append(ps, pc{n, c})
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].c > ps[j].c || ps[i].c == ps[j].c && ps[i].n < ps[j].n })
	for i, p := range ps {
		if i == 4 {
			break
		}
		card.People = append(card.People, p.n)
	}
	return card, nil
}

func mine(st store.Statement, ownerID, ownerName string) bool {
	if ownerID != "" && st.HolderID == ownerID {
		return true
	}
	if ownerName == "" {
		return false
	}
	for _, w := range st.Who {
		if strings.EqualFold(w, ownerName) {
			return true
		}
	}
	return strings.HasPrefix(st.Text, ownerName+" ")
}

// salience: kind, then whether the line carries a figure or a date, its
// age, and whether it checked out against its source.
func salience(kind string, st store.Statement, now time.Time) float64 {
	s := cardKindSalience[kind]
	if hasFigure(st.Text) {
		s *= 1.4
	}
	if len(DatesIn(st.Text, st.SaidAt)) > 0 {
		s *= 1.3
	}
	if age := now.Sub(st.SaidAt); age > 0 {
		s *= math.Pow(0.5, float64(age)/float64(cardHalfLife))
	}
	if !st.Grounded {
		s *= 0.6
	}
	if kind == "fact" && reportedView(st.Text) {
		s *= 0.6 // "X thinks it will be great" is a view, filed as a fact
	}
	return s
}

// aboutTheMeeting: a line about running the meeting (agenda, who dials
// in), not about the subject.
func aboutTheMeeting(text string) bool {
	l := strings.ToLower(text)
	for _, w := range logistics {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// hasFigure: a number that is a quantity, not a year, a date or a length of
// meeting time.
func hasFigure(text string) bool {
	for _, m := range reFigure.FindAllStringIndex(text, -1) {
		f := strings.TrimSpace(text[m[0]:m[1]])
		if len(f) == 4 && reYear.MatchString(f) {
			continue
		}
		rest := strings.ToLower(text[m[1]:min(len(text), m[1]+9)])
		if strings.HasPrefix(strings.TrimSpace(rest), "minute") || strings.HasPrefix(strings.TrimSpace(rest), "min ") {
			continue
		}
		if n, ok := parseNumber(f); ok && n > 1 {
			return true
		}
	}
	// Spelled out: "four and a half of the seven buckets".
	for n := range numbersIn(strings.ToLower(text)) {
		if v, err := strconv.ParseFloat(n, 64); err == nil && v >= 3 && !reFigure.MatchString(text) {
			return true
		}
	}
	return false
}

// due picks the date a commitment is about: the first one still ahead,
// else the last one passed.
func due(text string, said, now time.Time) (time.Time, string) {
	ds := DatesIn(text, said)
	if len(ds) == 0 {
		return time.Time{}, ""
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var past time.Time
	for _, d := range ds {
		// The calendar day as the speaker meant it, compared with today's.
		t := time.Unix(int64(d.Lo), 0).In(said.Location())
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location())
		if !day.Before(today) {
			return day, dueWords(day, today)
		}
		past = day
	}
	return past, "was due " + past.Format("2 Jan")
}

func dueWords(day, today time.Time) string {
	n := int(math.Round(day.Sub(today).Hours() / 24))
	switch {
	case n == 0:
		return "due today"
	case n == 1:
		return "due tomorrow"
	case n < 14:
		return fmt.Sprintf("due in %d days (%s)", n, day.Format("Mon 2 Jan"))
	}
	return "due " + day.Format("2 Jan")
}

// sortDue: upcoming soonest first, then undated by salience, then passed.
func sortDue(lines []CardLine, now time.Time) {
	rank := func(l CardLine) int {
		switch {
		case !l.DueAt.IsZero() && !strings.HasPrefix(l.Due, "was"):
			return 0
		case l.DueAt.IsZero():
			return 1
		}
		return 2
	}
	sort.SliceStable(lines, func(i, j int) bool {
		ri, rj := rank(lines[i]), rank(lines[j])
		if ri != rj {
			return ri < rj
		}
		if ri == 0 {
			return lines[i].DueAt.Before(lines[j].DueAt)
		}
		if ri == 2 {
			return lines[i].DueAt.After(lines[j].DueAt)
		}
		return lines[i].Salience > lines[j].Salience
	})
}

// dedupeLines drops a line that mostly repeats a more salient one.
func dedupeLines(in []CardLine) []CardLine {
	sort.SliceStable(in, func(i, j int) bool { return in[i].Salience > in[j].Salience })
	var out []CardLine
	for _, l := range in {
		dup := false
		for _, o := range out {
			if wordOverlap(l.Text, o.Text) >= 0.6 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, l)
		}
	}
	return out
}

func wordOverlap(a, b string) float64 { return wordOverlapIgnoring(a, b, nil) }

// wordOverlapIgnoring leaves out words every line of a card shares (its
// trigger terms), which say nothing about whether two lines repeat.
func wordOverlapIgnoring(a, b string, ignore []string) float64 {
	skip := map[string]bool{}
	for _, w := range ignore {
		skip[strings.ToLower(w)] = true
	}
	drop := func(ws []string) []string {
		out := ws[:0]
		for _, w := range ws {
			if !skip[w] {
				out = append(out, w)
			}
		}
		return out
	}
	wa, wb := drop(contentWords(a)), drop(contentWords(b))
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range wb {
		set[w] = true
	}
	n := 0
	for _, w := range wa {
		if set[w] {
			n++
		}
	}
	return float64(n) / float64(min(len(wa), len(wb)))
}

var viewVerbs = []string{" thinks ", " feels ", " believes ", " hopes ", " expects ", " is excited", " loves ", " likes "}

func reportedView(text string) bool {
	l := strings.ToLower(text)
	for _, v := range viewVerbs {
		if strings.Contains(l, v) {
			return true
		}
	}
	return false
}
