package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Pipeline methods: unscoped by design, for stages only. Nothing here may be
// wired to output a person or an automatic reply sees.

func (s *Store) ManifestHash(source, ref string) (string, bool, error) {
	var h string
	err := s.db.QueryRow(`SELECT hash FROM raw_manifest WHERE source = ? AND ref = ?`, source, ref).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return h, err == nil, err
}

func (s *Store) IsForgotten(source, ref string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM forgotten WHERE source = ? AND ref = ?`, source, ref).Scan(&n)
	return n > 0, err
}

func (s *Store) PutManifest(source, ref, hash, contentType string) error {
	_, err := s.db.Exec(`INSERT INTO raw_manifest(source, ref, hash, content_type, fetched_at) VALUES (?,?,?,?,?)
		ON CONFLICT(source, ref) DO UPDATE SET hash = excluded.hash, content_type = excluded.content_type, fetched_at = excluded.fetched_at`,
		source, ref, hash, contentType, time.Now().Unix())
	return err
}

// UpsertItem writes an item, returning whether its content changed.
func (s *Store) UpsertItem(it item.Item) (bool, error) {
	ch := it.ContentHash()
	var old string
	err := s.db.QueryRow(`SELECT content_hash FROM items WHERE id = ?`, it.ID).Scan(&old)
	if err == nil && old == ch {
		return false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	segs, _ := json.Marshal(it.Segments)
	parts, _ := json.Marshal(it.Participants)
	native, _ := json.Marshal(it.Native)
	_, err = s.db.Exec(`INSERT INTO items(id, source, ref, raw_hash, kind, title, occurred_at, ended_at, thread, segments, participants, native, content_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET raw_hash=excluded.raw_hash, kind=excluded.kind, title=excluded.title, occurred_at=excluded.occurred_at,
		ended_at=excluded.ended_at, thread=excluded.thread, segments=excluded.segments, participants=excluded.participants,
		native=excluded.native, content_hash=excluded.content_hash`,
		it.ID, it.Source, it.Ref, it.RawHash, it.Kind, it.Title, it.OccurredAt.Unix(), it.EndedAt.Unix(), it.Thread,
		string(segs), string(parts), string(native), ch)
	return true, err
}

// AllItems loads every item, oldest first.
func (s *Store) AllItems() ([]item.Item, error) {
	rows, err := s.db.Query(`SELECT id, source, ref, raw_hash, kind, title, occurred_at, ended_at, thread, segments, participants, native FROM items ORDER BY occurred_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item.Item
	for rows.Next() {
		var it item.Item
		var occ, end int64
		var segs, parts, native string
		if err := rows.Scan(&it.ID, &it.Source, &it.Ref, &it.RawHash, &it.Kind, &it.Title, &occ, &end, &it.Thread, &segs, &parts, &native); err != nil {
			return nil, err
		}
		it.OccurredAt, it.EndedAt = time.Unix(occ, 0), time.Unix(end, 0)
		json.Unmarshal([]byte(segs), &it.Segments)
		json.Unmarshal([]byte(parts), &it.Participants)
		json.Unmarshal([]byte(native), &it.Native)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) StageStamp(stage, itemID string) (string, error) {
	var h string
	err := s.db.QueryRow(`SELECT version || '/' || input_hash FROM stage_runs WHERE stage = ? AND item_id = ?`, stage, itemID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func stamp(tx *sql.Tx, stage, itemID, version, hash string) error {
	_, err := tx.Exec(`INSERT INTO stage_runs(stage, item_id, version, input_hash, ran_at) VALUES (?,?,?,?,?)
		ON CONFLICT(stage, item_id) DO UPDATE SET version=excluded.version, input_hash=excluded.input_hash, ran_at=excluded.ran_at`,
		stage, itemID, version, hash, time.Now().Unix())
	return err
}

func (s *Store) GlobalStamp(step string) (string, error) {
	var h string
	err := s.db.QueryRow(`SELECT input_hash FROM global_runs WHERE step = ?`, step).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func (s *Store) SetGlobalStamp(step, hash string) error {
	_, err := s.db.Exec(`INSERT INTO global_runs(step, input_hash, ran_at) VALUES (?,?,?)
		ON CONFLICT(step) DO UPDATE SET input_hash=excluded.input_hash, ran_at=excluded.ran_at`, step, hash, time.Now().Unix())
	return err
}

type Chunk struct {
	ID                    int64
	ItemID                string
	Seq                   int
	SegFrom, SegTo        int
	Start, End            float64
	Speakers, Title, Text string
}

// ReplaceChunks swaps an item's chunks. Vectors and mentions of the old
// chunks go with them (cascade); their stages re-run because their input
// hashes include the chunk stage's.
func (s *Store) ReplaceChunks(itemID, version, hash string, chunks []Chunk) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM chunks WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		for _, c := range chunks {
			if _, err := tx.Exec(`INSERT INTO chunks(item_id, seq, seg_from, seg_to, start_s, end_s, speakers, title, text, text_hash) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				itemID, c.Seq, c.SegFrom, c.SegTo, c.Start, c.End, c.Speakers, c.Title, c.Text, textHash(c.Text)); err != nil {
				return err
			}
		}
		return stamp(tx, "chunk", itemID, version, hash)
	})
}

func (s *Store) Chunks(itemID string) ([]Chunk, error) {
	rows, err := s.db.Query(`SELECT id, item_id, seq, seg_from, seg_to, start_s, end_s, speakers, title, text FROM chunks WHERE item_id = ? ORDER BY seq`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.ItemID, &c.Seq, &c.SegFrom, &c.SegTo, &c.Start, &c.End, &c.Speakers, &c.Title, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplaceLabels writes an item's label, its chunk escalations and its audience.
func (s *Store) ReplaceLabels(itemID, version, hash string, itemLabel sensitivity.Label, chunkLabels map[int]sensitivity.Label, audience []string) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM labels WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM item_audience WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		put := func(seq int, l sensitivity.Label) error {
			_, err := tx.Exec(`INSERT INTO labels(item_id, chunk_seq, level, categories, origin, confidence, reason) VALUES (?,?,?,?,?,?,?)`,
				itemID, seq, int(l.Level), int64(l.Categories), string(l.Origin), l.Confidence, l.Reason)
			return err
		}
		if err := put(-1, itemLabel); err != nil {
			return err
		}
		for seq, l := range chunkLabels {
			if err := put(seq, l); err != nil {
				return err
			}
		}
		for _, p := range audience {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO item_audience(item_id, principal) VALUES (?,?)`, itemID, p); err != nil {
				return err
			}
		}
		return stamp(tx, "classify", itemID, version, hash)
	})
}

// PutVecs replaces one model's vectors for an item's chunks.
func (s *Store) PutVecs(itemID, stage, model, version, hash string, vecs map[int64][]float32) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM chunk_vecs WHERE model = ? AND chunk_id IN (SELECT id FROM chunks WHERE item_id = ?)`, model, itemID); err != nil {
			return err
		}
		for id, v := range vecs {
			b, err := sqlite_vec.SerializeFloat32(v)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO chunk_vecs(chunk_id, model, vec) VALUES (?,?,?)`, id, model, b); err != nil {
				return err
			}
		}
		return stamp(tx, stage, itemID, version, hash)
	})
}

