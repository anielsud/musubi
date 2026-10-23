package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/anielsud/musubi/internal/provider"
)

// Lean extraction (knowledge.extract = "lean") is the measured meeting
// prompt, lean_ex_v2 (tools/insights-eval/extract_lean.py): a brief of the
// whole meeting, then the transcript in 50-line parts with 8 lines of
// context either side, each listing every knowledge item in its part. The
// items become statements as imported ones do (ImportStatements), so they
// are checked against the meeting and labelled the same way. Measured on
// GLM-5.3-Flash at about 0.9¢ a meeting (docs/research-log.md).

// LeanVersion stamps lean extractions: changing a prompt below bumps it.
const LeanVersion = "lean2"

// LeanParams are a lean extraction's settings.
type LeanParams struct {
	Segment, Context, Parallel int
	// Description says what kind of meeting this is ("an executive
	// leadership meeting"); "" uses the item's title.
	Description string
	// People is who may be meant, for recognising names ("Name: title
	// (office)" lines, or names): never copied into items.
	People string
}

const leanKinds = `Item kinds:
- decision: something decided or agreed in the meeting
- commitment: someone will do something (owner; due date if said)
- fact: stated as true (status, events, plans), including organisation and people changes
- metric: a number with what it measures, its as-of date, and its definition or any dispute about it
- position: a stance someone took and why
- disagreement: an issue with two or more sides; name each side and who holds it
- framing: a memorable phrase, metaphor or story someone used to persuade; quote it and say who used it
- rule: a lesson or rule of thumb: when X, do or don't do Y, because Z
- outside_fact: about the outside world (competitors, partners, customers, regulation, market)
- question: an open question or issue left unresolved
- sensitivity_cue: someone says something is confidential, embargoed or not yet announced (until when)
- personal: a personal detail about someone, attributed to them`

const leanRules = `You extract knowledge items from part of an executive leadership meeting transcript, for a knowledge base that people and AI assistants will rely on later. Wrong, invented or misattributed items are much worse than missing ones.

` + leanKinds + `

Each item must stand on its own, as a note a colleague could read cold:
- name people, organisations, products and deals explicitly; never "it", "they", "the deal" without saying which;
- keep numbers, dates, amounts, conditions and negations exactly as said;
- fold in context and what followed: a question and its answer, a proposal and its outcome, a correction made later;
- attribute to the speaker shown on the line; when the speaker is unknown, leave "who" empty; a demand someone relays belongs to its original source;
- never expand acronyms, and never add titles, surnames, companies or facts that were not said; the company directory is only for recognising who is meant;
- leave out garbled passages rather than guessing; skip small talk unless it is a personal detail;
- lines: the line numbers the item rests on.`

const leanExhaust = `

Be exhaustive. Make one item per distinct point: each number, each decision, each commitment, each position, each rule, each framing gets its own item. Never merge two points into one item, and never summarise several turns into one general statement. A dense 50-line stretch of a leadership meeting typically holds 12 to 20 items.`

const leanV2Rules = `

Status and kind discipline:
- Use decision only when the meeting clearly agreed or decided it, and commitment only when the person clearly committed ("I'll do it", "will do", an accepted ask). A suggestion, an idea, "we should", "maybe", "I'd like to" or "let's think about" is a position or a proposal with status proposed, not a decision or commitment.
- Keep who will do something exactly as said; do not move a task, a date or an occasion to someone or somewhere else.

Nothing that was not said:
- Write names exactly as they were said (a first name stays a first name); never add surnames, titles, departments, roles, channels, places or explanations.
- If a word looks misheard (a product or company name where a person seems meant, or an odd phrase), do not build an item on your guess of what it meant.
- When a line's speaker is unknown, leave who empty; never infer who said it from the content.
- lines: cite every line the item draws on, not just one.`

