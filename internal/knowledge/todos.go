package knowledge

import (
	"sort"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/store"
)

// To-dos: every time someone says a thing will or should happen, it is a
// commitment (or, not yet agreed, a proposal) extracted from the meeting.
// What came of it is read from the statements threading related to it
// later: one that completes it closes it, one that supersedes it changes
// it, one that repeats it raises it again. Nothing is stored for the list.
// The owner's word (owner_links) is read the same way and outranks the
// threader's: done or dropped is their evidence, a link says two are the
// same, one replaces another or is a step of it, and "not" and "due" fix
// the reading.

// ToDo is one thing someone said would or should happen.
type ToDo struct {
	store.Statement
	// Owners are the people it belongs to (the holder when the extractor
	// named none); Mine, that the owner of this knowledge base is one.
	Owners []string `json:"owners"`
	Mine   bool     `json:"mine,omitempty"`
	// State: open; quiet (open, nothing said of it for QuietAfter);
	// proposed (said should happen, not agreed); done; changed (a later
	// statement replaced it); not (the owner said it isn't a to-do).
	State   string    `json:"state"`
	Due     string    `json:"due,omitempty"`
	DueAt   time.Time `json:"dueAt,omitempty"`
	Overdue bool      `json:"overdue,omitempty"`
	Topic   int64     `json:"topic,omitempty"`
	// Raised: times it was said, counting repeats; LastRaised, the latest.
	Raised     int       `json:"raised"`
	LastRaised time.Time `json:"lastRaised"`
	// Outcome is the statement that closed or changed it.
	Outcome *store.Follow `json:"outcome,omitempty"`
	// Repeats are the earlier times it was said, folded into this one.
	Repeats []store.Statement `json:"repeats,omitempty"`
	// TopicLabel is its thread's name, when the scope may see it.
	TopicLabel string `json:"topicLabel,omitempty"`
	// Parent is the to-do the owner said this is a step of; Steps and
	// StepsDone count a parent's.
	Parent    int64 `json:"parent,omitempty"`
	Steps     int   `json:"steps,omitempty"`
	StepsDone int   `json:"stepsDone,omitempty"`
	// Marks are the owner's links and marks on it (or on what was folded
	// into it), for undoing.
	Marks []store.OwnerLink `json:"marks,omitempty"`
	// From a meeting digest (ToDoRequest.Source "digest"): who asked for
	// it, and the digest to-do's id. A negative ID means no statement says
	// the same, so it has no thread and the owner can't mark it yet.
	Requester string `json:"requester,omitempty"`
	Digest    int64  `json:"digest,omitempty"`
	// Agreed is the summary's reading (agreed, asked, floated, declined);
	// empty for a to-do from statements.
	Agreed string `json:"agreed,omitempty"`
	// givenDue is the due date the digest read, 2006-01-02.
	givenDue string
}

// ToDoRequest says what to list.
type ToDoRequest struct {
	Proposals  bool          `json:"proposals"`
	QuietAfter time.Duration `json:"quietAfter,omitempty"`
	// Source is "digest" to list the to-dos of meeting digests (and the
	// commitment statements of items with none), "digest-only" for the
	// digests' to-dos alone; anything else lists commitment statements.
	Source string `json:"source,omitempty"`
}

