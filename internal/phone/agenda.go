package phone

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// The agenda (pushed on every sync: Agenda, sync.go) is what the phone
// carries to the meetings: the owner's events and the briefs drafted for
// them, read in the owner's scope (the phone is the owner's), each brief
// with its label so the phone shows it.

const (
	defaultDays = 10
	maxDays     = 14

	// Caps from the contract (docs/phone.md, "Agenda JSON").
	maxBriefSources = 30
	maxOpen         = 15
	maxOpenSources  = 3
	maxPassage      = 1200
	maxSourceTitle  = 300
)

type Event struct {
	ID        string     `json:"id"`
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	Subject   string     `json:"subject"`
	Location  string     `json:"location"`
	Organizer string     `json:"organizer"`
	Attendees []Attendee `json:"attendees"`
	Brief     *Brief     `json:"brief"`
	Open      []Open     `json:"open"`
}

type Attendee struct {
	Name string `json:"name"`
}

type Brief struct {
	Text        string      `json:"text"`
	MadeAt      time.Time   `json:"made_at"`
	Sensitivity Sensitivity `json:"sensitivity"`
	// Sources are the passages the text cites as [n], n as the desktop's
	// Answer numbers them (the saved brief's sources, from 1); a cited
	// source the scope may not see is left out, and the phone drops its mark.
	Sources []Source `json:"sources"`
}

// Source is a passage behind a brief or an open item, with its own label.
type Source struct {
	N           int         `json:"n"`
	Title       string      `json:"title"`
	Kind        string      `json:"kind"`
	OccurredAt  time.Time   `json:"occurred_at"`
	Speakers    string      `json:"speakers"`
	Text        string      `json:"text"`
	Sensitivity Sensitivity `json:"sensitivity"`
}

// Open is an item open between the owner and the event's people, as the
// desktop's prep shows it, its statement citations taken out of the text
// and the passages behind it as Sources.
type Open struct {
	Text    string     `json:"text"`
	Who     string     `json:"who"`
	Due     string     `json:"due"`
	Thread  string     `json:"thread"` // the subject's name
	Raised  *time.Time `json:"raised"`
	Quiet   bool       `json:"quiet"`
	Overdue bool       `json:"overdue"`
	Private bool       `json:"private"`
	Sources []Source   `json:"sources"`
}

// OpenFunc is an event's open items as the desktop's prep gathers them
// (engine.Prep with the event as template), already in the owner's scope.
type OpenFunc func(eventID string) ([]knowledge.PrepOpen, error)

// Sensitivity is a brief's label as the MCP gives it.
type Sensitivity struct {
	Level string            `json:"level"`
	Share sensitivity.Share `json:"share"`
	Why   string            `json:"why"`
}

// AgendaStore is what the agenda reads: *store.Store.
type AgendaStore interface {
	AllItems() ([]item.Item, error)
	ItemLabel(itemID string) (sensitivity.Label, []string, error)
	PrepBriefFor(sc sensitivity.Scope, itemID string) (*store.PrepBrief, error)
	ChunkHit(sc sensitivity.Scope, chunkID int64) (*store.Hit, error)
}

// clampDays is the agenda window asked for, 1 to maxDays.
func clampDays(days int) int {
	switch {
	case days <= 0:
		return defaultDays
	case days > maxDays:
		return maxDays
	}
	return days
}

