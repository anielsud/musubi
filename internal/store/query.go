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
	ChunkID    int64             `json:"chunkId"`
	ItemID     string            `json:"itemId"`
	Kind       string            `json:"kind"`
	Title      string            `json:"title"`
	OccurredAt time.Time         `json:"occurredAt"`
	Seq        int               `json:"seq"`
	Start      float64           `json:"start"`
	End        float64           `json:"end"`
	Speakers   string            `json:"speakers"`
	Text       string            `json:"text"`
	Score      float64           `json:"score"`
	Label      sensitivity.Label `json:"label"`
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
	// Exclude drops items, e.g. the meeting in progress, so the live panel
	// doesn't show the conversation back to itself.
	Exclude []string
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
	if len(q.Exclude) > 0 {
		parts = append(parts, "i.id NOT IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Exclude)), ",")+")")
		for _, id := range q.Exclude {
			args = append(args, id)
		}
	}
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
		`WHERE chunks_fts MATCH ? AND ` + vis + ts + ` ORDER BY bm25(chunks_fts, 1.0, 0.5, 0.5) LIMIT ?`
	args := append([]any{match}, vargs...)
	args = append(append(args, targs...), depth)
	return s.ids(query, args...)
}

// vectorIDs returns the visible passages nearest the query vector. On a
// large corpus the code index proposes candidates (nearest by their 1-bit
// codes) and only those are compared exactly; when the scope or time
// filter leaves too few of them, it asks for more, and falls back to the
// exact scan over everything.
func (s *Store) vectorIDs(sc sensitivity.Scope, q SearchQuery, depth int) ([]int64, error) {
	b, err := sqlite_vec.SerializeFloat32(q.Vec)
	if err != nil {
		return nil, err
	}
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	ts, targs := timeSQL(q)
	exact := func() ([]int64, error) {
		query := `SELECT c.id FROM chunk_vecs v JOIN chunks c ON c.id = v.chunk_id JOIN items i ON i.id = c.item_id` + chunkVisJoin +
			`WHERE v.model = ? AND ` + vis + ts + ` ORDER BY vec_distance_cosine(v.vec, ?) LIMIT ?`
		args := append([]any{q.Model}, vargs...)
		args = append(append(args, targs...), b, depth)
		return s.ids(query, args...)
	}
	ix, err := s.passageIndex(q.Model)
	if err != nil || ix.Len() < exactBelow {
		return exact()
	}
	// On real embeddings 1-bit codes are coarser than on random vectors:
	// 7.5 candidates per result held 97% of the exact top 10 (research
	// log, 2026-10-16), so take twenty, or at least 1,000 (~3 ms to
	// re-rank).
	// The candidates must hold many visible passages, not just enough: when
	// the scope hides most of the nearest ones, the few visible among them
	// aren't the nearest visible overall. Widen until twenty per result are
	// visible, else scan exactly (over only what the scope sees).
	need := 20 * depth
	for k := max(1000, need); ; k *= 4 {
		if k >= ix.Len() || k > 20000 {
			return exact()
		}
		cands := ix.Nearest(q.Vec, k)
		ph := strings.TrimSuffix(strings.Repeat("?,", len(cands)), ",")
		query := `SELECT c.id FROM chunk_vecs v JOIN chunks c ON c.id = v.chunk_id JOIN items i ON i.id = c.item_id` + chunkVisJoin +
			`WHERE v.model = ? AND v.chunk_id IN (` + ph + `) AND ` + vis + ts + ` ORDER BY vec_distance_cosine(v.vec, ?)`
		args := []any{q.Model}
		for _, id := range cands {
			args = append(args, id)
		}
		args = append(args, vargs...)
		args = append(append(args, targs...), b)
		ids, err := s.ids(query, args...)
		if err != nil {
			return nil, err
		}
		if len(ids) >= need {
			return ids[:depth], nil
		}
	}
}

func (s *Store) ids(query string, args ...any) ([]int64, error) {
	rows, err := s.rdb.Query(query, args...)
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
	q := `SELECT c.id, c.item_id, i.kind, ` + displayTitle("i") + `, i.occurred_at, c.seq, c.start_s, c.end_s, c.speakers, c.text,
		max(il.level, coalesce(cl.level, 0)), (il.categories | coalesce(cl.categories, 0)), coalesce(cl.reason, il.reason)
		FROM chunks c JOIN items i ON i.id = c.item_id
		LEFT JOIN item_links lk ON lk.item_id = i.id AND lk.rel = 'occurred_as'
		LEFT JOIN items e ON e.id = lk.other_id` + chunkVisJoin + `WHERE c.id = ? AND ` + vis
	args := append([]any{chunkID}, vargs...)
	var h Hit
	var occ int64
	var lvl int
	var cats int64
	err := s.rdb.QueryRow(q, args...).Scan(&h.ChunkID, &h.ItemID, &h.Kind, &h.Title, &occ, &h.Seq, &h.Start, &h.End, &h.Speakers, &h.Text, &lvl, &cats, &h.Label.Reason)
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
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Name     string    `json:"name"`
	Items    int       `json:"items"`
	LastSeen time.Time `json:"lastSeen"`
}

