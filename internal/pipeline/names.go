package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// NameVersion changes when how meetings are named changes.
const NameVersion = "n1"

// unnamedTitle is a title that says nothing about the meeting: Tomoe's
// defaults, and Bluedot's for an unscheduled call.
var unnamedTitle = regexp.MustCompile(`^((\w+ )?Meeting|Session) \d{4}-\d\d-\d\d \d\d:\d\d$|^Ad-hoc meeting `)

// nameMax bounds how many meetings one run names (a backlog is named over
// several runs); an automatic run after an update names fewer.
const nameMax, nameAutoMax = 60, 10

const namePrompt = `You name meetings the way their calendar entry would. Given what a meeting covered, answer with its name only: 2 to 6 words, the main subject (with the other party when it's a call with one person or company and that's clear). No date, no quotes, no "Meeting about", no trailing period.`

// nameMeetings gives each meeting that has no calendar event and only a
// generic title a short name from its digest (or, without one, the start
// of its transcript), stored in item_titles. The calendar event's subject
// still wins when the meeting is linked later; a name is redone only when
// its input changes.
func (r *Runner) nameMeetings(ctx context.Context, o KnowledgeOptions, items []item.Item, links map[string]string, ks *KnowledgeStats) error {
	have, err := r.Store.GivenTitles()
	if err != nil {
		return err
	}
	var llm provider.LLM
	max := nameMax
	if o.Auto {
		max = nameAutoMax
	}
	for i := range items {
		it := &items[i]
		if ks.Named >= max || ctx.Err() != nil {
			break
		}
		if it.Kind != item.KindMeeting || links[it.ID] != "" || !unnamedTitle.MatchString(it.Title) {
			continue
		}
		text := r.nameInput(it)
		if text == "" {
			continue
		}
		if llm == nil {
			if llm, err = r.digestLLM(o); err != nil {
				return err
			}
		}
		hash := hashOf("name", NameVersion, llm.ModelName(), text)
		if have[it.ID].Input == hash {
			continue
		}
		reply, err := llm.Complete(ctx, []provider.Message{{Role: "system", Content: namePrompt}, {Role: "user", Content: text}}, provider.ChatOptions{Temperature: 0, MaxTokens: 40}, nil)
		if err != nil {
			r.logf("name %s: %v", it.ID, err)
			continue
		}
		name := cleanName(reply)
		if name == "" {
			continue
		}
		if err := r.Store.SetItemTitle(it.ID, name, "generated", hash); err != nil {
			return err
		}
		ks.Named++
	}
	if ks.Named > 0 {
		r.logf("names: %d meetings named", ks.Named)
	}
	return nil
}

// nameInput is what a meeting is named from: its digest's overview and
// section subjects, or the first few thousand characters of its passages.
func (r *Runner) nameInput(it *item.Item) string {
	if d, err := r.Store.MeetingDigest(sensitivity.Owner(), it.ID); err == nil && d != nil && d.Overview != "" {
		var b strings.Builder
		b.WriteString(d.Overview)
		for _, s := range d.Sections {
			fmt.Fprintf(&b, "\n- %s", s.Subject)
		}
		return b.String()
	}
	chunks, err := r.Store.Chunks(it.ID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range chunks {
		if b.Len() > 4000 {
			break
		}
		b.WriteString(c.Text)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// cleanName takes the model's first line, without quotes, labels or a
// trailing period; a reply that isn't a short name gives "".
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	for _, p := range []string{"Name:", "Title:", "Meeting name:"} {
		if strings.HasPrefix(strings.ToLower(s), strings.ToLower(p)) {
			s = strings.TrimSpace(s[len(p):])
		}
	}
	for i := 0; i < 2; i++ { // "Name". and "Name."
		s = strings.TrimRight(strings.Trim(s, "\"'“”‘’*`# "), ".")
	}
	words := strings.FieldsFunc(s, unicode.IsSpace)
	if len(words) == 0 || len(words) > 10 || len(s) > 80 {
		return ""
	}
	return strings.Join(words, " ")
}

// NameMeetings runs only the naming step, for a backlog (`musubi
// name-meetings`): up to nameMax meetings per call.
func (r *Runner) NameMeetings(ctx context.Context, o KnowledgeOptions) (int, error) {
	items, err := r.Store.AllItems()
	if err != nil {
		return 0, err
	}
	links, err := r.Store.LinksFrom("occurred_as")
	if err != nil {
		return 0, err
	}
	var ks KnowledgeStats
	err = r.nameMeetings(ctx, o, items, links, &ks)
	return ks.Named, err
}