// BuildAgenda lists the calendar events that haven't ended and start
// within days of now, in order, with their briefs and (when open is set)
// the items open with their people; only what sc may see. An event
// without a label is treated as confidential (fails closed).
func BuildAgenda(st AgendaStore, sc sensitivity.Scope, now time.Time, days int, open OpenFunc) ([]Event, error) {
	items, err := st.AllItems()
	if err != nil {
		return nil, err
	}
	until := now.Add(time.Duration(clampDays(days)) * 24 * time.Hour)
	out := []Event{}
	for _, it := range items {
		if it.Kind != item.KindEvent || it.Native["cancelled"] == "true" {
			continue
		}
		end := it.End()
		if !end.After(now) || !it.OccurredAt.Before(until) {
			continue
		}
		lab, aud, err := st.ItemLabel(it.ID)
		if err != nil {
			lab, aud = sensitivity.Label{Level: sensitivity.Confidential}, nil
		}
		if !sensitivity.Visible(lab, aud, sc) {
			continue
		}
		ev := Event{ID: it.ID, Start: it.OccurredAt, End: end, Subject: it.Title, Location: it.Native["location"], Attendees: []Attendee{}, Open: []Open{}}
		for _, p := range it.Participants {
			name := p.Name
			if name == "" {
				name = p.Email
			}
			if p.Role == "organizer" {
				ev.Organizer = name
			}
			if !p.Owner && name != "" {
				ev.Attendees = append(ev.Attendees, Attendee{Name: name})
			}
		}
		if b, err := st.PrepBriefFor(sc, it.ID); err == nil && b != nil && b.Text != "" {
			ev.Brief = &Brief{Text: b.Text, MadeAt: b.MadeAt, Sensitivity: labelOf(b.Label), Sources: briefSources(b, sc)}
		}
		// Only an event with other people has a prep on the desktop
		// (PrepTargets), so only such an event has open items.
		if open != nil && len(ev.Attendees) > 0 {
			if ops, err := open(it.ID); err == nil {
				ev.Open = openItems(st, sc, ops)
			}
		}
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func labelOf(l sensitivity.Label) Sensitivity {
	return Sensitivity{Level: l.Level.String(), Share: sensitivity.ShareOf(l), Why: sensitivity.Why(l, 0)}
}

var briefCite = regexp.MustCompile(`\[(\d+)\]`)

// briefSources are the saved brief's sources its text cites, numbered as
// the desktop's Answer numbers them (sources[n-1] is [n]); at most
// maxBriefSources, the first cited first. Uncited sources aren't sent: the
// phone reaches a source only through its mark.
func briefSources(b *store.PrepBrief, sc sensitivity.Scope) []Source {
	seen := map[int]bool{}
	var ns []int
	for _, m := range briefCite.FindAllStringSubmatch(b.Text, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > len(b.Sources) || seen[n] {
			continue
		}
		seen[n] = true
		ns = append(ns, n)
		if len(ns) == maxBriefSources {
			break
		}
	}
	sort.Ints(ns)
	out := []Source{}
	for _, n := range ns {
		h := b.Sources[n-1]
		if !sensitivity.Visible(h.Label, nil, sc) {
			continue
		}
		out = append(out, sourceOf(n, h, "", h.Label))
	}
	return out
}

func sourceOf(n int, h store.Hit, quote string, l sensitivity.Label) Source {
	title := h.Title
	if title == "" {
		title = h.ItemID
	}
	return Source{N: n, Title: clip(title, maxSourceTitle), Kind: h.Kind, OccurredAt: h.OccurredAt,
		Speakers: clip(h.Speakers, maxSourceTitle), Text: passage(h.Text, quote, maxPassage), Sensitivity: labelOf(l)}
}

// openItems turns the prep's open items into the agenda's: the desktop's
// order, at most maxOpen, the statement citations the desktop shows as
// chips taken out of the text, and up to maxOpenSources passages each
// (those cited in the text first, then the item's other statements; one
// per chunk).
func openItems(st AgendaStore, sc sensitivity.Scope, ops []knowledge.PrepOpen) []Open {
	out := []Open{}
	for _, o := range ops {
		if len(out) == maxOpen {
			break
		}
		byID := map[int64]store.ThreadItem{}
		for _, it := range o.Items {
			byID[it.ID] = it
		}
		text, cited := stripItemCites(o.Text, byID)
		order := append([]int64{}, cited...)
		for _, it := range o.Items {
			order = append(order, it.ID)
		}
		x := Open{Text: text, Who: o.Who, Due: o.Due, Thread: o.Name, Quiet: o.Quiet, Overdue: o.Overdue, Private: o.Private, Sources: []Source{}}
		if !o.Raised.IsZero() {
			r := o.Raised
			x.Raised = &r
		}
		chunks := map[int64]bool{}
		for _, id := range order {
			if len(x.Sources) == maxOpenSources {
				break
			}
			it, ok := byID[id]
			if !ok || chunks[it.ChunkID] {
				continue
			}
			chunks[it.ChunkID] = true
			h, err := st.ChunkHit(sc, it.ChunkID)
			if err != nil || h == nil {
				continue
			}
			// The passage is shown, the statement read from it: the source
			// is as sensitive as both.
			x.Sources = append(x.Sources, sourceOf(len(x.Sources)+1, *h, it.Quote, sensitivity.Combine(h.Label, it.Label)))
		}
		out = append(out, x)
	}
	return out
}

// itemCite is the desktop's statement citation (Knowledge.tsx, Cited):
// "(5435)", "[12, 14]", "(20-24)", with the space before it.
var itemCite = regexp.MustCompile(`\s*[(\[]\s*(\d+(?:\s*[-–]\s*\d+)?(?:\s*,\s*\d+(?:\s*[-–]\s*\d+)?)*)\s*[)\]]`)

// stripItemCites removes the citations the desktop would show as chips
// (those naming at least one of the item's statements) and returns the
// statements they name, in order; other brackets stay, as on the desktop.
func stripItemCites(text string, byID map[int64]store.ThreadItem) (string, []int64) {
	var ids []int64
	seen := map[int64]bool{}
	out := itemCite.ReplaceAllStringFunc(text, func(m string) string {
		group := itemCite.FindStringSubmatch(m)[1]
		list := idList(group)
		hit := false
		for _, id := range list {
			if _, ok := byID[id]; ok {
				hit = true
			}
		}
		if !hit {
			return m
		}
		for _, id := range list {
			if _, ok := byID[id]; ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return ""
	})
	return strings.TrimSpace(out), ids
}

// idList reads "12, 14" or a range "20-24" (a range of at most 50 ids), as
// the desktop's idList does.
func idList(s string) []int64 {
	var out []int64
	for _, part := range strings.Split(s, ",") {
		ab := strings.FieldsFunc(part, func(r rune) bool { return r == '-' || r == '–' })
		if len(ab) == 0 {
			continue
		}
		a, err := strconv.ParseInt(strings.TrimSpace(ab[0]), 10, 64)
		if err != nil {
			continue
		}
		if len(ab) == 2 {
			if b, err := strconv.ParseInt(strings.TrimSpace(ab[1]), 10, 64); err == nil && b >= a && b-a < 50 {
				for i := a; i <= b; i++ {
					out = append(out, i)
				}
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// passage is at most max characters of text; a longer one is cut to a
// window that starts a little before quote (the words cited) when text
// has it, marked with "…" where it was cut.
func passage(text, quote string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	r := []rune(text)
	start := 0
	if quote != "" {
		if i := strings.Index(text, quote); i > 0 {
			start = utf8.RuneCountInString(text[:i]) - 200
			if start < 0 {
				start = 0
			}
		}
	}
	end := start + max
	if start > 0 {
		end--
	}
	if end >= len(r) {
		end = len(r)
		// Use the whole budget: move the start back as far as it allows
		// (len(r) > max, so it stays after 0 and the "…" fits).
		start = len(r) - max + 1
	} else {
		end--
	}
	s := string(r[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(r) {
		s += "…"
	}
	return s
}

// clip is s cut to at most max characters.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
