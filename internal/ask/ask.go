// Package ask answers questions from the knowledge base: the threads a
// question is about (their written status and current items) and passages
// found by search, all read in the caller's scope; a model writes a short
// answer citing what it used.
package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

type Answer struct {
	Text    string
	Sources []store.Hit
	// Threads are the subjects the answer drew on, for links to their pages.
	Threads []ThreadRef
	// PromptChars is the size of what the model read (for measuring).
	PromptChars int
}

// ThreadRef is a thread an answer drew on, with the first sentence of its
// status: shown while the answer is written.
type ThreadRef struct {
	ID   int64     `json:"id"`
	Name string    `json:"name"`
	AsOf time.Time `json:"asOf"`
	Now  string    `json:"now,omitempty"`
	// Status is the thread's written status (inline ids removed), shown
	// at once for "where are we" questions while the answer is written.
	Status *knowledge.Status `json:"status,omitempty"`
}

// Hooks follow an answer as it is made: Found when the subjects and
// sources are known (before the model is asked), Token for each piece of
// the answer.
type Hooks struct {
	Found func(*Answer)
	Token func(string)
}

const system = `You answer questions about the user's own meetings, email and chats. Transcripts come from speech recognition and contain errors; "You" is the user.

You may get, for the subjects the question seems to be about, each subject's written status (where it stands, what changed, what is open, as of a date) and its latest items; then passages from the record found by search. Sources are numbered.

Rules:
- Lead with the answer. For "where are we", "status", "latest" or "what changed" questions, answer from the subject statuses and current items: where it stands, what changed recently, what is open, with dates. For what someone said, or when something happened, use the items and passages.
- Prefer the most recent information and give its date ("as of 7 Oct"). Items marked retired were replaced later: never present them as current.
- Cite sources inline like [3] or [2][5], only from the numbered sources.
- A subject found may not be what was asked about; ignore it then. If nothing given answers the question, say so plainly and mention what the sources do cover. Never invent facts.
- Be brief: a short paragraph or a short list.`

const (
	askThreads      = 3  // threads at most
	askThreadItems  = 12 // latest current items per thread
	askPassages     = 6  // passages alongside threads
	askPassagesOnly = 8  // passages when no thread is used
	statusCites     = 3  // sources cited per status point
	// askTimeout bounds an answer: a local model reads 10k tokens of
	// context first, slowly when other requests share it.
	askTimeout = 5 * time.Minute
)

// Ask finds what the question is about and, if a model is configured and
// allowed to read it, writes an answer. Tokens stream to onToken.
func Ask(ctx context.Context, e *engine.Engine, sc sensitivity.Scope, question string, k int, exclude []string, onToken func(string)) (*Answer, error) {
	return AskWith(ctx, e, sc, question, k, exclude, Hooks{Token: onToken})
}

// AskWith is Ask with hooks for showing progress.
func AskWith(ctx context.Context, e *engine.Engine, sc sensitivity.Scope, question string, k int, exclude []string, h Hooks) (*Answer, error) {
	onToken := h.Token
	lim := limitsFor(e)
	llm := e.AskLLM
	// A hosted model reads only what the cloud scope allows, unless the
	// owner allows hosted models to read meeting text.
	if llm != nil && !llm.Local() && !e.Cfg.Knowledge.AllowHosted {
		sc = narrower(sc, sensitivity.Cloud())
	}
	// Confirmed spellings: a question with a misheard or variant name also
	// finds the kept form.
	passage := func(s string) string { return s }
	if sp, err := e.Store.ConfirmedSpellings(); err == nil {
		spell := knowledge.Spellings(sp)
		question = spell.Expand(question)
		if e.Cfg.Knowledge.ApplySpellings {
			passage = spell.Canonical
		}
	}
	vec := e.Embed(ctx, question)
	var ctxt strings.Builder
	ans := &Answer{}
	add := func(h store.Hit) int {
		ans.Sources = append(ans.Sources, h)
		return len(ans.Sources)
	}
	if e.Cfg.Ask.Threads && vec != nil {
		if err := threadContext(e, sc, vec, question, &ctxt, ans, add); err != nil {
			return nil, err
		}
	}
	if err := personContext(e, sc, question, &ctxt, ans, add); err != nil {
		return nil, err
	}
	np := lim.passages
	if len(ans.Threads) == 0 {
		np = max(k, askPassagesOnly)
	}
	q := store.SearchQuery{Text: question, K: np, Mode: store.ModeKeyword, Exclude: exclude}
	if vec != nil {
		q.Mode, q.Vec, q.Model = store.ModeHybrid, vec, e.Embedder.Model()
	}
	hits, err := e.Store.Search(sc, q)
	if err != nil {
		return nil, err
	}
	if len(hits) > 0 {
		ctxt.WriteString("Passages found by search:\n\n")
		for _, h := range hits {
			n := add(h)
			fmt.Fprintf(&ctxt, "[%d] %s — %s (%s)\n%s\n\n", n, h.OccurredAt.Format("Mon 2 Jan 2006"), h.Title, h.Kind, clip(passage(h.Text), 1800))
		}
	}
	if h.Found != nil {
		h.Found(ans)
	}
	if llm == nil {
		return ans, nil
	}
	if len(ans.Sources) == 0 {
		ans.Text = "Nothing in your knowledge base matches that."
		if onToken != nil {
			onToken(ans.Text)
		}
		return ans, nil
	}
	msgs := []provider.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: "Today: " + time.Now().Format("Mon 2 Jan 2006") + "\n\n" + ctxt.String() + "Question: " + question},
	}
	ans.PromptChars = len(msgs[0].Content) + len(msgs[1].Content)
	text, err := llm.Complete(ctx, msgs, provider.ChatOptions{Temperature: 0.2, MaxTokens: 2000, Context: 16384, Timeout: askTimeout}, onToken)
	if err != nil {
		return ans, err
	}
	ans.Text = strings.TrimSpace(text)
	return ans, nil
}