type Entity struct {
	ID, Type, Name, Origin string
	Level                  sensitivity.Level
}

type Alias struct {
	Alias, EntityID, Kind string
}

// ReplaceEntities makes the entity tables match the given set. Entities that
// disappear take their mentions and links with them; the resolve stage
// re-runs everywhere because its input hash includes the registry's.
func (s *Store) ReplaceEntities(ents []Entity, aliases []Alias) error {
	return s.tx(func(tx *sql.Tx) error {
		keep := map[string]bool{}
		for _, e := range ents {
			keep[e.ID] = true
			if _, err := tx.Exec(`INSERT INTO entities(id, type, name, origin, level) VALUES (?,?,?,?,?)
				ON CONFLICT(id) DO UPDATE SET type=excluded.type, name=excluded.name, origin=excluded.origin, level=excluded.level`, e.ID, e.Type, e.Name, e.Origin, int(e.Level)); err != nil {
				return err
			}
		}
		rows, err := tx.Query(`SELECT id FROM entities`)
		if err != nil {
			return err
		}
		var drop []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			if !keep[id] {
				drop = append(drop, id)
			}
		}
		rows.Close()
		for _, id := range drop {
			if _, err := tx.Exec(`DELETE FROM entities WHERE id = ?`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM entity_aliases`); err != nil {
			return err
		}
		for _, a := range aliases {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO entity_aliases(alias, entity_id, kind) VALUES (?,?,?)`, a.Alias, a.EntityID, a.Kind); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) AllAliases() ([]Alias, error) {
	rows, err := s.db.Query(`SELECT alias, entity_id, kind FROM entity_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.Alias, &a.EntityID, &a.Kind); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type Link struct {
	ItemID, Rel, OtherID string
	Score                float64
}

func (s *Store) ReplaceLinks(rel string, links []Link) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM item_links WHERE rel = ?`, rel); err != nil {
			return err
		}
		for _, l := range links {
			if _, err := tx.Exec(`INSERT INTO item_links(item_id, rel, other_id, score) VALUES (?,?,?,?)`, l.ItemID, rel, l.OtherID, l.Score); err != nil {
				return err
			}
		}
		return nil
	})
}

