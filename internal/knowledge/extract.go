// Package knowledge turns passages into statements, statements into claims
// with relations, claims into topics, and computes how much to believe a
// claim when it is read. Design: docs/architecture.md, "Knowledge layer".
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// ExtractVersion changes with the prompt or parsing, re-running extraction.
const ExtractVersion = "x6"

var Kinds = []string{"fact", "opinion", "hypothesis", "decision", "commitment", "requirement", "question"}

const extractPrompt = `You extract the substance of a passage from someone's meeting transcripts, email or chat, so it can be tracked over time. Transcripts come from speech recognition and contain errors. "You" is the user, %s.

List what the passage asserts, as separate statements. For each give:
- "line": the number of the line it comes from
- "kind": fact (stated as true, including announcements and status), opinion (a judgement or preference), hypothesis (a guess: "I think", "maybe", "might", "probably"), decision (something agreed or decided), commitment (someone will do something), requirement (something must be true or be done), or question (an open question)
- "text": one sentence that stands on its own without the passage: full names instead of "I", "we", "he", "she", "they"; say what "it" refers to; keep numbers, amounts and dates; keep negation and hedging exactly as said ("it is not X" stays "the cause is not X")
- "holder": who said or holds it, written exactly as the speaker is named in the passage
- "certainty": how sure the holder sounded, from 0 to 1 (1: stated as certain; 0.7: fairly sure; 0.5: "I think"; 0.2: a guess)
- "value": the one date, amount, number, percentage or duration the statement turns on, if any, as {"type": "date", "money", "number", "percent", "duration" or "none", "raw": the words as said, e.g. "Q4", "mid November", "$40M", "15%%", "two years"}

Skip greetings, small talk, filler and talk about the meeting itself, but keep announcements even when phrased as thanks or congratulations ("congratulations to Ana on her promotion" states a promotion). Merge repeats. At most 8 statements; fewer is fine; none is fine.`

// extractSchema constrains the reply (structured output).
var extractSchema = json.RawMessage(`{"type":"object","required":["statements"],"properties":{"statements":{"type":"array","items":{
"type":"object","required":["line","kind","text","holder","certainty","value"],"properties":{
"line":{"type":"integer"},
"kind":{"type":"string","enum":["fact","opinion","hypothesis","decision","commitment","requirement","question"]},
"text":{"type":"string"},
"holder":{"type":"string"},
"certainty":{"type":"number"},
"value":{"type":"object","required":["type","raw"],"properties":{"type":{"type":"string","enum":["date","money","number","percent","duration","none"]},"raw":{"type":"string"}}}}}}}}`)

// Line is one line of a passage as the extractor sees it.
type Line struct {
	Speaker string
	Text    string
}

// SplitLines turns a chunk's "Speaker: text" lines back into lines.
func SplitLines(chunkText string) []Line {
	var out []Line
	for _, raw := range strings.Split(chunkText, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		sp, text, ok := strings.Cut(raw, ": ")
		if !ok || len(sp) > 60 {
			out = append(out, Line{Text: raw})
			continue
		}
		out = append(out, Line{Speaker: speakerName(sp), Text: text})
	}
	return out
}

func speakerName(s string) string {
	if i := strings.Index(s, " ("); i >= 0 && strings.HasSuffix(s, ")") && strings.HasPrefix(s, "Person ") {
		return s[i+2 : len(s)-1]
	}
	return s
}

// Extracted is a statement as the model returned it.
type Extracted struct {
	Line      int     `json:"line"`
	Kind      string  `json:"kind"`
	Text      string  `json:"text"`
	Holder    string  `json:"holder"`
	Certainty float64 `json:"certainty"`
	Value     struct {
		Type string `json:"type"`
		Raw  string `json:"raw"`
	} `json:"value"`
}

// Extract asks the model for the statements in one passage. orgs maps
// speakers from outside the company to their organisation, so "we" in a
// customer's words resolves to the customer.
func Extract(ctx context.Context, llm provider.LLM, owner, title string, lines []Line, orgs map[string]string) ([]Extracted, error) {
	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "Source: %s\n\n", title)
	}
	for i, l := range lines {
		if l.Speaker != "" {
			sp := l.Speaker
			if o := orgs[strings.ToLower(sp)]; o != "" {
				sp += " (" + o + ")"
			}
			fmt.Fprintf(&b, "%d %s: %s\n", i+1, sp, l.Text)
		} else {
			fmt.Fprintf(&b, "%d %s\n", i+1, l.Text)
		}
	}
	out, err := llm.Complete(ctx, []provider.Message{
		{Role: "system", Content: fmt.Sprintf(extractPrompt, owner)},
		{Role: "user", Content: b.String()},
	}, provider.ChatOptions{Schema: extractSchema, Temperature: 0, MaxTokens: 900}, nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Statements []Extracted `json:"statements"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("extract: model reply isn't JSON: %w", err)
	}
	var keep []Extracted
	for _, e := range parsed.Statements {
		e.Kind = normalizeKind(e.Kind)
		e.Text = strings.TrimSpace(e.Text)
		if e.Text == "" || !validKind(e.Kind) {
			continue
		}
		if e.Certainty <= 0 || e.Certainty > 1 {
			e.Certainty = 0.7
		}
		if e.Line < 1 || e.Line > len(lines) {
			e.Line = 1
		}
		if i := strings.Index(e.Holder, " ("); i > 0 && strings.HasSuffix(e.Holder, ")") {
			e.Holder = e.Holder[:i]
		}
		if e.Holder == "" {
			e.Holder = lines[e.Line-1].Speaker
		}
		keep = append(keep, e)
	}
	return keep, nil
}

// kindAliases maps kinds the model invents onto the seven it was given.
var kindAliases = map[string]string{
	"announcement": "fact", "status": "fact", "update": "fact", "observation": "fact", "statement": "fact", "claim": "fact",
	"plan": "decision", "agreement": "decision", "resolution": "decision",
	"action": "commitment", "action item": "commitment", "task": "commitment", "promise": "commitment", "request": "commitment",
	"proposal": "opinion", "suggestion": "opinion", "recommendation": "opinion", "belief": "opinion", "preference": "opinion",
	"assumption": "hypothesis", "guess": "hypothesis", "speculation": "hypothesis", "prediction": "hypothesis",
	"need": "requirement", "constraint": "requirement", "must": "requirement",
	"open question": "question", "concern": "question",
}

func normalizeKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if a, ok := kindAliases[k]; ok {
		return a
	}
	return k
}

func validKind(k string) bool {
	for _, x := range Kinds {
		if k == x {
			return true
		}
	}
	return false
}

// ToStatement fills a store statement from an extraction, normalising its
// value relative to when it was said.
func ToStatement(e Extracted, chunk store.Chunk, itemStart time.Time, holderID string) store.Statement {
	said := itemStart.Add(time.Duration(chunk.Start * float64(time.Second)))
	return store.Statement{
		ChunkID: chunk.ID, Line: e.Line, Kind: e.Kind, Text: e.Text, Holder: e.Holder, HolderID: holderID,
		Certainty: e.Certainty, SaidAt: said, Value: Normalize(e.Value.Type, e.Value.Raw, said),
	}
}
