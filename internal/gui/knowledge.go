package gui

import (
	"fmt"
	"time"

	"github.com/anielsud/musubi/internal/config"
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

// CompareWith lines this window's topics up with another dataset's, both
// read under the same scope; reverse lines the other's up with this one's.
func (a *App) CompareWith(key, scope string, reverse bool) (*knowledge.Comparison, error) {
	v, err := a.views(scope)
	if err != nil {
		return nil, err
	}
	d, err := findDataset(key)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(d.Config)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer st.Close()
	other := knowledge.Views{Store: st, Scope: v.Scope, Now: v.Now}
	if reverse {
		return knowledge.CompareDatasets(other, v)
	}
	return knowledge.CompareDatasets(v, other)
}