// threadContext writes the found threads' statuses and latest current
// items, numbering each item as a source.
func threadContext(e *engine.Engine, sc sensitivity.Scope, vec []float32, question string, b *strings.Builder, ans *Answer, add func(store.Hit) int) error {
	f, err := e.ThreadFinder()
	if err != nil || f == nil {
		return err
	}
	found := f.Find(sc, vec, question, askThreads)
	if len(found) == 0 {
		return nil
	}
	ids := make([]int64, len(found))
	for i, t := range found {
		ids[i] = t.ID
	}
	items, err := e.Store.VisibleThreadItems(sc, ids)
	if err != nil {
		return err
	}
	statuses, err := e.Store.ThreadStatuses(sc)
	if err != nil {
		return err
	}
	for _, t := range found {
		its := items[t.ID]
		if len(its) == 0 {
			continue
		}
		num := map[int64]int{}
		cite := func(id int64) string {
			if n, ok := num[id]; ok {
				return fmt.Sprintf("[%d]", n)
			}
			for _, it := range its {
				if it.ID == id {
					num[id] = add(itemHit(it))
					return fmt.Sprintf("[%d]", num[id])
				}
			}
			return ""
		}
		ref := ThreadRef{ID: t.ID, Name: t.Name, AsOf: its[len(its)-1].SaidAt}
		if t.Newer && e.Cfg.Ask.NewerFirst {
			fmt.Fprintf(b, "Subject: %s (the latest turn: later than the subjects below, so where it differs it is the current word)\n", t.Name)
		} else {
			fmt.Fprintf(b, "Subject: %s\n", t.Name)
		}
		if st, ok := statuses[t.ID]; ok {
			var s knowledge.Status
			if json.Unmarshal([]byte(st.Body), &s) == nil {
				ref.AsOf, ref.Now = st.AsOf, knowledge.FirstSentence(knowledge.StripIDs(s.Now))
				ref.Status = shown(s)
				fmt.Fprintf(b, "Status as of %s:\n%s\n", st.AsOf.Format("2 Jan 2006"), statusText(s, cite, limitsFor(e)))
			}
		}
		var cur []store.ThreadItem
		for _, it := range its {
			if it.Current {
				cur = append(cur, it)
			}
		}
		cur = cur[max(0, len(cur)-limitsFor(e).items):]
		if len(cur) > 0 {
			b.WriteString("Latest current items:\n")
			for _, it := range cur {
				fmt.Fprintf(b, "%s %s — %s: %s\n", cite(it.ID), it.SaidAt.Format("2 Jan 2006"), it.ItemTitle, it.Text)
			}
		}
		b.WriteString("\n")
		ans.Threads = append(ans.Threads, ref)
	}
	return nil
}

// inlineIDs are statement ids a status writer put in its prose: "(5435)",
// "[5266, 5271]", "(5266–5271)".
var inlineIDs = regexp.MustCompile(`\s*[(\[]\s*\d{1,9}(?:\s*[-–]\s*\d{1,9})?(?:\s*,\s*\d{1,9}(?:\s*[-–]\s*\d{1,9})?)*\s*[)\]]`)

var digits = regexp.MustCompile(`\d+`)

// limits size what Ask reads. ask.small_context reads less: about a third
// faster on the local 27B, with the same facts covered but slightly worse
// answers on the 28-question set (research log 2026-10-19), so it is off
// by default.
type limits struct{ items, passages, cites, changed, open int }

var (
	fullContext  = limits{items: askThreadItems, passages: askPassages, cites: statusCites, changed: 4, open: 6}
	smallContext = limits{items: 8, passages: 4, cites: 2, changed: 3, open: 4}
)