// ToDos lists what was said would or should happen, open first (overdue,
// then due soonest, then most recently raised), then quiet, then closed.
func (v Views) ToDos(req ToDoRequest, ownerName string) ([]ToDo, error) {
	read := v.Store.Commitments
	if req.Source == "digest" || req.Source == "digest-only" {
		read = v.Store.DigestCommitments
	}
	cs, err := read(v.Scope, req.Proposals)
	if err != nil {
		return nil, err
	}
	if req.Source == "digest-only" {
		kept := cs[:0]
		for _, c := range cs {
			if c.Digest != 0 {
				kept = append(kept, c)
			}
		}
		cs = kept
	}
	// Oldest first, so a repeat folds into the next time it was said and
	// that one carries the count forward.
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].SaidAt.Before(cs[j].SaidAt) })
	at := make(map[int64]int, len(cs))
	for i, c := range cs {
		at[c.ID] = i
	}
	own, err := v.ownerWord(cs, at, ownerName)
	if err != nil {
		return nil, err
	}
	todos := make([]ToDo, len(cs))
	into := make([]int, len(cs))
	for i := range into {
		into[i] = -1
	}
	for i, c := range cs {
		t := &todos[i]
		t.Statement, t.Topic = c.Statement, c.Topic
		t.Requester, t.Digest, t.givenDue, t.Agreed = c.Requester, c.Digest, c.DueDate, c.Agreed
		t.Raised, t.LastRaised = t.Raised+1, c.SaidAt
		t.Marks = append(t.Marks, own[i].marks...)
		for _, f := range c.Later {
			switch f.Rel {
			case "completes", "answers", "supersedes":
				if takes(t.Outcome, f) {
					f := f
					t.Outcome = &f
				}
			case "repeats":
				if j, ok := at[f.ID]; ok && j > i && into[i] < 0 {
					into[i] = j
					n := &todos[j]
					n.Raised += t.Raised
					n.Repeats = append(append(n.Repeats, t.Repeats...), c.Statement)
					n.Marks = append(n.Marks, t.Marks...)
					// What came of the earlier one stands unless the later
					// says otherwise.
					if t.Outcome != nil && takes(n.Outcome, *t.Outcome) {
						n.Outcome = t.Outcome
					}
					own[j].inherit(own[i])
				} else if f.SaidAt.After(t.LastRaised) {
					t.LastRaised = f.SaidAt
				}
			}
		}
	}
	// What an item was folded into, to the end of the chain.
	rep := func(i int) int {
		for into[i] >= 0 {
			i = into[i]
		}
		return i
	}
	labels, err := v.Store.ThreadLabels()
	if err != nil {
		return nil, err
	}
	owner := strings.ToLower(strings.TrimSpace(ownerName))
	var out []ToDo
	for i := range todos {
		if into[i] >= 0 {
			continue
		}
		t := todos[i]
		t.Marks = uniqueMarks(t.Marks)
		if l, ok := labels[t.Topic]; ok && labelVisible(v.Scope, l.LabelLevel) {
			t.TopicLabel = l.Label
		}
		if p := own[i].parent; p >= 0 {
			if p = rep(p); p != i {
				t.Parent = todos[p].ID
			}
		}
		t.Repeats = latestFirst(t.Repeats)
		t.Owners = t.Who
		if len(t.Owners) == 0 && t.Holder != "" {
			t.Owners = []string{t.Holder}
		}
		if own[i].whoSet {
			t.Owners = nil
			for _, n := range strings.Split(own[i].who, ",") {
				if n = strings.TrimSpace(n); n != "" {
					t.Owners = append(t.Owners, n)
				}
			}
		}
		for _, o := range t.Owners {
			if owner != "" && strings.EqualFold(strings.TrimSpace(o), owner) {
				t.Mine = true
			}
		}
		switch {
		case own[i].not:
			t.State = "not"
		case t.Outcome != nil && t.Outcome.Rel == "supersedes":
			t.State = "changed"
		case t.Outcome != nil:
			t.State = "done"
		case t.Status == "proposed":
			t.State = "proposed"
		case req.QuietAfter > 0 && v.Now.Sub(t.LastRaised) > req.QuietAfter:
			t.State = "quiet"
		default:
			t.State = "open"
		}
		if t.State == "open" || t.State == "quiet" || t.State == "proposed" {
			switch {
			case own[i].dueSet:
				t.DueAt, t.Due = dueOn(own[i].due, v.Now)
			case t.givenDue != "":
				t.DueAt, t.Due = dueOn(t.givenDue, v.Now)
			case t.Digest != 0:
				// The digest read the timing with the meeting's date: no
				// date there means none was said, not one to guess from
				// the words.
			default:
				t.DueAt, t.Due = due(t.Text, t.SaidAt, v.Now)
			}
			t.Overdue = strings.HasPrefix(t.Due, "was")
		}
		out = append(out, t)
	}
	// A parent counts its steps; a step whose parent isn't listed stands
	// on its own.
	byID := make(map[int64]int, len(out))
	for i, t := range out {
		byID[t.ID] = i
	}
	for i := range out {
		p, ok := byID[out[i].Parent]
		if !ok || out[i].State == "not" {
			out[i].Parent = 0
			continue
		}
		out[p].Steps++
		if s := out[i].State; s == "done" || s == "changed" {
			out[p].StepsDone++
		}
	}
	rank := func(t ToDo) int {
		switch t.State {
		case "open":
			if t.Overdue {
				return 0
			}
			return 1
		case "proposed":
			return 2
		case "quiet":
			return 3
		case "not":
			return 5
		}
		return 4
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		// Open with a date ahead: soonest first; otherwise latest raised.
		if rank(a) == 1 && !a.DueAt.IsZero() != !b.DueAt.IsZero() {
			return !a.DueAt.IsZero()
		}
		if rank(a) == 1 && !a.DueAt.IsZero() && !a.DueAt.Equal(b.DueAt) {
			return a.DueAt.Before(b.DueAt)
		}
		return a.LastRaised.After(b.LastRaised)
	})
	return out, nil
}

