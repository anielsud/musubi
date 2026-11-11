package gui

import (
	"fmt"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/store"
)

func (a *App) views(scope string) (knowledge.Views, error) {
	if a.eng == nil {
		return knowledge.Views{}, fmt.Errorf("not ready")
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return knowledge.Views{}, err
	}
	return knowledge.Views{Store: a.eng.Store, Scope: sc, Now: time.Now()}, nil
}

// KnowledgeCounts says how far the knowledge layer has been built.
func (a *App) KnowledgeCounts() (store.KnowledgeCounts, error) {
	if a.eng == nil {
		return store.KnowledgeCounts{}, fmt.Errorf("not ready")
	}
	return a.eng.Store.KnowledgeCounts()
}

// Topics is the topic map.
func (a *App) Topics(scope string) ([]knowledge.TopicSummary, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	return v.Topics()
}

// Topic is one topic's page.
func (a *App) Topic(id int64, scope string) (*knowledge.TopicView, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	return v.Topic(id)
}

// Claim is one claim with its evidence and relations.
func (a *App) Claim(id int64, scope string) (*knowledge.ClaimView, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	return v.Claim(id)
}

// SearchClaims finds claims by words.
func (a *App) SearchClaims(q, scope string) ([]knowledge.ClaimView, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	return v.SearchClaims(q, 20)
}

// Judge records the owner's verdict ("verified", "disputed", "false", or ""
// to clear) on the claim a statement belongs to.
func (a *App) Judge(statementID int64, verdict, note string) error {
	if a.eng == nil {
		return fmt.Errorf("not ready")
	}
	return a.eng.Store.Judge(statementID, verdict, note)
}

// TopicMap is what the topic views draw: topics, links, clusters, activity.
func (a *App) TopicMap(scope string) (*knowledge.TopicMap, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	return v.Map()
}

// ToDos lists what was said would or should happen, and what came of it;
// an open item quiet as long as prep.quiet_days is shown as gone quiet.
func (a *App) ToDos(proposals bool, scope string) ([]knowledge.ToDo, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	req := knowledge.ToDoRequest{Proposals: proposals, QuietAfter: a.eng.Cfg.Prep.QuietAfter()}
	if a.eng.Cfg.Knowledge.Digest.ToDos {
		req.Source = "digest"
	}
	return v.ToDos(req, a.eng.Cfg.Owner.Name)
}

// MeetingList lists the meetings with a summary, newest first, as far as
// the scope may see them (knowledge.digest).
func (a *App) MeetingList(scope string, limit int) ([]store.DigestedItem, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	return v.MeetingList(limit)
}

// MeetingPage is one meeting: its summary in sections, its to-dos and how
// they stand, and the threads its statements are in. Nil when the meeting
// has no summary, or the scope can't see it.
func (a *App) MeetingPage(itemID, scope string) (*knowledge.MeetingPage, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	return v.MeetingPage(itemID, a.eng.Cfg.Knowledge.Digest.MinTopicStatements, a.eng.Cfg.Prep.QuietAfter(), a.eng.Cfg.Owner.Name)
}

// LinkToDo records the owner's word on a to-do: a link to another (same,
// replaces, part) or a mark (done, dropped, not, due, who; other 0), with
// a note (for due, the date as 2006-01-02, empty for none; for who, the
// names). A negative id is a meeting summary's to-do no statement matches.
// The owner's view only.
func (a *App) LinkToDo(id int64, rel string, other int64, note string) (int64, error) {
	if a.eng == nil {
		return 0, fmt.Errorf("not ready")
	}
	return a.eng.Store.LinkStatements(id, rel, other, strings.TrimSpace(note), "owner")
}

// UndoToDoLink undoes one of the owner's links or marks.
func (a *App) UndoToDoLink(id int64) error {
	if a.eng == nil {
		return fmt.Errorf("not ready")
	}
	return a.eng.Store.UndoOwnerLink(id)
}
