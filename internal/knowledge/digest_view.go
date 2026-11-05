package knowledge

import (
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// MeetingToDo is a to-do said in a meeting, with how it stands now.
type MeetingToDo struct {
	store.DigestTodo
	// State is read as the to-do view reads it: open, quiet, proposed,
	// done, changed, not; "repeated" when a later meeting said it again
	// and it is listed there; "declined" when the meeting turned it down.
	State string `json:"state"`
	Due   string `json:"due,omitempty"`
	// Topic is the thread the to-do is on (the statement it matched), 0
	// for none.
	Topic      int64  `json:"topic,omitempty"`
	TopicLabel string `json:"topicLabel,omitempty"`
}

// MeetingPage is a meeting as the explorer shows it: its summary in
// sections, its to-dos, and the threads its statements are in.
type MeetingPage struct {
	ItemID   string                `json:"itemId"`
	Title    string                `json:"title"`
	Start    time.Time             `json:"start"`
	Overview string                `json:"overview"`
	Sections []store.DigestSection `json:"sections"`
	ToDos    []MeetingToDo         `json:"todos"`
	Topics   []store.MeetingTopic  `json:"topics"`
}

// MeetingPage returns the meeting's digest as far as the scope may see it,
// nil when there is none. Topics are the threads holding at least
// minStatements of its statements, derived now from the statements'
// memberships (so they follow rethreading and the owner's merges and
// splits); a thread whose label the scope may not see is left out.
func (v Views) MeetingPage(itemID string, minStatements int, quietAfter time.Duration, ownerName string) (*MeetingPage, error) {
	d, err := v.Store.MeetingDigest(v.Scope, itemID)
	if err != nil || d == nil {
		return nil, err
	}
	page := &MeetingPage{ItemID: d.ItemID, Title: d.ItemTitle, Start: d.Start, Overview: d.Overview, Sections: d.Sections}
	listed, err := v.ToDos(ToDoRequest{Proposals: true, QuietAfter: quietAfter, Source: "digest"}, ownerName)
	if err != nil {
		return nil, err
	}
	byDigest := map[int64]ToDo{}
	for _, t := range listed {
		if t.Digest != 0 {
			byDigest[t.Digest] = t
		}
	}
	for _, t := range d.Todos {
		mt := MeetingToDo{DigestTodo: t, State: "repeated"}
		if l, ok := byDigest[t.ID]; ok {
			mt.State, mt.Due, mt.Topic, mt.TopicLabel = l.State, l.Due, l.Topic, l.TopicLabel
		}
		if t.Agreed == "declined" {
			mt.State = "declined"
		}
		page.ToDos = append(page.ToDos, mt)
	}
	topics, err := v.Store.MeetingTopics(v.Scope, itemID, minStatements)
	if err != nil {
		return nil, err
	}
	for _, t := range topics {
		if labelVisible(v.Scope, sensitivity.Level(t.LabelLevel)) {
			page.Topics = append(page.Topics, t)
		}
	}
	return page, nil
}

// MeetingList lists the meetings with a digest the scope may see, newest
// first.
func (v Views) MeetingList(limit int) ([]store.DigestedItem, error) {
	return v.Store.DigestedItems(v.Scope, limit)
}