const leanExample = `

Example (an invented meeting; for format and judgement only, never copy its content):
41 Dana Reyes: Northwind renewal is stuck. They want 40,000 credits but they'll only ever use the reporting module, which doesn't consume credits.
42 Lee Chen: If we put credits they won't use on the order form, finance has to strip them out of every usage report. We learned that the hard way with Bluegill.
43 Dana Reyes: Fine, then credits stay off the form and we price the reporting module on its own.
44 Sam Owner: Agreed. And please, this is not public yet; the price change goes out on the first.
45 Lee Chen: It's like selling fuel to someone with a bicycle.
Items:
{"items":[
 {"kind":"fact","text":"Northwind's renewal is stuck: Northwind wants 40,000 credits but will only use the reporting module, which doesn't consume credits.","who":["Dana Reyes"],"lines":[41],"status":"stated"},
 {"kind":"decision","text":"Northwind's renewal will leave credits off the order form and price the reporting module on its own.","who":["Dana Reyes","Sam Owner"],"lines":[43,44],"status":"agreed"},
 {"kind":"rule","text":"Don't put credits a customer won't consume on an order form: finance then has to strip them out of every usage report (learned on the Bluegill deal).","who":["Lee Chen"],"lines":[42],"status":"stated"},
 {"kind":"sensitivity_cue","text":"The price change is not public until it goes out on the first of the month.","who":["Sam Owner"],"lines":[44],"status":"stated"},
 {"kind":"framing","text":"Lee Chen compared selling credits to a customer who won't use them to \"selling fuel to someone with a bicycle\".","who":["Lee Chen"],"lines":[45],"status":"stated"}
]}`

var leanSchema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{
 "kind":{"type":"string","enum":["decision","commitment","fact","metric","position","disagreement","framing","rule","outside_fact","question","sensitivity_cue","personal"]},
 "text":{"type":"string"},"who":{"type":"array","items":{"type":"string"}},
 "lines":{"type":"array","items":{"type":"integer"}},
 "status":{"type":"string","enum":["decided","agreed","proposed","open","answered","rejected","stated","disputed"]}}}}}}`)

var leanBriefSchema = json.RawMessage(`{"type":"object","properties":{"purpose":{"type":"string"},
 "participants":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"role":{"type":"string"}}}},
 "subjects":{"type":"array","items":{"type":"string"}},
 "glossary":{"type":"array","items":{"type":"object","properties":{"term":{"type":"string"},"means":{"type":"string"}}}}}}`)

type leanBrief struct {
	Purpose      string `json:"purpose"`
	Participants []struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"participants"`
	Glossary []struct {
		Term  string `json:"term"`
		Means string `json:"means"`
	} `json:"glossary"`
}

// LeanLines are the meeting's numbered lines as the lean prompt reads them:
// "N speaker: text", with "unknown speaker" where nobody is named.
func (m *Meeting) LeanLines() []string {
	out := make([]string, len(m.lines))
	for i, l := range m.lines {
		t := l.text
		switch {
		case l.speaker == "":
			t = "unknown speaker: " + t
		case unknownSpeaker(l.speaker):
			// A placeholder or a room's device: nobody named.
			t = "unknown speaker: " + strings.TrimPrefix(t, l.speaker+": ")
		}
		out[i] = fmt.Sprintf("%d %s", i+1, t)
	}
	return out
}