// takes says whether a later statement f decides what came of a to-do
// over cur: the owner's word over the threader's, and the owner's latest
// over their earlier; otherwise the first that completes it stands, a
// later answer replaces an earlier one, and a change counts only when
// nothing else was said.
func takes(cur *store.Follow, f store.Follow) bool {
	switch {
	case cur == nil:
		return true
	case cur.Owner != f.Owner:
		return f.Owner
	case f.Owner:
		return true
	case f.Rel == "supersedes":
		return false
	}
	return cur.Rel != "completes"
}

// ownerSaid is the owner's word on one to-do.
type ownerSaid struct {
	not    bool
	due    string
	dueSet bool
	parent int // index of the to-do it is a step of; -1 for none
	who    string
	whoSet bool
	marks  []store.OwnerLink
}

// inherit carries the owner's word on an earlier time something was said
// to the later one it folds into, where that says nothing of its own.
func (o *ownerSaid) inherit(e ownerSaid) {
	o.not = o.not || e.not
	if !o.dueSet {
		o.due, o.dueSet = e.due, e.dueSet
	}
	if o.parent < 0 {
		o.parent = e.parent
	}
	if !o.whoSet {
		o.who, o.whoSet = e.who, e.whoSet
	}
}

// ownerWord reads the owner's links and marks onto the to-dos they name:
// done and dropped become later statements (theirs) that complete or
// replace it, same and replaces become repeats and supersedes between the
// two, the rest is returned per to-do. A link to a statement the scope
// can't see, or that isn't listed, is left out.
func (v Views) ownerWord(cs []store.Commitment, at map[int64]int, ownerName string) ([]ownerSaid, error) {
	own := make([]ownerSaid, len(cs))
	for i := range own {
		own[i].parent = -1
	}
	links, err := v.Store.OwnerLinks()
	if err != nil || len(links) == 0 {
		return own, err
	}
	// A summary's to-do that no statement matches is keyed by its meeting
	// and its text as written.
	unmatched := map[store.StatementKey]int{}
	for i, c := range cs {
		if c.ID < 0 {
			text := c.Text
			if c.Original != "" {
				text = c.Original
			}
			unmatched[store.StatementKey{Item: c.ItemID, Text: text}] = i
		}
	}
	find := func(k store.StatementKey) (int, bool) {
		if i, ok := unmatched[k]; ok {
			return i, true
		}
		ids, err := v.Store.ResolveKeys([]store.StatementKey{k})
		if err != nil || len(ids) == 0 {
			return 0, false
		}
		i, ok := at[ids[0]]
		return i, ok
	}
	for _, l := range links {
		a, ok := find(l.Statement)
		if !ok {
			continue
		}
		b := -1
		if l.Other.Item != "" {
			if b, ok = find(l.Other); !ok {
				continue
			}
			own[b].marks = append(own[b].marks, l)
		}
		own[a].marks = append(own[a].marks, l)
		said := func(rel string) store.Follow {
			return store.Follow{Rel: rel, Owner: true, Statement: store.Statement{Text: l.Note, Holder: ownerName, SaidAt: l.At}}
		}
		switch l.Rel {
		case "done":
			cs[a].Later = append(cs[a].Later, said("completes"))
		case "dropped":
			cs[a].Later = append(cs[a].Later, said("supersedes"))
		case "not":
			own[a].not = true
		case "due":
			own[a].due, own[a].dueSet = l.Note, true
		case "who":
			own[a].who, own[a].whoSet = l.Note, true
		case "same":
			// The earlier folds into the later, as a repeat does (cs is
			// oldest first).
			e, n := min(a, b), max(a, b)
			cs[e].Later = append(cs[e].Later, store.Follow{Rel: "repeats", Owner: true, Statement: cs[n].Statement})
		case "replaces":
			cs[b].Later = append(cs[b].Later, store.Follow{Rel: "supersedes", Owner: true, Statement: cs[a].Statement})
		case "part":
			own[a].parent = b
		}
	}
	return own, nil
}

// dueOn reads a date the owner set (2006-01-02; empty for none).
func dueOn(s string, now time.Time) (time.Time, string) {
	d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), now.Location())
	if err != nil {
		return time.Time{}, ""
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if d.Before(today) {
		return d, "was due " + d.Format("2 Jan")
	}
	return d, dueWords(d, today)
}

// uniqueMarks drops a link seen twice (both its ends folded into one).
func uniqueMarks(ms []store.OwnerLink) []store.OwnerLink {
	seen := map[int64]bool{}
	var out []store.OwnerLink
	for _, m := range ms {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}

func latestFirst(sts []store.Statement) []store.Statement {
	sort.SliceStable(sts, func(i, j int) bool { return sts[i].SaidAt.After(sts[j].SaidAt) })
	return sts
}