type ItemRef struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Title       string            `json:"title"`
	OccurredAt  time.Time         `json:"occurredAt"`
	Mentions    int               `json:"mentions"`
	Participant bool              `json:"participant"`
	Label       sensitivity.Label `json:"label"`
}

type Graph struct {
	Entity    EntityRef   `json:"entity"`
	Items     []ItemRef   `json:"items"`
	Neighbors []EntityRef `json:"neighbors"`
}

// FindEntities resolves a name or id to entities that are visible in scope:
// curated and directory entities always, others only if some visible item
// links to them (an entity's existence can itself be sensitive).
func (s *Store) FindEntities(sc sensitivity.Scope, q string) ([]EntityRef, error) {
	norm := NormalizeAlias(q)
	lq := strings.ToLower(strings.TrimSpace(q))
	// Exact matches first (an id, a bare email or domain, an alias, a
	// name), then names containing the text, for typing "dana" or "acme".
	rows, err := s.rdb.Query(`SELECT e.id, e.type, e.name, e.origin, e.level, min(r) FROM (
			SELECT e.id, 0 r FROM entities e WHERE e.id IN (?, 'person:' || ?, 'org:' || ?, 'term:' || ?)
			UNION ALL SELECT a.entity_id, 1 FROM entity_aliases a WHERE a.alias = ?
			UNION ALL SELECT e.id, 1 FROM entities e WHERE lower(e.name) = ?
			UNION ALL SELECT e.id, 2 FROM entities e WHERE length(?) >= 3 AND lower(e.name) LIKE '%' || ? || '%'
		) m JOIN entities e ON e.id = m.id WHERE e.type != 'group'
		GROUP BY e.id ORDER BY min(r), e.name LIMIT 12`, q, lq, lq, lq, norm, lq, lq, lq)
	if err != nil {
		return nil, err
	}
	var cands []Entity
	for rows.Next() {
		var e Entity
		var lvl, rank int
		rows.Scan(&e.ID, &e.Type, &e.Name, &e.Origin, &lvl, &rank)
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
	if e.Level > sc.MaxLevel || (len(sc.Audience) > 0 && e.Level > sc.Open) {
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
	SELECT i.id, i.kind, ` + displayTitle("i") + `, i.occurred_at, coalesce(m.n, 0), p.item_id IS NOT NULL, il.level, il.categories
	FROM u JOIN items i ON i.id = u.item_id
	JOIN labels il ON il.item_id = i.id AND il.chunk_seq = -1
	LEFT JOIN m ON m.item_id = i.id LEFT JOIN p ON p.item_id = i.id
	LEFT JOIN item_links lk ON lk.item_id = i.id AND lk.rel = 'occurred_as' LEFT JOIN items e ON e.id = lk.other_id
	ORDER BY i.occurred_at DESC LIMIT ?`
	args := append([]any{entityID}, cargs...)
	args = append(args, entityID)
	args = append(args, iargs...)
	args = append(args, limit)
	rows, err := s.rdb.Query(q, args...)
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
	err := s.rdb.QueryRow(`SELECT id, type, name, origin, level FROM entities WHERE id = ?`, entityID).Scan(&e.ID, &e.Type, &e.Name, &e.Origin, &lvl)
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
			JOIN entities en ON en.id = mm.entity_id WHERE mm.item_id = ? AND c.item_id = ? AND ` + cvis + `
			UNION
			SELECT en.id, en.type, en.name, en.level FROM item_entities ie JOIN labels il ON il.item_id = ie.item_id AND il.chunk_seq = -1
			JOIN entities en ON en.id = ie.entity_id WHERE ie.item_id = ? AND ` + ivis
		// mm.item_id lets SQLite use mentions_item instead of scanning every
		// mention for each item (a mention's item is its passage's).
		args := append([]any{it.ID, it.ID}, cargs...)
		args = append(args, it.ID)
		args = append(args, iargs...)
		rows, err := s.rdb.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, typ, name string
			var lvl int
			rows.Scan(&id, &typ, &name, &lvl)
			if l := sensitivity.Level(lvl); l > sc.MaxLevel || (len(sc.Audience) > 0 && l > sc.Open) {
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

// AuditCounts counts the audited reads made under a scope name, by op.
func (s *Store) AuditCounts(scope string) (map[string]int, error) {
	rows, err := s.rdb.Query(`SELECT op, count(*) FROM audit WHERE scope = ? GROUP BY op`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var op string
		var n int
		if err := rows.Scan(&op, &n); err != nil {
			return nil, err
		}
		out[op] = n
	}
	return out, rows.Err()
}

// ChunkLabel returns a chunk's effective label and its item's audience. It
// is the eval harness's independent check on what Search returned, so it
// reads labels directly rather than through visSQL.
func (s *Store) ChunkLabel(chunkID int64) (sensitivity.Label, []string, error) {
	var lvl int
	var cats int64
	var itemID string
	err := s.rdb.QueryRow(`SELECT c.item_id, max(il.level, coalesce(cl.level, 0)), (il.categories | coalesce(cl.categories, 0))
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
	err := s.rdb.QueryRow(`SELECT level, categories FROM labels WHERE item_id = ? AND chunk_seq = -1`, itemID).Scan(&lvl, &cats)
	if err != nil {
		return sensitivity.Label{}, nil, fmt.Errorf("item %s has no label: %w", itemID, err)
	}
	aud, err := s.audience(itemID)
	return sensitivity.Label{Level: sensitivity.Level(lvl), Categories: sensitivity.Categories(cats)}, aud, err
}

func (s *Store) audience(itemID string) ([]string, error) {
	rows, err := s.rdb.Query(`SELECT principal FROM item_audience WHERE item_id = ?`, itemID)
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

// ItemView is a source item as a person may see it: only the chunks visible
// in scope.
type ItemView struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Title      string            `json:"title"`
	OccurredAt time.Time         `json:"occurredAt"`
	Label      sensitivity.Label `json:"label"`
	Chunks     []Hit             `json:"chunks"`
}

// Item returns the visible chunks of an item, in order.
func (s *Store) Item(sc sensitivity.Scope, itemID string) (*ItemView, error) {
	vis, vargs := visSQL(sc, "il", "cl", "c.item_id")
	rows, err := s.rdb.Query(`SELECT c.id FROM chunks c`+chunkVisJoin+`WHERE c.item_id = ? AND `+vis+` ORDER BY c.seq`, append([]any{itemID}, vargs...)...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return nil, nil
	}
	v := &ItemView{ID: itemID}
	for _, id := range ids {
		h, err := s.hit(sc, id)
		if err != nil {
			return nil, err
		}
		if h == nil {
			continue
		}
		v.Kind, v.Title, v.OccurredAt = h.Kind, h.Title, h.OccurredAt
		if h.Label.Level > v.Label.Level {
			v.Label.Level = h.Label.Level
		}
		v.Chunks = append(v.Chunks, *h)
	}
	return v, nil
}

// ItemTitles returns the titles and times of the visible items among ids.
func (s *Store) ItemTitles(sc sensitivity.Scope, ids []string) (map[string]ItemRef, error) {
	out := map[string]ItemRef{}
	for i := 0; i < len(ids); i += 400 {
		part := ids[i:min(i+400, len(ids))]
		vis, vargs := visSQL(sc, "il", "", "i.id")
		q := `SELECT i.id, i.kind, ` + displayTitle("i") + `, i.occurred_at FROM items i
			JOIN labels il ON il.item_id = i.id AND il.chunk_seq = -1
			LEFT JOIN item_links lk ON lk.item_id = i.id AND lk.rel = 'occurred_as' LEFT JOIN items e ON e.id = lk.other_id
			WHERE i.id IN (?` + strings.Repeat(",?", len(part)-1) + `) AND ` + vis
		var args []any
		for _, id := range part {
			args = append(args, id)
		}
		args = append(args, vargs...)
		rows, err := s.rdb.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r ItemRef
			var occ int64
			if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &occ); err != nil {
				rows.Close()
				return nil, err
			}
			r.OccurredAt = time.Unix(occ, 0)
			out[r.ID] = r
		}
		rows.Close()
	}
	return out, nil
}

// TopicRefRow is a thread an entity comes up in, with how many claims.
type TopicRefRow struct {
	ID     int64
	Label  string
	Claims int
}

// EntityTopicRefs lists the threads an entity comes up in most, owner
// scope (the person views are the owner's): the threads holding the most
// of what they said (current items), and the topic map's entity links
// where those exist.
// names (lower case) also match statements whose holder was written but
// not resolved to the entity.
func (s *Store) EntityTopicRefs(entityID string, names []string, limit int) ([]TopicRefRow, error) {
	in := "''"
	args := []any{entityID}
	if len(names) > 0 {
		in = strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
		for _, n := range names {
			args = append(args, n)
		}
	}
	args = append(args, entityID, limit)
	rows, err := s.rdb.Query(`SELECT id, label, sum(n) FROM (
			SELECT t.id, t.label, count(*) AS n FROM thread_items ti JOIN statements st ON st.id = ti.statement_id JOIN topics t ON t.id = ti.topic_id
			WHERE ti.current = 1 AND (st.holder_id = ? OR lower(st.holder) IN (`+in+`)) AND t.label != '' GROUP BY t.id
			UNION ALL
			SELECT t.id, t.label, te.claims FROM topic_entities te JOIN topics t ON t.id = te.topic_id WHERE te.entity_id = ? AND t.label != '')
		GROUP BY id ORDER BY 3 DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicRefRow
	for rows.Next() {
		var r TopicRefRow
		if err := rows.Scan(&r.ID, &r.Label, &r.Claims); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
