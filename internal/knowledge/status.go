package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// StatusVersion changes with the status prompt or the items it is shown.
const StatusVersion = "ts2"

// A thread's status is written from its current items (where it stands)
// and the items retired in the weeks before its latest one (how it got
// there): the topic page and Ask lead with it, so someone asking "where
// are we on X" reads a few sentences, not a list of fragments.
const statusSystem = `You write the status of one subject from a leadership team's meeting notes, for the executive who owns the notes to read before acting on it.

You get the subject and its items in time order, each with its date, kind/status, who said it, and whether it is CURRENT (the latest word) or RETIRED (replaced, completed or answered by a later item; it shows how things got here). Write:
- now: two to four sentences on where it stands as of the latest item. Lead with what matters most for a decision: the decision taken, the number, the blocker, the date. Say who owns what. Do not restate retired positions as current.
- changed: up to four significant changes in the month before the latest item: what changed, from what to what, and when.
- open: questions not yet answered, decisions pending, and commitments not known to be done, with who owns them and any due date said.
- people: up to five people who drive the subject or hold positions on it, as written in the items.
Use only the items. Be specific and brief; name figures and dates. Where items conflict or are unclear, say so. Cite the ids of the items each point rests on in the ids fields.

The CURRENT and RETIRED marks and the item ids are for you, not the reader: never mention them, item numbers, "the notes" or "the items" in the text. Where a retired item still looks open, decide for yourself and list it or leave it out, without comment. One open entry is one question, decision or commitment.`

var statusSchema = json.RawMessage(`{"type":"object","required":["now","now_ids","changed","open","people"],"properties":{
"now":{"type":"string"},"now_ids":{"type":"array","items":{"type":"integer"}},
"changed":{"type":"array","items":{"type":"object","required":["text","ids","date"],"properties":{"text":{"type":"string"},"ids":{"type":"array","items":{"type":"integer"}},"date":{"type":"string"}}}},
"open":{"type":"array","items":{"type":"object","required":["text","ids","who","due"],"properties":{"text":{"type":"string"},"ids":{"type":"array","items":{"type":"integer"}},"who":{"type":"string"},"due":{"type":"string"}}}},
"people":{"type":"array","items":{"type":"string"}}}}`)

// Status is a thread's written status.
type Status struct {
	Now     string        `json:"now"`
	NowIDs  []int64       `json:"now_ids"`
	Changed []StatusPoint `json:"changed"`
	Open    []StatusPoint `json:"open"`
	People  []string      `json:"people"`
}

// StatusPoint is one change or open item, with the statements it rests on.
type StatusPoint struct {
	Text string  `json:"text"`
	IDs  []int64 `json:"ids"`
	Date string  `json:"date,omitempty"`
	Who  string  `json:"who,omitempty"`
	Due  string  `json:"due,omitempty"`
}

const (
	statusCurrentMax = 60 // the latest current items
	statusRetiredMax = 30 // the latest retired items in the window
	statusWindow     = 60 * 24 * time.Hour
	statusMaxTokens  = 3000
)

// StatusInput is what a status is written from: the items shown, the
// label they carry together, and the date of the latest.
type StatusInput struct {
	Items []store.ThreadStatement
	Label sensitivity.Label
	AsOf  time.Time
	Hash  string
}

// StatusInputFor picks the items a status is written from among a thread's
// statements that pass keep (nil keeps all): the latest current ones, and
// the latest retired ones said within statusWindow of the newest item.
func StatusInputFor(name, scope string, sts []store.ThreadStatement, keep func(store.ThreadStatement) bool) StatusInput {
	var cur, old []store.ThreadStatement
	var asOf time.Time
	for _, s := range sts {
		if keep != nil && !keep(s) {
			continue
		}
		if s.SaidAt.After(asOf) {
			asOf = s.SaidAt
		}
		if s.Current {
			cur = append(cur, s)
		} else {
			old = append(old, s)
		}
	}
	cur = cur[max(0, len(cur)-statusCurrentMax):]
	var recent []store.ThreadStatement
	for _, s := range old {
		if asOf.Sub(s.SaidAt) <= statusWindow {
			recent = append(recent, s)
		}
	}
	recent = recent[max(0, len(recent)-statusRetiredMax):]
	items := append(append([]store.ThreadStatement{}, cur...), recent...)
	sortByTime(items)
	in := StatusInput{Items: items, AsOf: asOf}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", StatusVersion, name, scope)
	for _, s := range items {
		in.Label.Level = max(in.Label.Level, s.Level)
		in.Label.Categories |= s.Categories
		fmt.Fprintf(h, "%d/%t/%s\x00", s.ID, s.Current, s.Text)
	}
	in.Hash = hex.EncodeToString(h.Sum(nil)[:12])
	return in
}

func sortByTime(items []store.ThreadStatement) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && (items[j].SaidAt.Before(items[j-1].SaidAt) || items[j].SaidAt.Equal(items[j-1].SaidAt) && items[j].ID < items[j-1].ID); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// WriteStatus asks the model for a thread's status. Ids it cites that it
// wasn't shown are dropped.
func WriteStatus(ctx context.Context, llm provider.LLM, name, scope string, in StatusInput) (Status, error) {
	var lines []string
	shown := map[int64]bool{}
	for _, s := range in.Items {
		shown[s.ID] = true
		flag := "RETIRED"
		if s.Current {
			flag = "CURRENT"
		}
		lines = append(lines, itemLine(s.ItemStatement, time.Time{})+" · "+flag+" · "+s.ItemTitle)
	}
	msg := fmt.Sprintf("Subject: %s — %s\nLatest item: %s\n\nItems (oldest first):\n%s\n\nWrite the status.",
		name, scope, in.AsOf.Format("2006-01-02"), strings.Join(lines, "\n"))
	opt := provider.ChatOptions{Schema: statusSchema, Temperature: 0.3, TopP: 0.8, TopK: 20, MaxTokens: statusMaxTokens, Timeout: threadCallTimeout}
	opt.Session, opt.Kind = "status", "status"
	raw, err := llm.Complete(ctx, []provider.Message{{Role: "system", Content: statusSystem}, {Role: "user", Content: msg}}, opt, nil)
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal([]byte(jsonPart(raw)), &st); err != nil {
		return Status{}, fmt.Errorf("status reply: %w", err)
	}
	if strings.TrimSpace(st.Now) == "" {
		return Status{}, fmt.Errorf("status reply has no status")
	}
	keepIDs := func(ids []int64) []int64 {
		var out []int64
		for _, id := range ids {
			if shown[id] {
				out = append(out, id)
			}
		}
		return out
	}
	st.NowIDs = keepIDs(st.NowIDs)
	for i := range st.Changed {
		st.Changed[i].IDs = keepIDs(st.Changed[i].IDs)
	}
	for i := range st.Open {
		st.Open[i].IDs = keepIDs(st.Open[i].IDs)
	}
	return st, nil
}
