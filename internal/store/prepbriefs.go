package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// PrepBrief is talking points drafted ahead of a meeting (prep_briefs).
// Threads is the drafter's list of subjects drawn on, kept as written.
type PrepBrief struct {
	ItemID  string            `json:"itemId"`
	Input   string            `json:"-"`
	Model   string            `json:"model"`
	Text    string            `json:"text"`
	Sources []Hit             `json:"sources"`
	Threads json.RawMessage   `json:"threads"`
	Label   sensitivity.Label `json:"label"`
	MadeAt  time.Time         `json:"madeAt"`
}

// SavePrepBrief stores (or replaces) an event's talking points; the label
// combines the sources' (confidential with none: unknown fails closed).
func (s *Store) SavePrepBrief(b PrepBrief) error {
	l := sensitivity.Label{Level: sensitivity.Confidential}
	if len(b.Sources) > 0 {
		l = sensitivity.Label{}
		for _, h := range b.Sources {
			l = sensitivity.Combine(l, h.Label)
		}
	}
	src, err := json.Marshal(b.Sources)
	if err != nil {
		return err
	}
	th := b.Threads
	if len(th) == 0 {
		th = json.RawMessage("[]")
	}
	_, err = s.db.Exec(`INSERT INTO prep_briefs(item_id, input, model, text, sources, threads, level, categories, made_at) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(item_id) DO UPDATE SET input = excluded.input, model = excluded.model, text = excluded.text, sources = excluded.sources,
		threads = excluded.threads, level = excluded.level, categories = excluded.categories, made_at = excluded.made_at`,
		b.ItemID, b.Input, b.Model, b.Text, string(src), string(th), int(l.Level), int64(l.Categories), b.MadeAt.Unix())
	return err
}

// PrepBriefFor is an event's stored talking points, nil when there are
// none or the scope may not see them.
func (s *Store) PrepBriefFor(sc sensitivity.Scope, itemID string) (*PrepBrief, error) {
	var b PrepBrief
	var src, th string
	var lvl int
	var cats int64
	var made int64
	err := s.db.QueryRow(`SELECT item_id, input, model, text, sources, threads, level, categories, made_at FROM prep_briefs WHERE item_id = ?`, itemID).
		Scan(&b.ItemID, &b.Input, &b.Model, &b.Text, &src, &th, &lvl, &cats, &made)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Label = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
	if !sensitivity.Visible(b.Label, nil, sc) {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(src), &b.Sources); err != nil {
		return nil, err
	}
	b.Threads, b.MadeAt = json.RawMessage(th), time.Unix(made, 0)
	return &b, nil
}
