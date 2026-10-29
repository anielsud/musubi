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

// ToDo is one thing someone said would or should happen.
type ToDo struct {
	store.Statement
	// Owners are the people it belongs to (the holder when the extractor
	// named none); Mine, that the owner of this knowledge base is one.
	Owners []string `json:"owners"`
	Mine   bool     `json:"mine,omitempty"`
	// State: open; quiet (open, nothing said of it for QuietAfter);
	// proposed (said should happen, not agreed); done; changed (a later
	// statement replaced it).
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
}

// ToDoRequest says what to list.
type ToDoRequest struct {
	Proposals  bool          `json:"proposals"`
	QuietAfter time.Duration `json:"quietAfter,omitempty"`
}

// ToDos lists what was said would or should happen, open first (overdue,
// then due soonest, then most recently raised), then quiet, then closed.
func (v Views) ToDos(req ToDoRequest, ownerName string) ([]ToDo, error) {
	cs, err := v.Store.Commitments(v.Scope, req.Proposals)
	if err != nil {
		return nil, err
	}
	// Oldest first, so a repeat folds into the next time it was said and
	// that one carries the count forward.
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].SaidAt.Before(cs[j].SaidAt) })
	at := make(map[int64]int, len(cs))
	for i, c := range cs {
		at[c.ID] = i
	}
	todos := make([]ToDo, len(cs))
	folded := make([]bool, len(cs))
	for i, c := range cs {
		t := &todos[i]
		t.Statement, t.Topic = c.Statement, c.Topic
		t.Raised, t.LastRaised = t.Raised+1, c.SaidAt
		for _, f := range c.Later {
			switch f.Rel {
			case "completes", "answers":
				if t.Outcome == nil || t.Outcome.Rel != "completes" {
					f := f
					t.Outcome = &f
				}
			case "supersedes":
				if t.Outcome == nil {
					f := f
					t.Outcome = &f
				}
			case "repeats":
				if j, ok := at[f.ID]; ok && j > i && !folded[i] {
					folded[i] = true
					n := &todos[j]
					n.Raised += t.Raised
					n.Repeats = append(append(n.Repeats, t.Repeats...), c.Statement)
					// What came of the earlier one stands unless the later
					// says otherwise.
					if n.Outcome == nil {
						n.Outcome = t.Outcome
					}
				} else if f.SaidAt.After(t.LastRaised) {
					t.LastRaised = f.SaidAt
				}
			}
		}
	}
	owner := strings.ToLower(strings.TrimSpace(ownerName))
	var out []ToDo
	for i := range todos {
		if folded[i] {
			continue
		}
		t := todos[i]
		t.Repeats = latestFirst(t.Repeats)
		t.Owners = t.Who
		if len(t.Owners) == 0 && t.Holder != "" {
			t.Owners = []string{t.Holder}
		}
		for _, o := range t.Owners {
			if owner != "" && strings.EqualFold(strings.TrimSpace(o), owner) {
				t.Mine = true
			}
		}
		switch {
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
			t.DueAt, t.Due = due(t.Text, t.SaidAt, v.Now)
			t.Overdue = strings.HasPrefix(t.Due, "was")
		}
		out = append(out, t)
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

func latestFirst(sts []store.Statement) []store.Statement {
	sort.SliceStable(sts, func(i, j int) bool { return sts[i].SaidAt.After(sts[j].SaidAt) })
	return sts
}
