package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// PointsVersion changes with the points prompts, checks or merging,
// re-running point extraction.
const PointsVersion = "p1"

// Meeting-level points (knowledge.extract = "points"). Statements are taken
// one passage at a time, so they miss what the next passage clarifies and
// lean on "it" and "the agreement". Points are taken from runs of passages
// with the neighbouring passages as context and a brief of the whole
// meeting, so each can be read cold. Measured on the pilot meetings
// (docs/research-log.md, 2026-10-11 and 12): with Qwen3.8-27B thinking,
// about 23.5 of 24 key points covered and 1-2% wrong.
//
// Wrong points are the risk that matters: downstream reasoning treats them
// as fact. Two defences run here. The prompt asks the model to leave out
// what it is unsure of, with worked examples of past mistakes. Then every
// point is checked against the meeting in code: each name and figure must
// appear in the meeting or the known facts (the brief does not count: it
// is model-written and can carry the same invention), and each quote must
// be in the line it cites. A point failing a check is kept but marked
// ungrounded, which belief discounts and linking won't merge on.

// Point is one self-contained thing worth remembering from a meeting.
type Point struct {
	Kind     string          `json:"kind"`   // decision, commitment, position, fact, question, issue
	Status   string          `json:"status"` // decided, agreed, proposed, open, answered, rejected, stated
	Text     string          `json:"text"`
	Who      []string        `json:"who"`
	Personal bool            `json:"personal"`
	Evidence []PointEvidence `json:"evidence"`
	// Issues lists failed checks; empty when the point checks out.
	Issues []string `json:"-"`
}

// PointEvidence is a line of the meeting (numbered from 1 across the whole
// meeting) and the words a point rests on.
type PointEvidence struct {
	Line  int    `json:"line"`
	Quote string `json:"quote"`
}

// Meeting is a transcript as the points prompts see it.
type Meeting struct {
	Title string
	Date  time.Time
	// Known is the facts sheet (KnownFacts): who the owner is, and each
	// invitee's organisation, which the brief must not guess.
	Known  string
	Chunks []store.Chunk
	lines  []meetingLine
}

type meetingLine struct {
	chunk int // index into Chunks
	n     int // line within the chunk, from 1
	text  string
}

// NewMeeting numbers the chunks' lines across the meeting. The owner's
// lines ("You: ...") are shown as "Name (You)" so the model can name them.
func NewMeeting(title string, date time.Time, known, owner string, chunks []store.Chunk) *Meeting {
	m := &Meeting{Title: title, Date: date, Known: known, Chunks: chunks}
	for ci, c := range chunks {
		for i, l := range SplitLines(c.Text) {
			t := l.Text
			switch {
			case l.Speaker == "You" && owner != "":
				t = owner + " (You): " + t
			case l.Speaker != "":
				t = l.Speaker + ": " + t
			}
			m.lines = append(m.lines, meetingLine{ci, i + 1, t})
		}
	}
	return m
}

// Invitee is someone invited to a meeting, as the calendar has them.
type Invitee struct{ Name, Email string }

// KnownFacts writes the facts the model may rely on: the owner and their
// company, and each invitee's organisation by email domain. An address
// that isn't first.last gives no name, so it is shown as it is rather than
// letting the model guess one.
func KnownFacts(owner, ownerEmail string, internal []string, invitees []Invitee) string {
	company := "the company"
	if len(internal) > 0 {
		company = orgOf(internal[0])
	}
	lines := []string{fmt.Sprintf("The user is %s of %s (shown as \"You\").", owner, company)}
	for _, p := range invitees {
		em := strings.ToLower(p.Email)
		if em == "" || em == strings.ToLower(ownerEmail) {
			continue
		}
		local, dom, _ := strings.Cut(em, "@")
		name := p.Name
		if name == "" {
			if strings.Contains(local, ".") {
				var ws []string
				for _, w := range strings.Split(local, ".") {
					if w != "" {
						ws = append(ws, strings.ToUpper(w[:1])+w[1:])
					}
				}
				name = strings.Join(ws, " ")
			} else {
				name = "someone with address " + em
			}
		}
		org := orgOf(dom) + " (external)"
		if sensitivity.InternalDomain(dom, internal) {
			org = company
		}
		lines = append(lines, fmt.Sprintf("Invited: %s, %s.", name, org))
	}
	return strings.Join(lines, "\n")
}