// LinksFrom returns item → linked item ids for rel.
func (s *Store) LinksFrom(rel string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT item_id, other_id FROM item_links WHERE rel = ?`, rel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var a, b string
		rows.Scan(&a, &b)
		out[a] = b
	}
	return out, rows.Err()
}

type Mention struct {
	ChunkSeq        int
	EntityID        string // "" when ambiguous
	Surface         string
	Seg, Start, End int
	Confidence      float64
	Candidates      []string
}

type ItemEntity struct {
	EntityID, Role string
}

func (s *Store) ReplaceMentions(itemID, version, hash string, ms []Mention, parts []ItemEntity) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM mentions WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM item_entities WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		for _, m := range ms {
			var ent any
			if m.EntityID != "" {
				ent = m.EntityID
			}
			if _, err := tx.Exec(`INSERT INTO mentions(chunk_id, item_id, entity_id, surface, seg, start, end, confidence, candidates)
				SELECT id, ?, ?, ?, ?, ?, ?, ?, ? FROM chunks WHERE item_id = ? AND seq = ?`,
				itemID, ent, m.Surface, m.Seg, m.Start, m.End, m.Confidence, strings.Join(m.Candidates, ","), itemID, m.ChunkSeq); err != nil {
				return err
			}
		}
		for _, p := range parts {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO item_entities(item_id, entity_id, role) VALUES (?,?,?)`, itemID, p.EntityID, p.Role); err != nil {
				return err
			}
		}
		return stamp(tx, "resolve", itemID, version, hash)
	})
}

// Forget deletes an item and everything derived from it, and its raw
// manifest entry. It returns the raw hash, which the caller deletes from the
// blob store once no manifest row still refers to it.
func (s *Store) Forget(itemID string) (rawHash string, stillUsed bool, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		var src, ref string
		if err := tx.QueryRow(`SELECT source, ref, raw_hash FROM items WHERE id = ?`, itemID).Scan(&src, &ref, &rawHash); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, itemID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM raw_manifest WHERE source = ? AND ref = ?`, src, ref); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO forgotten(source, ref, at) VALUES (?,?,?)`, src, ref, time.Now().Unix()); err != nil {
			return err
		}
		var n int
		tx.QueryRow(`SELECT count(*) FROM raw_manifest WHERE hash = ?`, rawHash).Scan(&n)
		stillUsed = n > 0
		return nil
	})
	return rawHash, stillUsed, err
}

