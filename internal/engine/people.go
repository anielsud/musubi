package engine

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/m365"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// The people views are owner-only: who the owner meets, what colleagues
// look and sound like, and what is said about them are the owner's own.

// PersonRow is one person in the People list.
type PersonRow struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email,omitempty"`
	Title        string    `json:"title,omitempty"`
	Department   string    `json:"department,omitempty"`
	Together     int       `json:"together"`
	LastTogether time.Time `json:"lastTogether"`
}

// person is what the people views know about one person while reading
// items: how to recognise them, and what they were part of.
type person struct {
	id, name, email string
	names           map[string]bool // normalised names and aliases
	dir             *m365.Person
	meetings        []*item.Item // meetings and events, deduplicated
}

// normName folds a name for matching: lower case, letters and digits,
// single spaces ("Vale, Robin" and "Robin Vale" stay distinct; both are
// added when the directory has given name and surname).
func normName(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// people reads every person entity with their names, and which meetings
// and events each took part in. A recording linked to a calendar event
// counts once (as the event), as does a Teams copy.
func (e *Engine) people() (map[string]*person, []item.Item, error) {
	ents, err := e.Store.EntityNames()
	if err != nil {
		return nil, nil, err
	}
	aliases, err := e.Store.AllAliases()
	if err != nil {
		return nil, nil, err
	}
	dir, err := m365.LoadDirectory(e.Cfg.DirectoryPath())
	if err != nil {
		return nil, nil, err
	}
	owner := strings.ToLower(e.Cfg.Owner.Email)
	ps := map[string]*person{}
	for id, nt := range ents {
		if nt[1] != "person" || id == "person:"+owner {
			continue
		}
		p := &person{id: id, name: nt[0], names: map[string]bool{}}
		if em, ok := strings.CutPrefix(id, "person:"); ok && strings.Contains(em, "@") {
			p.email = em
			if d := dir.People[em]; d != nil && !d.Missing {
				p.dir = d
				for _, n := range []string{d.Name, d.GivenName + " " + d.Surname} {
					if k := normName(n); k != "" {
						p.names[k] = true
					}
				}
			}
		}
		if k := normName(p.name); k != "" {
			p.names[k] = true
		}
		ps[id] = p
	}
	for _, a := range aliases {
		if p := ps[a.EntityID]; p != nil && a.Kind == "full" && strings.Contains(a.Alias, " ") {
			p.names[normName(a.Alias)] = true
		}
	}
	byEmail, byName := map[string]*person{}, map[string][]*person{}
	for _, p := range ps {
		if p.email != "" {
			byEmail[p.email] = p
		}
		for n := range p.names {
			byName[n] = append(byName[n], p)
		}
	}
	items, err := e.Store.AllItems()
	if err != nil {
		return nil, nil, err
	}
	links, err := e.Store.LinksFrom("occurred_as")
	if err != nil {
		return nil, nil, err
	}
	for i := range items {
		it := &items[i]
		if it.Kind != item.KindMeeting && it.Kind != item.KindEvent || links[it.ID] != "" {
			continue // a linked recording counts as its event
		}
		if it.Kind == item.KindEvent && (it.Native["cancelled"] == "true" || it.Native["allDay"] == "true") {
			continue
		}
		seen := map[*person]bool{}
		add := func(p *person) {
			if p != nil && !seen[p] {
				seen[p] = true
				p.meetings = append(p.meetings, it)
			}
		}
		for _, pt := range it.Participants {
			if pt.Owner {
				continue
			}
			if pt.Email != "" {
				add(byEmail[strings.ToLower(pt.Email)])
			} else if c := byName[normName(pt.Name)]; len(c) == 1 {
				add(c[0])
			}
		}
	}
	return ps, items, nil
}

// People lists the people the owner meets (or the directory knows), most
// met first.
func (e *Engine) People() ([]PersonRow, error) {
	ps, _, err := e.people()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []PersonRow
	for _, p := range ps {
		if len(p.meetings) == 0 && p.dir == nil {
			continue
		}
		r := PersonRow{ID: p.id, Name: p.name, Email: p.email, Together: len(p.meetings)}
		if p.dir != nil {
			r.Title, r.Department = p.dir.Title, p.dir.Department
		}
		for _, m := range p.meetings {
			if m.OccurredAt.Before(now) && m.OccurredAt.After(r.LastTogether) {
				r.LastTogether = m.OccurredAt
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Together != out[b].Together {
			return out[a].Together > out[b].Together
		}
		return out[a].Name < out[b].Name
	})
	return out, nil
}

// PeopleNames maps each normalised name or alias that means exactly one
// person to that person, so the app can make names into links.
func (e *Engine) PeopleNames() (map[string]string, error) {
	ps, _, err := e.people()
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	out := map[string]string{}
	for _, p := range ps {
		for n := range p.names {
			count[n]++
			out[n] = p.id
		}
		if p.email != "" {
			out[p.email] = p.id
		}
	}
	for n, c := range count {
		if c > 1 {
			delete(out, n)
		}
	}
	if e.Cfg.Owner.Name != "" {
		delete(out, normName(e.Cfg.Owner.Name))
	}
	return out, nil
}

// MeetingRef is a meeting on a person page.
type MeetingRef struct {
	ItemID string    `json:"itemId"`
	Title  string    `json:"title"`
	At     time.Time `json:"at"`
}

// PersonRef names another person.
type PersonRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// seenWith ranks who shares meetings with p. Each shared meeting adds
// 1/(people in it - 1), so a one-to-one or a small working session counts
// for far more than a company-wide call; the owner isn't counted (they're
// in all of them).
func seenWith(p *person, ps map[string]*person, n int) []PersonRef {
	in := map[string][]*person{} // meeting → the people in it
	for _, q := range ps {
		for _, m := range q.meetings {
			in[m.ID] = append(in[m.ID], q)
		}
	}
	score := map[*person]float64{}
	for _, m := range p.meetings {
		others := in[m.ID]
		if len(others) < 2 {
			continue
		}
		w := 1 / float64(len(others)-1)
		for _, q := range others {
			if q != p {
				score[q] += w
			}
		}
	}
	var qs []*person
	for q, sc := range score {
		if sc >= 0.5 { // at least half a small meeting's worth
			qs = append(qs, q)
		}
	}
	sort.Slice(qs, func(a, b int) bool {
		if score[qs[a]] != score[qs[b]] {
			return score[qs[a]] > score[qs[b]]
		}
		return qs[a].name < qs[b].name
	})
	var out []PersonRef
	for _, q := range qs[:min(n, len(qs))] {
		out = append(out, PersonRef{ID: q.id, Name: q.name})
	}
	return out
}

// TopicRef is a thread a person comes up in.
type TopicRef struct {
	ID     int64  `json:"id"`
	Label  string `json:"label"`
	Claims int    `json:"claims"`
}

// PersonPage is everything the person view shows.
type PersonPage struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Email   string         `json:"email,omitempty"`
	Dir     *m365.Person   `json:"directory,omitempty"`
	Chain   []*m365.Person `json:"chain"`
	Reports []*m365.Person `json:"reports"`
	Peers   []*m365.Person `json:"peers"` // others with the same manager
	// SeenWith are the people most often in the same meetings, small
	// meetings counting for more.
	SeenWith []PersonRef      `json:"seenWith"`
	Together int              `json:"together"`
	First    time.Time        `json:"first"`
	Recent   []MeetingRef     `json:"recent"`
	Upcoming []MeetingRef     `json:"upcoming"`
	Topics   []TopicRef       `json:"topics"`
	ToDos    []knowledge.ToDo `json:"todos"`
	Names    []string         `json:"names"`
	Headshot string           `json:"headshot,omitempty"` // data: URL
	Voice    *VoiceClip       `json:"voice,omitempty"`
}

// PersonPage gathers a person's page; nil when the entity isn't a person.
func (e *Engine) PersonPage(entityID string) (*PersonPage, error) {
	ps, items, err := e.people()
	if err != nil {
		return nil, err
	}
	p := ps[entityID]
	if p == nil {
		return nil, nil
	}
	pg := &PersonPage{ID: p.id, Name: p.name, Email: p.email, Dir: p.dir, Together: len(p.meetings)}
	for n := range p.names {
		pg.Names = append(pg.Names, n)
	}
	sort.Strings(pg.Names)
	if p.dir != nil {
		dir, _ := m365.LoadDirectory(e.Cfg.DirectoryPath())
		pg.Chain, pg.Reports = dir.Chain(p.email), dir.Reports(p.email)
		// Peers: the manager's other direct reports. Someone in the chain
		// above (a manager the directory lists as reporting to themselves,
		// as a top executive can be) isn't a peer.
		if p.dir.ManagerEmail != "" {
			above := map[string]bool{}
			for _, c := range pg.Chain {
				above[c.Email] = true
			}
			for _, q := range dir.Reports(p.dir.ManagerEmail) {
				if q.Email != p.email && !above[q.Email] {
					pg.Peers = append(pg.Peers, q)
				}
			}
		}
	}
	now := time.Now()
	var past, future []*item.Item
	for _, m := range p.meetings {
		if pg.First.IsZero() || m.OccurredAt.Before(pg.First) {
			pg.First = m.OccurredAt
		}
		if m.OccurredAt.After(now) {
			future = append(future, m)
		} else {
			past = append(past, m)
		}
	}
	sort.Slice(past, func(a, b int) bool { return past[a].OccurredAt.After(past[b].OccurredAt) })
	sort.Slice(future, func(a, b int) bool { return future[a].OccurredAt.Before(future[b].OccurredAt) })
	refs := func(ms []*item.Item, n int) []MeetingRef {
		var ids []string
		for _, m := range ms[:min(n, len(ms))] {
			ids = append(ids, m.ID)
		}
		titles, _ := e.Store.ItemTitles(sensitivity.Owner(), ids)
		var out []MeetingRef
		seen := map[string]bool{}
		for _, m := range ms[:min(n, len(ms))] {
			t := titles[m.ID].Title
			if t == "" {
				t = m.Title
			}
			// The same meeting can be on the calendar twice (a forwarded
			// invitation, a Teams copy): once is enough.
			if k := t + "|" + m.OccurredAt.Format("2006-01-02 15"); !seen[k] {
				seen[k] = true
				out = append(out, MeetingRef{ItemID: m.ID, Title: t, At: m.OccurredAt})
			}
		}
		return out
	}
	pg.Recent, pg.Upcoming = refs(past, 10), refs(future, 5)
	pg.SeenWith = seenWith(p, ps, 8)
	if pg.Topics, err = e.entityTopics(p.id, pg.Names, 8); err != nil {
		return nil, err
	}
	pg.ToDos = e.personToDos(p)
	pg.Headshot = e.headshot(p)
	pg.Voice = e.voiceClip(p, items)
	return pg, nil
}

func (e *Engine) entityTopics(id string, names []string, n int) ([]TopicRef, error) {
	rows, err := e.Store.EntityTopicRefs(id, names, n)
	if err != nil {
		return nil, err
	}
	var out []TopicRef
	for _, r := range rows {
		out = append(out, TopicRef{ID: r.ID, Label: r.Label, Claims: r.Claims})
	}
	return out, nil
}

// personToDos are the open to-dos the person owns.
func (e *Engine) personToDos(p *person) []knowledge.ToDo {
	v := knowledge.Views{Store: e.Store, Scope: sensitivity.Owner(), Now: time.Now()}
	req := knowledge.ToDoRequest{QuietAfter: e.Cfg.Prep.QuietAfter()}
	if e.Cfg.Knowledge.Digest.ToDos {
		req.Source = "digest"
	}
	all, err := v.ToDos(req, e.Cfg.Owner.Name)
	if err != nil {
		return nil
	}
	var out []knowledge.ToDo
	for _, t := range all {
		if t.State != "open" && t.State != "quiet" {
			continue
		}
		for _, o := range t.Owners {
			if p.names[normName(o)] {
				out = append(out, t)
				break
			}
		}
		if len(out) >= 10 {
			break
		}
	}
	return out
}