func limitsFor(e *engine.Engine) limits {
	if e.Cfg.Ask.SmallContext {
		return smallContext
	}
	return fullContext
}

// statusText renders a status for the prompt with its citations as source
// numbers; ids the scope can't see are dropped with their citation.
func statusText(s knowledge.Status, cite func(int64) string, lim limits) string {
	refs := func(ids []int64) string {
		var out string
		for _, id := range ids[:min(len(ids), lim.cites)] {
			out += cite(id)
		}
		return out
	}
	prose := func(t string) string {
		return inlineIDs.ReplaceAllStringFunc(t, func(m string) string {
			var out string
			for _, p := range digits.FindAllString(m, lim.cites) {
				if id, err := strconv.ParseInt(p, 10, 64); err == nil {
					out += cite(id)
				}
			}
			if out == "" {
				return ""
			}
			return " " + out
		})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s %s\n", prose(s.Now), refs(s.NowIDs))
	for _, c := range s.Changed[:min(len(s.Changed), lim.changed)] {
		fmt.Fprintf(&b, "Changed (%s): %s %s\n", c.Date, prose(c.Text), refs(c.IDs))
	}
	for _, o := range s.Open[:min(len(s.Open), lim.open)] {
		extra := ""
		if o.Who != "" {
			extra += "; owner: " + o.Who
		}
		if o.Due != "" {
			extra += "; due: " + o.Due
		}
		fmt.Fprintf(&b, "Open: %s%s %s\n", prose(o.Text), extra, refs(o.IDs))
	}
	return b.String()
}

// itemHit shows a thread item as a source: its passage opens in the drawer.
func itemHit(it store.ThreadItem) store.Hit {
	return store.Hit{ChunkID: it.ChunkID, ItemID: it.ItemID, Kind: it.ItemKind, Title: it.ItemTitle, OccurredAt: it.SaidAt,
		Seq: it.ChunkSeq, Speakers: it.Holder, Text: it.Text, Label: it.Label}
}

// narrower is the intersection of two scopes without an audience.
func narrower(a, b sensitivity.Scope) sensitivity.Scope {
	out := a
	out.MaxLevel = min(a.MaxLevel, b.MaxLevel)
	out.Allowed = a.Allowed & b.Allowed
	return out
}

// Excerpts formats hits as numbered context for a prompt.
func Excerpts(hits []store.Hit) string {
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s — %s (%s)\n%s\n\n", i+1, h.OccurredAt.Format("Mon 2 Jan 2006"), h.Title, h.Kind, clip(h.Text, 1800))
	}
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

const prepSystem = `You help an executive prepare for a meeting. You get the people they are meeting, the subjects those people are part of (each with its written status and what is new since they last met), what is open between them, and what was said elsewhere that these people likely haven't heard. Sources are numbered.

Write talking points: four to seven bullets, most important first. Each says what to raise and why now (a decision due, a change since last time, something they need to know, a commitment owed either way), in one or two sentences, citing sources like [3]. Prefer what changed recently and what needs them. Leave out anything that is only background. Never invent facts.`

// PrepBrief writes talking points for a prep view. Tokens stream to
// onToken.
func PrepBrief(ctx context.Context, e *engine.Engine, v *knowledge.PrepView, onToken func(string)) (*Answer, error) {
	llm := e.AskLLM
	if llm == nil {
		return nil, fmt.Errorf("no model configured to write talking points ([ask] model or [llm])")
	}
	if !llm.Local() && !e.Cfg.Knowledge.AllowHosted {
		return nil, fmt.Errorf("talking points read meeting text, so they need a local model, or knowledge.allow_hosted")
	}
	ans := &Answer{}
	num := map[int64]int{}
	cite := func(its []store.ThreadItem) func(int64) string {
		return func(id int64) string {
			if n, ok := num[id]; ok {
				return fmt.Sprintf("[%d]", n)
			}
			for _, it := range its {
				if it.ID == id {
					ans.Sources = append(ans.Sources, itemHit(it))
					num[id] = len(ans.Sources)
					return fmt.Sprintf("[%d]", num[id])
				}
			}
			return ""
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Meeting: %s\nWith: %s\nLast met: %s\n\n", v.Title, strings.Join(v.People, ", "), v.Since.Format("Mon 2 Jan 2006"))
	for _, s := range v.Subjects {
		c := cite(s.Items)
		fmt.Fprintf(&b, "Subject: %s (%s)\n", s.Name, s.Why)
		if s.Status != nil {
			fmt.Fprintf(&b, "Status as of %s:\n%s", s.AsOf.Format("2 Jan 2006"), statusText(*s.Status, c, limitsFor(e)))
		}
		if len(s.NewSince) > 0 {
			b.WriteString("New since you last met:\n")
			for _, it := range s.NewSince {
				fmt.Fprintf(&b, "%s %s — %s: %s\n", c(it.ID), it.SaidAt.Format("2 Jan"), it.ItemTitle, it.Text)
			}
		}
		b.WriteString("\n")
		ans.Threads = append(ans.Threads, ThreadRef{ID: s.ID, Name: s.Name, AsOf: s.AsOf})
	}
	if len(v.Share) > 0 {
		b.WriteString("Said elsewhere, which they likely haven't heard:\n")
		for _, sh := range v.Share {
			c := cite([]store.ThreadItem{sh.Item})
			fmt.Fprintf(&b, "%s %s — %s (%s): %s\n", c(sh.Item.ID), sh.Item.SaidAt.Format("2 Jan"), sh.Name, sh.Item.ItemTitle, sh.Item.Text)
		}
	}
	msgs := []provider.Message{{Role: "system", Content: prepSystem}, {Role: "user", Content: b.String() + "\nWrite the talking points."}}
	text, err := llm.Complete(ctx, msgs, provider.ChatOptions{Temperature: 0.3, MaxTokens: 2000, Context: 16384, Timeout: askTimeout}, onToken)
	if err != nil {
		return ans, err
	}
	ans.Text = strings.TrimSpace(text)
	return ans, nil
}

// personWords mark a question about what is between the owner and someone:
// "what's open with Dana", "what should I raise with Lee at our 1:1".
var personWords = regexp.MustCompile(`(?i)\b(open|raise|prep|prepare|1:1|1-1|one on one|sync|owe|owes|discuss|bring up|between)\b`)

// personContext adds, for a question about someone the owner meets, what
// is open between them and the subjects they are part of (knowledge.Prep).
func personContext(e *engine.Engine, sc sensitivity.Scope, question string, b *strings.Builder, ans *Answer, add func(store.Hit) int) error {
	if !personWords.MatchString(question) {
		return nil
	}
	items, err := e.Store.AllItems()
	if err != nil {
		return err
	}
	who := knowledge.PersonIn(question, items, e.Cfg.Owner.Name)
	if who == "" {
		return nil
	}
	v, err := knowledge.Prep(e.Store, sc, knowledge.PrepRequest{People: []string{who}, QuietAfter: e.Cfg.Prep.QuietAfter(), MarkPrivate: e.Cfg.Prep.MarkPrivate}, items, e.Cfg.Owner.Name)
	if err != nil || len(v.Subjects) == 0 {
		return err
	}
	num := map[int64]int{}
	cite := func(its []store.ThreadItem) func(int64) string {
		return func(id int64) string {
			if n, ok := num[id]; ok {
				return fmt.Sprintf("[%d]", n)
			}
			for _, it := range its {
				if it.ID == id {
					num[id] = add(itemHit(it))
					return fmt.Sprintf("[%d]", num[id])
				}
			}
			return ""
		}
	}
	fmt.Fprintf(b, "About %s (%d meetings with them in the record):\n", who, v.Met)
	if len(v.Open) > 0 {
		b.WriteString("Open between you:\n")
		for _, o := range v.Open[:min(len(v.Open), 8)] {
			c := cite(o.Items)
			var refs string
			for _, it := range o.Items[:min(len(o.Items), statusCites)] {
				refs += c(it.ID)
			}
			fmt.Fprintf(b, "- %s (owner: %s; due: %s; subject: %s) %s\n", knowledge.StripIDs(o.Text), o.Who, o.Due, o.Name, refs)
		}
	}
	b.WriteString("Their subjects:\n")
	for _, s := range v.Subjects[:min(len(v.Subjects), 4)] {
		c := cite(s.Items)
		fmt.Fprintf(b, "Subject: %s (%s)\n", s.Name, s.Why)
		if s.Status != nil {
			fmt.Fprintf(b, "Status as of %s:\n%s", s.AsOf.Format("2 Jan 2006"), statusText(*s.Status, c, limitsFor(e)))
		}
		ref := ThreadRef{ID: s.ID, Name: s.Name, AsOf: s.AsOf}
		if s.Status != nil {
			ref.Now = knowledge.FirstSentence(knowledge.StripIDs(s.Status.Now))
		}
		ans.Threads = append(ans.Threads, ref)
	}
	b.WriteString("\n")
	return nil
}

// shown is a status as the answer view shows it before the answer: the
// writer's inline ids removed.
func shown(s knowledge.Status) *knowledge.Status {
	out := knowledge.Status{Now: knowledge.StripIDs(s.Now), People: s.People}
	for _, c := range s.Changed {
		c.Text = knowledge.StripIDs(c.Text)
		out.Changed = append(out.Changed, c)
	}
	for _, o := range s.Open {
		o.Text = knowledge.StripIDs(o.Text)
		out.Open = append(out.Open, o)
	}
	return &out
}