func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Mentions returns an item's mentions with their chunk seq, for the eval
// harness.
func (s *Store) Mentions(itemID string) ([]Mention, error) {
	rows, err := s.db.Query(`SELECT c.seq, coalesce(m.entity_id, ''), m.surface, m.seg, m.start, m.end, m.confidence, m.candidates
		FROM mentions m JOIN chunks c ON c.id = m.chunk_id WHERE m.item_id = ? ORDER BY m.seg, m.start`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mention
	for rows.Next() {
		var m Mention
		var cands string
		if err := rows.Scan(&m.ChunkSeq, &m.EntityID, &m.Surface, &m.Seg, &m.Start, &m.End, &m.Confidence, &cands); err != nil {
			return nil, err
		}
		if cands != "" {
			m.Candidates = strings.Split(cands, ",")
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Stats are counts for the owner's status view; no content.
type Stats struct {
	ItemsByKind    map[string]int
	ItemLevels     map[string]int
	ChunkLevels    map[string]int
	Categories     map[string]int
	Chunks         int
	Vectors        map[string]int
	Entities       map[string]int
	Mentions       int
	Ambiguous      int
	MeetingsLinked int
	Meetings       int
}

func (s *Store) Stats() (*Stats, error) {
	st := &Stats{ItemsByKind: map[string]int{}, ItemLevels: map[string]int{}, ChunkLevels: map[string]int{}, Categories: map[string]int{}, Vectors: map[string]int{}, Entities: map[string]int{}}
	count := func(q string, into map[string]int, name func(string) string) error {
		rows, err := s.db.Query(q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int
			rows.Scan(&k, &n)
			if name != nil {
				k = name(k)
			}
			into[k] += n
		}
		return rows.Err()
	}
	lvl := func(k string) string {
		var n int
		fmt.Sscan(k, &n)
		return sensitivity.Level(n).String()
	}
	for _, q := range []struct {
		sql  string
		into map[string]int
		name func(string) string
	}{
		{`SELECT kind, count(*) FROM items GROUP BY kind`, st.ItemsByKind, nil},
		{`SELECT level, count(*) FROM labels WHERE chunk_seq = -1 GROUP BY level`, st.ItemLevels, lvl},
		{`SELECT max(il.level, coalesce(cl.level, 0)), count(*) FROM chunks c` + chunkVisJoin + ` GROUP BY 1`, st.ChunkLevels, lvl},
		{`SELECT model, count(*) FROM chunk_vecs GROUP BY model`, st.Vectors, nil},
		{`SELECT type || '/' || origin, count(*) FROM entities GROUP BY 1`, st.Entities, nil},
	} {
		if err := count(q.sql, q.into, q.name); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.Query(`SELECT categories FROM labels WHERE categories != 0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c int64
		rows.Scan(&c)
		for _, n := range sensitivity.Categories(c).Names() {
			st.Categories[n]++
		}
	}
	rows.Close()
	s.db.QueryRow(`SELECT count(*) FROM chunks`).Scan(&st.Chunks)
	s.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE entity_id IS NULL) FROM mentions`).Scan(&st.Mentions, &st.Ambiguous)
	s.db.QueryRow(`SELECT count(*) FROM items WHERE kind = 'meeting'`).Scan(&st.Meetings)
	s.db.QueryRow(`SELECT count(*) FROM item_links WHERE rel = 'occurred_as'`).Scan(&st.MeetingsLinked)
	return st, nil
}

// ItemLabels returns an item's own label and its chunk escalations, for
// inspection by the owner.
func (s *Store) ItemLabels(itemID string) (map[int]sensitivity.Label, error) {
	rows, err := s.db.Query(`SELECT chunk_seq, level, categories, origin, confidence, reason FROM labels WHERE item_id = ?`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]sensitivity.Label{}
	for rows.Next() {
		var seq, lvl int
		var cats int64
		var l sensitivity.Label
		var origin string
		rows.Scan(&seq, &lvl, &cats, &origin, &l.Confidence, &l.Reason)
		l.Level, l.Categories, l.Origin = sensitivity.Level(lvl), sensitivity.Categories(cats), sensitivity.Origin(origin)
		out[seq] = l
	}
	return out, rows.Err()
}

// ResolveItemID expands an id prefix to the one item it names.
func (s *Store) ResolveItemID(prefix string) (string, error) {
	rows, err := s.db.Query(`SELECT id FROM items WHERE id = ? OR id LIKE ? || '%' LIMIT 2`, prefix, prefix)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no item %q", prefix)
	case 1:
		return ids[0], nil
	}
	for _, id := range ids {
		if id == prefix {
			return id, nil
		}
	}
	return "", fmt.Errorf("item prefix %q is ambiguous", prefix)
}

// ChunkTexts returns every chunk's text, for corpus statistics (term
// rarity). Not for display.
func (s *Store) ChunkTexts() ([]string, error) {
	rows, err := s.db.Query(`SELECT text FROM chunks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		out = append(out, t)
	}
	return out, rows.Err()
}

type MeetingRef struct {
	ID         string    `json:"id"`
	Ref        string    `json:"ref"`
	Title      string    `json:"title"`
	OccurredAt time.Time `json:"occurredAt"`
	Chunks     int       `json:"chunks"`
}

// Meetings lists Tomoe recordings with their linked titles, newest first,
// for the owner's replay picker.
func (s *Store) Meetings(limit int) ([]MeetingRef, error) {
	rows, err := s.db.Query(`SELECT i.id, i.ref, coalesce(e.title, i.title), i.occurred_at, (SELECT count(*) FROM chunks c WHERE c.item_id = i.id)
		FROM items i LEFT JOIN item_links l ON l.item_id = i.id AND l.rel = 'occurred_as' LEFT JOIN items e ON e.id = l.other_id
		WHERE i.kind = 'meeting' ORDER BY i.occurred_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingRef
	for rows.Next() {
		var m MeetingRef
		var occ int64
		rows.Scan(&m.ID, &m.Ref, &m.Title, &occ, &m.Chunks)
		m.OccurredAt = time.Unix(occ, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// EntityNames maps entity ids to name and type, for the live spotter. The
// spotter's output is shown only through scoped reads (EntityGraph).
func (s *Store) EntityNames() (map[string][2]string, error) {
	rows, err := s.db.Query(`SELECT id, name, type FROM entities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var id, name, typ string
		rows.Scan(&id, &name, &typ)
		out[id] = [2]string{name, typ}
	}
	return out, rows.Err()
}

// EventAt returns the subject of the calendar event under way at t, for the
// live panel's title (Tomoe names recordings by time). Owner-only display.
func (s *Store) EventAt(t time.Time) (string, error) {
	var title string
	err := s.db.QueryRow(`SELECT title FROM items WHERE kind = 'event' AND occurred_at <= ? AND ended_at > ?
		AND ended_at - occurred_at <= 4 * 3600 AND native NOT LIKE '%"allDay":"true"%' AND native NOT LIKE '%"cancelled":"true"%'
		AND native NOT LIKE '%"showAs":"free"%' ORDER BY occurred_at DESC LIMIT 1`, t.Unix()+15*60, t.Unix()).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return title, err
}

func textHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:12])
}