func orgOf(dom string) string {
	first, _, _ := strings.Cut(strings.ToLower(dom), ".")
	if first == "" {
		return dom
	}
	return strings.ToUpper(first[:1]) + first[1:]
}

// transcript renders chunks [a, b) with line numbers; lines outside
// [markA, markB) are tagged as context.
func (m *Meeting) transcript(a, b, markA, markB int) string {
	var sb strings.Builder
	for i, l := range m.lines {
		if l.chunk < a || l.chunk >= b {
			continue
		}
		tag := ""
		if l.chunk < markA || l.chunk >= markB {
			tag = "[context] "
		}
		fmt.Fprintf(&sb, "%d %s%s\n", i+1, tag, l.text)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (m *Meeting) header() string {
	return fmt.Sprintf("Meeting: %s (%s).", m.Title, m.Date.Format("Jan 2 2006"))
}

// pointRules is the measured prompt (FEWSHOT=1 in the pilot), except that
// personal life is no longer skipped, and points say whether they are
// personal (both untested as of p1).
const pointRules = `A point is one thing worth remembering later: a decision, a commitment (who will do what), a position someone took and why, a fact with names, numbers or dates, or an open question/issue. Make each point complete and self-contained, as a short note a colleague could read cold:
- name people, organisations, documents and projects explicitly; never "it", "they", "the agreement", "the project" without saying which;
- fold in the context and what followed: if a question was asked and answered, the point is the question AND the answer; if something was clarified or corrected later, state the corrected version; if a proposal was rejected, say so;
- keep conditions and caveats ("only if...", "not until...", "unclear whether...");
- keep exact numbers, dates and amounts; never invent them;
- distinguish what was decided from what was only proposed or discussed; hypotheticals and thinking aloud are not decisions;
- skip small talk, jokes and banter, and anything too garbled to be sure of.
Transcripts come from speech recognition: words are often misheard. When unsure, leave it out.

More rules, from mistakes seen before:
- Never expand acronyms, and never add job titles, company names, law-firm names or other facts that were not said in the meeting.
- Keep apart three things that are easy to mix up: what someone else asks for or requires (a client's demand), what we do today (current practice), and what this meeting agreed. Say which one each point is.
- Attribute a point to the person who said or holds it. If someone relays another person's demand, the demand belongs to the other person; the relayer only reported it.
- If a line is too garbled to be sure what it means, leave it out rather than guess; do not turn a fragment into a position.
- Personal details (holidays, family, where someone is from, language needs) are worth keeping when said clearly: attribute them to the person they are about, as facts, and mark them personal. Every other point is not personal.

Example 1 (an invented meeting; for format and judgement only, never copy its content):
Known facts: The user is Sam Owner of Northwind. Invited: Lee Chen, Northwind. Invited: someone with address dana@bluegill.example, Bluegill (external).
12 Dana: Our risk team requires all critical fixes within ten days.
13 Lee Chen: Today we do criticals in about thirty days, honestly.
14 Sam Owner (You): Could we do ten days for criticals that actually affect us?
15 Dana: That works for us if you document which ones you skip.
16 Lee Chen: OK. And uh the the thing with the the thing forty
17 Sam Owner (You): Great, ten days for applicable criticals, documented. Lee, can you write that up by Friday?
18 Lee Chen: Will do. Off to Lisbon next week by the way, my sister's wedding.
Points:
{"points":[
 {"kind":"position","status":"stated","text":"Bluegill's risk team requires all critical fixes within ten days (Dana's demand, as Bluegill's requirement).","who":["Dana"],"personal":false,"evidence":[{"line":12,"quote":"Our risk team requires all critical fixes within ten days"}]},
 {"kind":"fact","status":"stated","text":"Northwind currently fixes critical issues in about thirty days (Lee Chen, describing today's practice).","who":["Lee Chen"],"personal":false,"evidence":[{"line":13,"quote":"Today we do criticals in about thirty days"}]},
 {"kind":"decision","status":"agreed","text":"Northwind and Bluegill agreed on ten days for critical fixes that actually affect Northwind, provided Northwind documents which criticals it skips; this replaces Bluegill's demand for all criticals.","who":["Sam Owner","Dana"],"personal":false,"evidence":[{"line":14,"quote":"ten days for criticals that actually affect us"},{"line":15,"quote":"That works for us if you document which ones you skip"},{"line":17,"quote":"ten days for applicable criticals, documented"}]},
 {"kind":"commitment","status":"agreed","text":"Lee Chen will write up the ten-day agreement for applicable criticals by Friday.","who":["Lee Chen"],"personal":false,"evidence":[{"line":17,"quote":"Lee, can you write that up by Friday?"},{"line":18,"quote":"Will do"}]},
 {"kind":"fact","status":"stated","text":"Lee Chen is going to Lisbon next week for his sister's wedding.","who":["Lee Chen"],"personal":true,"evidence":[{"line":18,"quote":"Off to Lisbon next week by the way, my sister's wedding"}]}
]}
(Line 16 is too garbled to use, so it yields nothing.)

Example 2 (invented):
Known facts: The user is Sam Owner of Northwind. Invited: Priya Natarajan, Northwind.
40 Priya Natarajan: Did legal ever come back on the data clause?
41 Sam Owner (You): Not yet. I think it's fine, but I want them to confirm.
42 Priya Natarajan: So we're not shipping the export until they do?
43 Sam Owner (You): Right, export waits for legal.
Points:
{"points":[
 {"kind":"issue","status":"open","text":"Legal has not yet confirmed the data clause; Sam Owner thinks it is fine but wants legal to confirm.","who":["Sam Owner"],"personal":false,"evidence":[{"line":41,"quote":"Not yet. I think it's fine, but I want them to confirm"}]},
 {"kind":"decision","status":"decided","text":"The export will not ship until legal confirms the data clause.","who":["Sam Owner","Priya Natarajan"],"personal":false,"evidence":[{"line":42,"quote":"we're not shipping the export until they do"},{"line":43,"quote":"export waits for legal"}]}
]}`

var pointSchema = json.RawMessage(`{"type":"object","required":["points"],"properties":{"points":{"type":"array","items":{"type":"object",
"required":["kind","status","text","who","personal","evidence"],"properties":{
"kind":{"type":"string","enum":["decision","commitment","position","fact","question","issue"]},
"status":{"type":"string","enum":["decided","agreed","proposed","open","answered","rejected","stated"]},
"text":{"type":"string"},
"who":{"type":"array","items":{"type":"string"}},
"personal":{"type":"boolean"},
"evidence":{"type":"array","items":{"type":"object","required":["line","quote"],"properties":{"line":{"type":"integer"},"quote":{"type":"string"}}}}}}}}}`)

var briefSchema = json.RawMessage(`{"type":"object","required":["purpose","participants","subjects","glossary"],"properties":{
"purpose":{"type":"string"},
"participants":{"type":"array","items":{"type":"object","required":["name","role"],"properties":{"name":{"type":"string"},"role":{"type":"string"}}}},
"subjects":{"type":"array","items":{"type":"string"}},
"glossary":{"type":"array","items":{"type":"object","required":["term","means"],"properties":{"term":{"type":"string"},"means":{"type":"string"}}}}}}`)

// Pointer extracts a meeting's points.
type Pointer struct {
	LLM provider.LLM
	// Think reasons before each segment's points (not the brief: there it
	// looped for minutes and gained nothing).
	Think bool
	// Segment is chunks per call; Parallel, calls at once.
	Segment, Parallel int
	Logf              func(format string, args ...any)
}

type brief struct {
	Purpose      string `json:"purpose"`
	Participants []struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"participants"`
	Subjects []string `json:"subjects"`
	Glossary []struct {
		Term  string `json:"term"`
		Means string `json:"means"`
	} `json:"glossary"`
}

// text is the brief as segments see it. A JSON brief made segments return
// nothing; prose works.
func (b brief) text(known string) string {
	var sb strings.Builder
	if known != "" {
		sb.WriteString(known + "\n")
	}
	sb.WriteString("Purpose: " + b.Purpose + "\nParticipants: ")
	var ps []string
	for _, p := range b.Participants {
		ps = append(ps, fmt.Sprintf("%s (%s)", p.Name, clipRunes(p.Role, 60)))
	}
	sb.WriteString(strings.Join(ps, "; ") + "\nTerms: ")
	var ts []string
	for i, g := range b.Glossary {
		if i == 12 {
			break
		}
		ts = append(ts, fmt.Sprintf("%s = %s", g.Term, clipRunes(g.Means, 100)))
	}
	sb.WriteString(strings.Join(ts, "; "))
	return sb.String()
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Extract returns the meeting's points, checked, in meeting order. Any
// segment failing twice fails the meeting: a partial set stamped as done
// would never be completed.
func (p *Pointer) Extract(ctx context.Context, m *Meeting) ([]Point, error) {
	if len(m.lines) == 0 {
		return nil, nil
	}
	var br brief
	if err := p.call(ctx, false, "You read a whole meeting transcript and write a brief that helps someone interpret any excerpt of it.",
		fmt.Sprintf("%s\nKnown facts (use as given, do not contradict):\n%s\n\n%s\n\nWrite: the meeting's purpose (one sentence); each participant's role or organisation only as far as the transcript shows it (say unclear rather than guess; the user named in the known facts is the person whose records these are); the subjects discussed, in order; and a glossary that resolves vague references used in the meeting (\"the agreement\", \"the project\", acronyms, misheard names) to what they mean.",
			m.header(), m.Known, m.transcript(0, len(m.Chunks), 0, len(m.Chunks))),
		briefSchema, 1500, &br); err != nil {
		return nil, fmt.Errorf("brief: %w", err)
	}
	bt := br.text(m.Known)

	seg := max(p.Segment, 1)
	var starts []int
	for a := 0; a < len(m.Chunks); a += seg {
		starts = append(starts, a)
	}
	results := make([][]Point, len(starts))
	errs := make([]error, len(starts))
	sem := make(chan struct{}, max(p.Parallel, 1))
	var wg sync.WaitGroup
	for i, a := range starts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b := min(a+seg, len(m.Chunks))
			user := fmt.Sprintf("%s\n%s\n\nTranscript excerpt. Lines marked [context] come just before or after the part you cover: use them to understand and complete points, but only extract points whose substance is in the unmarked lines.\n\n%s\n\nList the points in the unmarked lines (usually 2 to 6; none if it is all small talk). Evidence: the line numbers and exact words each point rests on.",
				m.header(), bt, m.transcript(max(0, a-1), b+1, a, b))
			var out struct {
				Points []Point `json:"points"`
			}
			for attempt := 0; attempt < 2; attempt++ {
				if errs[i] = p.call(ctx, p.Think, "You extract knowledge points from part of a meeting transcript.\n\n"+pointRules, user, pointSchema, 2500, &out); errs[i] == nil || ctx.Err() != nil {
					break
				}
				p.logf("points: segment at passage %d failed, retrying: %v", a, errs[i])
			}
			results[i] = out.Points
		}()
	}
	wg.Wait()
	var all []Point
	for i, r := range results {
		if errs[i] != nil {
			return nil, fmt.Errorf("segment at passage %d: %w", starts[i], errs[i])
		}
		all = append(all, r...)
	}
	return m.Check(dedupe(clean(all, len(m.lines)))), nil
}

func (p *Pointer) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

func (p *Pointer) call(ctx context.Context, think bool, system, user string, schema json.RawMessage, maxTokens int, out any) error {
	opt := provider.ChatOptions{Schema: schema, Temperature: 0, MaxTokens: maxTokens, Context: 16384, Timeout: 10 * time.Minute}
	if think {
		// Qwen's recommended thinking sampling: greedy decoding loops. The
		// token limit covers reasoning too; bound reasoning on the server
		// (llama-server --reasoning-budget).
		opt.Temperature, opt.TopP, opt.TopK = 0.6, 0.95, 20
		opt.Think, opt.MaxTokens, opt.Context, opt.Timeout = true, 16000, 24576, 30*time.Minute
	}
	reply, err := p.LLM.Complete(ctx, []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, opt, nil)
	if err != nil {
		return err
	}
	if i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}"); i >= 0 && j > i {
		reply = reply[i : j+1]
	}
	if err := json.Unmarshal([]byte(reply), out); err != nil {
		return fmt.Errorf("reply isn't JSON: %w", err)
	}
	return nil
}

var pointKinds = map[string]bool{"decision": true, "commitment": true, "position": true, "fact": true, "question": true, "issue": true}
var pointStatuses = map[string]bool{"decided": true, "agreed": true, "proposed": true, "open": true, "answered": true, "rejected": true, "stated": true}

// clean drops malformed points and evidence outside the meeting.
func clean(ps []Point, nLines int) []Point {
	var out []Point
	for _, p := range ps {
		p.Text = strings.TrimSpace(p.Text)
		if p.Text == "" || !pointKinds[p.Kind] {
			continue
		}
		if !pointStatuses[p.Status] {
			p.Status = "stated"
		}
		ev := p.Evidence[:0]
		for _, e := range p.Evidence {
			if e.Line >= 1 && e.Line <= nLines {
				ev = append(ev, e)
			}
		}
		p.Evidence = ev
		var who []string
		for _, w := range p.Who {
			if w = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(w), "(You)")); w != "" {
				who = append(who, w)
			}
		}
		p.Who = who
		out = append(out, p)
	}
	return out
}

// dedupe drops a point that says what an earlier one says (segments see
// their neighbours as context and sometimes extract from it anyway),
// keeping whichever cites more evidence. Points that merely concern the
// same thing are kept: merging them was tried with a model and lost
// decisions.
func dedupe(ps []Point) []Point {
	var out []Point
	for _, p := range ps {
		dup := false
		for i := range out {
			if wordJaccard(contentWordSet(p.Text), contentWordSet(out[i].Text)) >= 0.8 {
				if len(p.Evidence) > len(out[i].Evidence) {
					out[i] = p
				}
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, p)
		}
	}
	return out
}

func contentWordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range plainWords(s) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}

func wordJaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// Check marks points whose names, figures or quotes aren't in the meeting.
// Names and figures are checked against the whole meeting, since a point
// folds in what was said elsewhere; quotes against the line cited (or a
// neighbour, as models misnumber by one).
func (m *Meeting) Check(ps []Point) []Point {
	var all strings.Builder
	for _, l := range m.lines {
		all.WriteString(l.text + "\n")
	}
	src := all.String() + m.Known
	srcLower := strings.ToLower(src)
	srcTokens := wordSet(src)
	srcNums := numbersIn(src)
	for i := range ps {
		p := &ps[i]
		p.Issues = nil
		for _, w := range properNames(p.Text) {
			if !nameIn(w, srcLower, srcTokens) {
				p.Issues = append(p.Issues, "name not in meeting: "+w)
			}
		}
		for n := range numbersIn(p.Text) {
			// "no one", "one of": "one" is rarely a figure.
			if n == "1" || n == "0" {
				continue
			}
			if !figureIn(n, srcNums) {
				p.Issues = append(p.Issues, "figure not in meeting: "+n)
			}
		}
		if len(p.Evidence) == 0 {
			p.Issues = append(p.Issues, "no evidence")
		}
		for _, e := range p.Evidence {
			if !m.quoteNear(e) {
				p.Issues = append(p.Issues, fmt.Sprintf("quote not at line %d", e.Line))
				break
			}
		}
	}
	return ps
}

func (m *Meeting) quoteNear(e PointEvidence) bool {
	qw := plainWords(e.Quote)
	if len(qw) == 0 {
		return false
	}
	var window []string
	for k := e.Line - 2; k <= e.Line; k++ {
		if k >= 0 && k < len(m.lines) {
			window = append(window, m.lines[k].text)
		}
	}
	sw := wordSet(strings.Join(window, " "))
	hit := 0
	for _, w := range qw {
		if sw[w] {
			hit++
		}
	}
	return float64(hit) >= 0.7*float64(len(qw))
}

// figureIn accepts a figure written at another scale ("$250k" for "two
// hundred and fifty thousand", "40M" for "forty million").
func figureIn(n string, src map[string]bool) bool {
	if src[n] {
		return true
	}
	for _, scale := range []string{"000", "000000", "000000000"} {
		if !strings.Contains(n, ".") && src[n+scale] {
			return true
		}
	}
	return false
}

var reProper = regexp.MustCompile(`\b[A-Z][a-zA-Z&'\-]+(?:\s+[A-Z][a-zA-Z&'\-]+)*`)

// commonCaps are capitalised words that start sentences or label points
// rather than name anything.
var commonCaps = wordList(`Decided Agreed Proposed Rejected Answered Stated Personal Next Unrelated Today Currently Previously Later Earlier Meanwhile Instead Rather Owner Counsel
The A An This That These Those It Its They Their Them We Our Us You Your He She His Her Him If When Where What Which Who Whom Whose How Why Whether While Although Though Once Unless
Both Each Every Everyone Everybody Some Someone Most Many Several Few None Nobody Nothing Neither Either All Any No Not Only Also However Because
Decision Proposal Clarification Context Constraint Concern Question Issue Fact Position Commitment Action Client Customer Team Company
There Then After Before Until Since As For In On At By With Without From To Of And Or But So Yes OK Okay`)

func wordList(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

// properNames returns the capitalised words of a point's text: names of
// people, organisations, products and places, which are what a model
// invents when it "helpfully" expands an acronym or adds a firm.
func properNames(text string) []string {
	t := strings.NewReplacer("(", " ", ")", " ", "&", " and ", "-", " ", "\"", " ", "’", "'").Replace(text)
	var out []string
	seen := map[string]bool{}
	for _, m := range reProper.FindAllString(t, -1) {
		for _, w := range strings.Fields(m) {
			w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "'")
			if len(w) <= 2 || commonCaps[w] || seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// nameIn allows for speech recognition spelling a name differently in
// different places: same first five letters, or one letter off.
func nameIn(w, srcLower string, srcTokens map[string]bool) bool {
	wl := strings.ToLower(w)
	if strings.Contains(srcLower, wl) {
		return true
	}
	if len(wl) < 5 {
		return false
	}
	for tok := range srcTokens {
		if d := len(tok) - len(wl); d < -2 || d > 2 {
			continue
		}
		if len(tok) >= 5 && tok[:5] == wl[:5] {
			return true
		}
		if len(tok) == len(wl) {
			diff := 0
			for k := range tok {
				if tok[k] != wl[k] {
					diff++
				}
			}
			if diff <= 1 {
				return true
			}
		}
	}
	return false
}

// pointKind maps a point onto the statement kinds belief and linking know.
// Status decides: a proposed decision is an opinion until agreed, and an
// answered question is a fact.
func pointKind(kind, status string) string {
	switch {
	case status == "proposed" && (kind == "decision" || kind == "commitment"):
		return "opinion"
	case kind == "position":
		return "opinion"
	case kind == "question" || kind == "issue":
		if status == "open" {
			return "question"
		}
		return "fact"
	case kind == "decision" && status == "rejected":
		return "fact"
	}
	return kind
}

func pointCertainty(status string) float64 {
	switch status {
	case "proposed":
		return 0.6
	case "open":
		return 0.5
	}
	return 0.9
}

// PointStatement turns a point into a statement, anchored at its first
// evidence line. Its own label is the most sensitive of the passages it
// rests on (labels, by chunk seq), what its text matches in the
// sensitivity rules, and Personal when it is about someone's private life.
func (m *Meeting) PointStatement(p Point, itemStart time.Time, holderID func(string) string, labels map[int]sensitivity.Label) store.Statement {
	first := m.lines[0]
	quote := ""
	var ev []store.Evidence
	var own sensitivity.Label
	if len(p.Evidence) == 0 {
		// Nothing says where it came from: as sensitive as the meeting's
		// most sensitive passage.
		for _, lb := range labels {
			own = sensitivity.Combine(own, lb)
		}
	}
	for k, e := range p.Evidence {
		l := m.lines[e.Line-1]
		if k == 0 {
			first, quote = l, e.Quote
		}
		c := m.Chunks[l.chunk]
		ev = append(ev, store.Evidence{ChunkID: c.ID, Line: l.n, Quote: e.Quote})
		if lb, ok := labels[c.Seq]; ok {
			own = sensitivity.Combine(own, lb)
		}
	}
	if l, ok := sensitivity.ClassifyText(p.Text); ok {
		own = sensitivity.Combine(own, l)
	}
	if p.Personal {
		own.Categories |= sensitivity.Personal
	}
	own.Level = max(own.Level, sensitivity.LevelFor(own.Categories))
	c := m.Chunks[first.chunk]
	said := itemStart.Add(time.Duration(c.Start * float64(time.Second)))
	holder := ""
	if len(p.Who) > 0 {
		holder = p.Who[0]
	}
	return store.Statement{
		ChunkID: c.ID, Line: first.n, Kind: pointKind(p.Kind, p.Status), Text: p.Text,
		Holder: holder, HolderID: holderID(holder), Certainty: pointCertainty(p.Status), SaidAt: said,
		Value: Normalize("none", "", said), Quote: quote,
		Grounded: len(p.Issues) == 0, Issues: strings.Join(p.Issues, "; "),
		SearchText: SearchForm(p.Text),
		Status:     p.Status, Who: p.Who, Evidence: ev, Own: own,
	}
}
