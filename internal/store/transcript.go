package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Transcript is an item's lines, as far as a scope may see them.
type Transcript struct {
	ItemID     string
	Title      string
	OccurredAt time.Time
	Lines      []item.Segment
}

// Transcript returns the item's lines that fall in passages the scope may
// see, in order, with their own start times (passages carry only their
// first line's). A passage the scope may not see leaves out all its lines.
// Nil when no passage is visible.
func (s *Store) Transcript(sc sensitivity.Scope, itemID string) (*Transcript, error) {
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	rows, err := s.rdb.Query(`SELECT c.seg_from, c.seg_to FROM chunks c`+chunkVisJoin+`WHERE c.item_id = ? AND `+vis+` ORDER BY c.seq`, append([]any{itemID}, vargs...)...)
	if err != nil {
		return nil, err
	}
	type span struct{ from, to int }
	var spans []span
	for rows.Next() {
		var sp span
		if err := rows.Scan(&sp.from, &sp.to); err != nil {
			rows.Close()
			return nil, err
		}
		spans = append(spans, sp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		return nil, nil
	}
	t := &Transcript{ItemID: itemID}
	var occ int64
	var segs string
	err = s.rdb.QueryRow(`SELECT `+displayTitle("i")+`, i.occurred_at, i.segments FROM items i WHERE i.id = ?`, itemID).Scan(&t.Title, &occ, &segs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.OccurredAt = time.Unix(occ, 0)
	var all []item.Segment
	if err := json.Unmarshal([]byte(segs), &all); err != nil {
		return nil, err
	}
	for _, sp := range spans {
		from, to := max(sp.from, 0), min(sp.to, len(all))
		for i := from; i < to; i++ {
			t.Lines = append(t.Lines, all[i])
		}
	}
	return t, nil
}