// LeanExtract extracts the meeting's knowledge items with the lean prompt.
// session groups the calls (they share a prompt start the provider caches).
func LeanExtract(ctx context.Context, llm provider.LLM, m *Meeting, p LeanParams, session string) ([]Imported, error) {
	lines := m.LeanLines()
	n := len(lines)
	if n == 0 {
		return nil, nil
	}
	desc := p.Description
	if desc == "" {
		desc = m.Title
	}
	head := fmt.Sprintf("Meeting: %s, %s.\nCompany directory (only for recognising who is meant):\n%s\n", desc, m.Date.Format("2006-01-02"), p.People)
	var brief leanBrief
	reply, err := llm.Complete(ctx, []provider.Message{
		{Role: "system", Content: "You read a whole meeting transcript and write a brief that helps someone interpret any excerpt of it."},
		{Role: "user", Content: head + "\nTranscript:\n" + strings.Join(lines, "\n") + "\n\nWrite: the meeting's purpose; each participant's role in this meeting as far as the transcript shows it; the subjects discussed, in order; and a glossary resolving vague references, acronyms, product names and misheard names to what they mean. Say \"unclear\" rather than guess."},
	}, provider.ChatOptions{Schema: leanBriefSchema, MaxTokens: 8000, Session: session, Kind: "brief", Temperature: 0.7, TopP: 0.95}, nil)
	if err != nil {
		return nil, fmt.Errorf("brief: %w", err)
	}
	if err := json.Unmarshal([]byte(reply), &brief); err != nil {
		return nil, fmt.Errorf("brief: %w", err)
	}
	var bt strings.Builder
	bt.WriteString("\nBrief of the whole meeting:\nPurpose: " + brief.Purpose + "\nParticipants: ")
	for i, pt := range brief.Participants {
		if i > 0 {
			bt.WriteString("; ")
		}
		bt.WriteString(fmt.Sprintf("%s (%s)", pt.Name, cut(pt.Role, 80)))
	}
	bt.WriteString("\nTerms: ")
	for i, g := range brief.Glossary {
		if i == 25 {
			break
		}
		if i > 0 {
			bt.WriteString("; ")
		}
		bt.WriteString(fmt.Sprintf("%s = %s", g.Term, cut(g.Means, 120)))
	}
	system := leanRules + leanExhaust + leanV2Rules + leanExample
	seg, ctxN := max(1, p.Segment), max(0, p.Context)
	type part struct{ a, b int }
	var parts []part
	for a := 1; a <= n; a += seg {
		parts = append(parts, part{a, min(a+seg-1, n)})
	}
	one := func(pt part) ([]Imported, error) {
		lo, hi := max(1, pt.a-ctxN), min(n, pt.b+ctxN)
		var ex strings.Builder
		for i := lo; i <= hi; i++ {
			if i < pt.a || i > pt.b {
				ex.WriteString("[context] ")
			}
			ex.WriteString(lines[i-1] + "\n")
		}
		u := head + bt.String() + fmt.Sprintf("\n\nTranscript excerpt. Lines marked [context] come just before or after the part you cover: use them to understand and complete items, but only extract items whose substance is in the unmarked lines %d-%d.\n\n", pt.a, pt.b) +
			strings.TrimRight(ex.String(), "\n") + "\n\nList every item in the unmarked lines."
		reply, err := llm.Complete(ctx, []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: u}},
			provider.ChatOptions{Schema: leanSchema, MaxTokens: 16000, Session: session, Kind: "extract", Temperature: 0.7, TopP: 0.95}, nil)
		if err != nil {
			return nil, err
		}
		var out struct {
			Items []Imported `json:"items"`
		}
		if err := json.Unmarshal([]byte(reply), &out); err != nil {
			return nil, err
		}
		return out.Items, nil
	}
	// The first part alone, then the rest at once: every part's prompt
	// starts with the same instructions, directory and brief, which the
	// provider serves from its cache once one call has finished with it.
	results := make([][]Imported, len(parts))
	errs := make([]error, len(parts))
	results[0], errs[0] = one(parts[0])
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, p.Parallel))
	for i := 1; i < len(parts); i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = one(parts[i])
		}(i)
	}
	wg.Wait()
	var items []Imported
	failed := 0
	for i := range parts {
		if errs[i] != nil {
			failed++
			continue
		}
		items = append(items, results[i]...)
	}
	// A part that failed is lost to this extraction: fail the meeting so
	// it's extracted again, rather than store it with a hole.
	if failed > 0 {
		return nil, fmt.Errorf("%d of %d parts failed (first: %v)", failed, len(parts), firstErr(errs))
	}
	return items, nil
}

func firstErr(errs []error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var placeholderSpeaker = regexp.MustCompile(`(?i)^(person \d+|speaker \d+|new speaker|unknown.*|system audio)$`)

// unknownSpeaker reports whether a line's speaker label names nobody: a
// placeholder ("Person 3", "Speaker 2"), a provisional name ("Ana?"), or a
// meeting room's device, which stands for whoever is in the room.
func unknownSpeaker(s string) bool {
	if placeholderSpeaker.MatchString(strings.TrimSpace(s)) || strings.HasSuffix(s, "?") {
		return true
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if w == "room" || w == "rooms" {
			return true
		}
	}
	return false
}
