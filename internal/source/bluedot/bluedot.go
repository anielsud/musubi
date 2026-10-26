// Package bluedot reads a folder of Bluedot meeting transcripts (markdown:
// a header with "**Start:** <UTC time>", then "## Transcript" and lines
// "**[m:ss] Name:** text"). Each becomes the same inbox envelope that
// tools/insights-eval/transcripts.py writes, so it's translated the same
// way. Only the transcript is read: Bluedot's own summary and action items
// above "## Transcript" never are.
package bluedot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/mcpdrop"
)

// Source names Bluedot's items.
const Source = "bluedot"

// Connector reads Dir's transcripts dated Since or later. Known reports a
// meeting already loaded another way (the same file under another source
// name), which is skipped.
type Connector struct {
	Dir   string
	Since string // "2006-01-02"; "" reads every file
	Known func(ref string) bool
}

func (c Connector) Name() string { return Source }

var (
	turnRe  = regexp.MustCompile(`\*\*\[([\d:]+)\] ([^*]+):\*\* (.*)`)
	startRe = regexp.MustCompile(`\*\*Start:\*\* (\S+)`)
	datedRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} - `)
	wordRe  = regexp.MustCompile(`[a-z']+`)
)

// filler words: a turn of four or fewer of them folds into the turn
// before, as transcripts.py does ("yeah", "ok thanks").
var filler = map[string]bool{}

func init() {
	for _, w := range strings.Fields("yeah yes yep yup ok okay good great right sure thanks thank you bye cheers hi hello hey mm mhm uhhuh um uh cool perfect awesome exactly totally absolutely correct fine wow oh ah huh very so") {
		filler[w] = true
	}
}

func isFiller(t string) bool {
	w := wordRe.FindAllString(strings.ToLower(t), -1)
	if len(w) == 0 || len(w) > 4 {
		return false
	}
	for _, x := range w {
		if !filler[x] {
			return false
		}
	}
	return true
}

// person: a name label is a speaker; a room's device, "Speaker N" or an
// unknown label is nobody in particular.
func person(label string) bool {
	l := strings.ToLower(label)
	return !(strings.HasPrefix(label, "Room - ") || strings.HasPrefix(label, "Speaker") || strings.Contains(l, "unknown"))
}

func (c Connector) Fetch(ctx context.Context, emit func(source.Record) error) error {
	files, err := filepath.Glob(filepath.Join(c.Dir, "*.md"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		base := filepath.Base(f)
		key := strings.TrimSuffix(base, ".md")
		if c.Since != "" && len(base) >= 10 && base[:10] < c.Since {
			continue
		}
		if c.Known != nil && c.Known(key) {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		env, ok := envelope(key, string(raw))
		if !ok {
			continue // not a transcript (no "## Transcript")
		}
		b, err := json.Marshal(env)
		if err != nil {
			return err
		}
		if err := emit(source.Record{Source: Source, Ref: key, ContentType: mcpdrop.ContentType, Data: b}); err != nil {
			return fmt.Errorf("%s: %w", base, err)
		}
	}
	return nil
}

type turn struct {
	Speaker string `json:"speaker"`
	Email   string `json:"email"`
	Offset  int    `json:"offset"`
	Text    string `json:"text"`
}

// envelope is a transcript as transcripts.py's drop: title, start,
// attendees (the named speakers), turns with fillers folded in.
func envelope(key, raw string) (map[string]any, bool) {
	head, tr, ok := strings.Cut(raw, "## Transcript")
	if !ok {
		return nil, false
	}
	title := key
	if datedRe.MatchString(key) {
		title = key[13:]
	}
	start := time.Time{}
	if m := startRe.FindStringSubmatch(head); m != nil {
		s := regexp.MustCompile(`(\.\d+)?Z?$`).ReplaceAllString(m[1], "")
		if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
			start = t
		}
	}
	if start.IsZero() && len(key) >= 10 {
		start, _ = time.Parse("2006-01-02", key[:10])
	}
	var turns []turn
	var labels []string
	var folded [][][2]string
	for _, m := range turnRe.FindAllStringSubmatch(tr, -1) {
		label, text := strings.TrimSpace(m[2]), strings.Join(strings.Fields(m[3]), " ")
		if len(turns) > 0 && isFiller(text) {
			folded[len(folded)-1] = append(folded[len(folded)-1], [2]string{label, text})
			continue
		}
		turns = append(turns, turn{Offset: seconds(m[1]), Text: text})
		labels = append(labels, label)
		folded = append(folded, nil)
	}
	var names []string
	seen := map[string]bool{}
	for i := range turns {
		for _, f := range folded[i] {
			turns[i].Text += fmt.Sprintf(" (%s: %s)", f[0], f[1])
		}
		if person(labels[i]) {
			turns[i].Speaker = labels[i]
			if !seen[labels[i]] {
				seen[labels[i]] = true
				names = append(names, labels[i])
			}
		}
	}
	att := make([]map[string]string, len(names))
	for i, n := range names {
		att[i] = map[string]string{"name": n}
	}
	return map[string]any{"kind": "meeting", "source": Source, "ref": key, "record": map[string]any{
		"title": title, "start": start.UTC().Format("2006-01-02T15:04:05Z"), "attendees": att, "turns": turns,
	}}, true
}

func seconds(ts string) int {
	var parts []int
	for _, p := range strings.Split(ts, ":") {
		n, _ := strconv.Atoi(p)
		parts = append(parts, n)
	}
	switch len(parts) {
	case 3:
		return parts[0]*3600 + parts[1]*60 + parts[2]
	case 2:
		return parts[0]*60 + parts[1]
	}
	return 0
}
