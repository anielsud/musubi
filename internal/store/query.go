package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// Query methods. Each takes a Scope and filters in SQL; results carry their
// effective label so callers (and the eval harness) can show or re-check it.

type Hit struct {
	ChunkID    int64
	ItemID     string
	Kind       string
	Title      string
	OccurredAt time.Time
	Seq        int
	Start, End float64
	Speakers   string
	Text       string
	Score      float64
	Label      sensitivity.Label
}

type SearchMode string

const (
	ModeHybrid  SearchMode = "hybrid"
	ModeKeyword SearchMode = "keyword"
	ModeVector  SearchMode = "vector"
)

type SearchQuery struct {
	Text         string
	Vec          []float32 // query embedding; nil for keyword only
	Model        string
	K            int
	Mode         SearchMode
	Since, Until time.Time
}

// Search runs keyword (FTS5/BM25) and vector (exact cosine) retrieval over
// the scope-visible chunks and fuses them by reciprocal rank.
func (s *Store) Search(sc sensitivity.Scope, q SearchQuery) ([]Hit, error) {
	if q.K <= 0 {
		q.K = 10
	}
	if q.Mode == "" {
		q.Mode = ModeHybrid
	}
	// Each list goes deeper than K so fusion has candidates from both.
	depth := max(60, q.K)
	var lists [][]int64
	if q.Mode != ModeVector {
		ids, err := s.keywordIDs(sc, q, depth)
		if err != nil {
			return nil, err
		}
		lists = append(lists, ids)
	}
	if q.Mode != ModeKeyword && q.Vec != nil {
		ids, err := s.vectorIDs(sc, q, depth)
		if err != nil {
			return nil, err
		}
		lists = append(lists, ids)
	}
	// Reciprocal rank fusion: robust to the two scores being on unrelated
	// scales, and needs no tuning beyond the usual k=60.
	score := map[int64]float64{}
	for _, l := range lists {
		for rank, id := range l {
			score[id] += 1 / float64(60+rank+1)
		}
	}
	ids := make([]int64, 0, len(score))
	for id := range score {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if score[ids[i]] != score[ids[j]] {
			return score[ids[i]] > score[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > q.K {
		ids = ids[:q.K]
	}
	hits := make([]Hit, 0, len(ids))
	for _, id := range ids {
		h, err := s.hit(sc, id)
		if err != nil {
			return nil, err
		}
		if h == nil {
			continue
		}
		h.Score = score[id]
		hits = append(hits, *h)
	}
	return hits, nil
}

func timeSQL(q SearchQuery) (string, []any) {
	var parts []string
	var args []any
	if !q.Since.IsZero() {
		parts = append(parts, "i.occurred_at >= ?")
		args = append(args, q.Since.Unix())
	}
	if !q.Until.IsZero() {
		parts = append(parts, "i.occurred_at < ?")
		args = append(args, q.Until.Unix())
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(parts, " AND "), args
}

func (s *Store) keywordIDs(sc sensitivity.Scope, q SearchQuery, depth int) ([]int64, error) {
	match := ftsQuery(q.Text)
	if match == "" {
		return nil, nil
	}
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	ts, targs := timeSQL(q)
	query := `SELECT c.id FROM chunks_fts f JOIN chunks c ON c.id = f.rowid JOIN items i ON i.id = c.item_id` + chunkVisJoin +
		`WHERE chunks_fts MATCH ? AND ` + vis + ts + ` ORDER BY bm25(chunks_fts, 1.0, 0.5) LIMIT ?`
	args := append([]any{match}, vargs...)
	args = append(append(args, targs...), depth)
	return s.ids(query, args...)
}

func (s *Store) vectorIDs(sc sensitivity.Scope, q SearchQuery, depth int) ([]int64, error) {
	b, err := sqlite_vec.SerializeFloat32(q.Vec)
	if err != nil {
		return nil, err
	}
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	ts, targs := timeSQL(q)
	query := `SELECT c.id FROM chunk_vecs v JOIN chunks c ON c.id = v.chunk_id JOIN items i ON i.id = c.item_id` + chunkVisJoin +
		`WHERE v.model = ? AND ` + vis + ts + ` ORDER BY vec_distance_cosine(v.vec, ?) LIMIT ?`
	args := append([]any{q.Model}, vargs...)
	args = append(append(args, targs...), b, depth)
	return s.ids(query, args...)
}

func (s *Store) ids(query string, args ...any) ([]int64, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// hit loads one chunk for display, re-applying the scope. A meeting shows
// the title of the calendar event it was linked to.
func (s *Store) hit(sc sensitivity.Scope, chunkID int64) (*Hit, error) {
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	q := `SELECT c.id, c.item_id, i.kind, coalesce(e.title, i.title), i.occurred_at, c.seq, c.start_s, c.end_s, c.speakers, c.text,
		max(il.level, coalesce(cl.level, 0)), (il.categories | coalesce(cl.categories, 0)), coalesce(cl.reason, il.reason)
		FROM chunks c JOIN items i ON i.id = c.item_id
		LEFT JOIN item_links lk ON lk.item_id = i.id AND lk.rel = 'occurred_as'
		LEFT JOIN items e ON e.id = lk.other_id` + chunkVisJoin + `WHERE c.id = ? AND ` + vis
	args := append([]any{chunkID}, vargs...)
	var h Hit
	var occ int64
	var lvl int
	var cats int64
	err := s.db.QueryRow(q, args...).Scan(&h.ChunkID, &h.ItemID, &h.Kind, &h.Title, &occ, &h.Seq, &h.Start, &h.End, &h.Speakers, &h.Text, &lvl, &cats, &h.Label.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.OccurredAt = time.Unix(occ, 0)
	h.Label.Level, h.Label.Categories = sensitivity.Level(lvl), sensitivity.Categories(cats)
	return &h, nil
}

var ftsStop = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true, "did": true, "do": true,
	"for": true, "from": true, "how": true, "i": true, "in": true, "is": true, "it": true, "of": true, "on": true, "or": true,
	"that": true, "the": true, "this": true, "to": true, "was": true, "we": true, "what": true, "when": true, "where": true,
	"who": true, "why": true, "with": true, "about": true, "our": true, "they": true, "said": true, "say": true,
}

// ftsQuery turns free text into an FTS5 OR query of quoted terms, so user
// input can never be parsed as FTS syntax.
func ftsQuery(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var terms []string
	seen := map[string]bool{}
	for _, w := range words {
		if ftsStop[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
	}
	return strings.Join(terms, " OR ")
}

// EntityRef is an entity as a query result.
type EntityRef struct {
	ID, Type, Name string
	Items          int
	LastSeen       time.Time
}

type ItemRef struct {
	ID, Kind, Title string
	OccurredAt      time.Time
	Mentions        int
	Participant     bool
	Label           sensitivity.Label
}

type Graph struct {
	Entity    EntityRef
	Items     []ItemRef
	Neighbors []EntityRef
}

// FindEntities resolves a name or id to entities that are visible in scope:
// curated and directory entities always, others only if some visible item
// links to them (an entity's existence can itself be sensitive).
func (s *Store) FindEntities(sc sensitivity.Scope, q string) ([]EntityRef, error) {
	norm := NormalizeAlias(q)
	rows, err := s.db.Query(`SELECT DISTINCT e.id, e.type, e.name, e.origin, e.level FROM entities e LEFT JOIN entity_aliases a ON a.entity_id = e.id
		WHERE e.id = ? OR a.alias = ? OR lower(e.name) = lower(?) ORDER BY e.id`, q, norm, q)
	if err != nil {
		return nil, err
	}
	var cands []Entity
	for rows.Next() {
		var e Entity
		var lvl int
		rows.Scan(&e.ID, &e.Type, &e.Name, &e.Origin, &lvl)
		e.Level = sensitivity.Level(lvl)
		cands = append(cands, e)
	}
	rows.Close()
	var out []EntityRef
	for _, e := range cands {
		ok, err := s.entityVisible(sc, e)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, EntityRef{ID: e.ID, Type: e.Type, Name: e.Name})
		}
	}
	return out, nil
}

func (s *Store) entityVisible(sc sensitivity.Scope, e Entity) (bool, error) {
	if e.Level > sc.MaxLevel || (len(sc.Audience) > 0 && e.Level > sensitivity.OpenLevel) {
		return false, nil
	}
	if e.Origin == "registry" || e.Origin == "directory" {
		return true, nil
	}
	items, err := s.entityItems(sc, e.ID, 1)
	return len(items) > 0, err
}

// entityItems lists visible items where the entity is mentioned (in a
// visible chunk) or takes part (in a visible item), newest first.
func (s *Store) entityItems(sc sensitivity.Scope, entityID string, limit int) ([]ItemRef, error) {
	cvis, cargs := visSQL(sc, "il", "cl", "c.item_id")
	ivis, iargs := visSQL(sc, "il", "", "ie.item_id")
	q := `WITH m AS (
		SELECT c.item_id, count(*) n FROM mentions mm JOIN chunks c ON c.id = mm.chunk_id` + chunkVisJoin + `
		WHERE mm.entity_id = ? AND ` + cvis + ` GROUP BY c.item_id
	), p AS (
		SELECT DISTINCT ie.item_id FROM item_entities ie JOIN labels il ON il.item_id = ie.item_id AND il.chunk_seq = -1
		WHERE ie.entity_id = ? AND ` + ivis + `
	), u AS (
		SELECT item_id FROM m UNION SELECT item_id FROM p
	)
	SELECT i.id, i.kind, coalesce(e.title, i.title), i.occurred_at, coalesce(m.n, 0), p.item_id IS NOT NULL, il.level, il.categories
	FROM u JOIN items i ON i.id = u.item_id
	JOIN labels il ON il.item_id = i.id AND il.chunk_seq = -1
	LEFT JOIN m ON m.item_id = i.id LEFT JOIN p ON p.item_id = i.id
	LEFT JOIN item_links lk ON lk.item_id = i.id AND lk.rel = 'occurred_as' LEFT JOIN items e ON e.id = lk.other_id
	ORDER BY i.occurred_at DESC LIMIT ?`
	args := append([]any{entityID}, cargs...)
	args = append(args, entityID)
	args = append(args, iargs...)
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemRef
	for rows.Next() {
		var r ItemRef
		var occ int64
		var lvl int
		var cats int64
		if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &occ, &r.Mentions, &r.Participant, &lvl, &cats); err != nil {
			return nil, err
		}
		r.OccurredAt = time.Unix(occ, 0)
		r.Label = sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EntityGraph returns an entity's visible items and the entities that share
// them, ranked by how many items they share.
func (s *Store) EntityGraph(sc sensitivity.Scope, entityID string, exclude []string, limit int) (*Graph, error) {
	var e Entity
	var lvl int
	err := s.db.QueryRow(`SELECT id, type, name, origin, level FROM entities WHERE id = ?`, entityID).Scan(&e.ID, &e.Type, &e.Name, &e.Origin, &lvl)
	e.Level = sensitivity.Level(lvl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ok, err := s.entityVisible(sc, e); err != nil || !ok {
		return nil, err
	}
	items, err := s.entityItems(sc, entityID, 100000)
	if err != nil {
		return nil, err
	}
	g := &Graph{Entity: EntityRef{ID: e.ID, Type: e.Type, Name: e.Name, Items: len(items)}, Items: items}
	if len(items) > 0 {
		g.Entity.LastSeen = items[0].OccurredAt
	}
	skip := map[string]bool{entityID: true}
	for _, x := range exclude {
		skip[x] = true
	}
	when := map[string]time.Time{}
	for _, it := range items {
		when[it.ID] = it.OccurredAt
	}
	// Neighbours are counted per shared item, through links that are
	// themselves visible: a mention in a hidden chunk of a visible meeting
	// doesn't count.
	type agg struct {
		ref   EntityRef
		items map[string]bool
	}
	nb := map[string]*agg{}
	add := func(id, typ, name, itemID string) {
		if skip[id] {
			return
		}
		a := nb[id]
		if a == nil {
			a = &agg{ref: EntityRef{ID: id, Type: typ, Name: name}, items: map[string]bool{}}
			nb[id] = a
		}
		a.items[itemID] = true
		if t := when[itemID]; t.After(a.ref.LastSeen) {
			a.ref.LastSeen = t
		}
	}
	for _, it := range items {
		cvis, cargs := visSQL(sc, "il", "cl", "c.item_id")
		ivis, iargs := visSQL(sc, "il", "", "ie.item_id")
		q := `SELECT en.id, en.type, en.name, en.level FROM mentions mm JOIN chunks c ON c.id = mm.chunk_id` + chunkVisJoin + `
			JOIN entities en ON en.id = mm.entity_id WHERE c.item_id = ? AND ` + cvis + `
			UNION
			SELECT en.id, en.type, en.name, en.level FROM item_entities ie JOIN labels il ON il.item_id = ie.item_id AND il.chunk_seq = -1
			JOIN entities en ON en.id = ie.entity_id WHERE ie.item_id = ? AND ` + ivis
		args := append([]any{it.ID}, cargs...)
		args = append(args, it.ID)
		args = append(args, iargs...)
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, typ, name string
			var lvl int
			rows.Scan(&id, &typ, &name, &lvl)
			if l := sensitivity.Level(lvl); l > sc.MaxLevel || (len(sc.Audience) > 0 && l > sensitivity.OpenLevel) {
				continue
			}
			add(id, typ, name, it.ID)
		}
		rows.Close()
	}
	for _, a := range nb {
		a.ref.Items = len(a.items)
		g.Neighbors = append(g.Neighbors, a.ref)
	}
	sort.Slice(g.Neighbors, func(i, j int) bool {
		a, b := g.Neighbors[i], g.Neighbors[j]
		if a.Items != b.Items {
			return a.Items > b.Items
		}
		return a.ID < b.ID
	})
	if limit > 0 && len(g.Neighbors) > limit {
		g.Neighbors = g.Neighbors[:limit]
	}
	if limit > 0 && len(g.Items) > limit {
		g.Items = g.Items[:limit]
	}
	return g, nil
}

// Audit records a scoped read made for an outbound purpose.
func (s *Store) Audit(sc sensitivity.Scope, op, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit(at, scope, op, detail) VALUES (?,?,?,?)`, time.Now().Unix(), sc.Name, op, detail)
	return err
}

// ChunkLabel returns a chunk's effective label and its item's audience. It
// is the eval harness's independent check on what Search returned, so it
// reads labels directly rather than through visSQL.
func (s *Store) ChunkLabel(chunkID int64) (sensitivity.Label, []string, error) {
	var lvl int
	var cats int64
	var itemID string
	err := s.db.QueryRow(`SELECT c.item_id, max(il.level, coalesce(cl.level, 0)), (il.categories | coalesce(cl.categories, 0))
		FROM chunks c`+chunkVisJoin+`WHERE c.id = ?`, chunkID).Scan(&itemID, &lvl, &cats)
	if err != nil {
		return sensitivity.Label{}, nil, fmt.Errorf("chunk %d has no label: %w", chunkID, err)
	}
	aud, err := s.audience(itemID)
	return sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}, aud, err
}

// ItemLabel is ChunkLabel for item-level results.
func (s *Store) ItemLabel(itemID string) (sensitivity.Label, []string, error) {
	var lvl int
	var cats int64
	err := s.db.QueryRow(`SELECT level, categories FROM labels WHERE item_id = ? AND chunk_seq = -1`, itemID).Scan(&lvl, &cats)
	if err != nil {
		return sensitivity.Label{}, nil, fmt.Errorf("item %s has no label: %w", itemID, err)
	}
	aud, err := s.audience(itemID)
	return sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}, aud, err
}

func (s *Store) audience(itemID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT principal FROM item_audience WHERE item_id = ?`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		out = append(out, p)
	}
	return out, rows.Err()
}

// NormalizeAlias folds case and punctuation the way entity_aliases stores
// them.
func NormalizeAlias(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "'s ", " ")
	s = strings.TrimSuffix(s, "'s")
	f := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '&' })
	return strings.Join(f, " ")
}
