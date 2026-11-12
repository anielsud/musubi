package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/vindex"
)

// A speaker relabel (docs/recorder-screens.md, "Renaming a voice profile
// renames its speakers") rewrites an item's stored copies of a speaker
// label without running any stage again: the pipeline works out every new
// value (pipeline.Runner.RelabelSpeaker) and RelabelSpeaker writes them in
// one transaction, recording what each field held before so UndoRelabel
// can put it back. Pipeline methods: unscoped, never shown to a person.

// RelabelledStage is the stage_runs row holding the chunk stamps an
// item's passages had before relabels ("version/hash", space-separated,
// newest last). Output a knowledge stage stamped from one of them came
// from the same passages, relabelled since, so it is restamped rather
// than made again. ReplaceChunks drops it: new passages are new input.
const RelabelledStage = "chunk-relabelled"

const maxRelabelled = 8

// SpeakerRelabel is the new value of every field a relabel changes in one
// item. Empty slices change nothing; a field is written as given.
type SpeakerRelabel struct {
	ItemID string `json:"item"`
	// Names is the relabel itself, old → new, for the record.
	Names map[string]string `json:"names"`
	// Item columns.
	Segments     string `json:"segments"`
	Participants string `json:"participants"`
	Native       string `json:"native"`
	ContentHash  string `json:"contentHash"`
	// ChunkStamp is the item's chunk stamp ("version/hash") afterwards.
	ChunkStamp string             `json:"chunkStamp"`
	Chunks     []ChunkRelabel     `json:"chunks,omitempty"`
	Statements []StatementRelabel `json:"statements,omitempty"`
	Sections   []SectionRelabel   `json:"sections,omitempty"`
	Todos      []TodoRelabel      `json:"todos,omitempty"`
	Overview   *string            `json:"overview,omitempty"`
	Topics     []TopicRelabel     `json:"topics,omitempty"`
	Statuses   []StatusRelabel    `json:"statuses,omitempty"`
}

type ChunkRelabel struct {
	ID       int64  `json:"id"`
	Text     string `json:"text"`
	Speakers string `json:"speakers"`
}

type StatementRelabel struct {
	ID         int64  `json:"id"`
	Holder     string `json:"holder"`
	HolderID   string `json:"holderId"`
	Who        string `json:"who"`
	Text       string `json:"text"`
	SearchText string `json:"searchText"`
}

type SectionRelabel struct {
	N       int    `json:"n"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	People  string `json:"people"`
}

type TodoRelabel struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	Owners    string `json:"owners"`
	Requester string `json:"requester"`
}

type TopicRelabel struct {
	ID      int64  `json:"id"`
	Label   string `json:"label"`
	Summary string `json:"summary"`
}

// StatusRelabel restamps a thread status whose input changed only by the
// relabel (Was is the input hash it must still have).
type StatusRelabel struct {
	Topic     int64  `json:"topic"`
	Variant   string `json:"variant"`
	Was       string `json:"was"`
	InputHash string `json:"inputHash"`
	Body      string `json:"body"`
}

// relabelRecord is one item's relabel as kept in speaker_relabels: the
// values before and after.
type relabelRecord struct {
	Before SpeakerRelabel `json:"before"`
	After  SpeakerRelabel `json:"after"`
}

// RelabelItem is an item as stored, for working out a relabel.
func (s *Store) RelabelItem(itemID string) (item.Item, string, error) {
	var it item.Item
	var occ, end int64
	var segs, parts, native, ch string
	err := s.db.QueryRow(`SELECT id, source, ref, raw_hash, kind, title, occurred_at, ended_at, thread, segments, participants, native, content_hash FROM items WHERE id = ?`, itemID).
		Scan(&it.ID, &it.Source, &it.Ref, &it.RawHash, &it.Kind, &it.Title, &occ, &end, &it.Thread, &segs, &parts, &native, &ch)
	if err != nil {
		return it, "", err
	}
	it.OccurredAt, it.EndedAt = time.Unix(occ, 0), time.Unix(end, 0)
	if err := json.Unmarshal([]byte(segs), &it.Segments); err != nil {
		return it, "", err
	}
	json.Unmarshal([]byte(parts), &it.Participants)
	json.Unmarshal([]byte(native), &it.Native)
	return it, ch, nil
}

// RelabelStatement is a statement's fields a relabel may change, with
// what places it in its passage.
type RelabelStatement struct {
	StatementRelabel
	ChunkID  int64
	Line     int
	Evidence string
	// Corrected: the owner corrected it, so its text is theirs.
	Corrected bool
}

// RelabelStatements lists an item's statements for a relabel.
func (s *Store) RelabelStatements(itemID string) ([]RelabelStatement, error) {
	rows, err := s.db.Query(`SELECT st.id, st.holder, coalesce(st.holder_id, ''), st.who, st.text, st.search_text, st.chunk_id, st.line, st.evidence,
		EXISTS (SELECT 1 FROM corrections c WHERE c.item_id = st.item_id AND c.statement = st.id AND c.undone = 0)
		FROM statements st WHERE st.item_id = ? ORDER BY st.id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelabelStatement
	for rows.Next() {
		var r RelabelStatement
		if err := rows.Scan(&r.ID, &r.Holder, &r.HolderID, &r.Who, &r.Text, &r.SearchText, &r.ChunkID, &r.Line, &r.Evidence, &r.Corrected); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RelabelDigest is an item's digest fields a relabel may change.
func (s *Store) RelabelDigest(itemID string) (overview *string, sections []SectionRelabel, todos []TodoRelabel, err error) {
	var ov string
	switch err = s.db.QueryRow(`SELECT overview FROM meeting_digests WHERE item_id = ?`, itemID).Scan(&ov); {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return
	default:
		overview = &ov
	}
	rows, err := s.db.Query(`SELECT n, subject, text, people FROM meeting_sections WHERE item_id = ? ORDER BY n`, itemID)
	if err != nil {
		return
	}
	for rows.Next() {
		var x SectionRelabel
		if err = rows.Scan(&x.N, &x.Subject, &x.Text, &x.People); err != nil {
			rows.Close()
			return
		}
		sections = append(sections, x)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT id, text, owners, requester FROM meeting_todos WHERE item_id = ? ORDER BY id`, itemID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var x TodoRelabel
		if err = rows.Scan(&x.ID, &x.Text, &x.Owners, &x.Requester); err != nil {
			return
		}
		todos = append(todos, x)
	}
	err = rows.Err()
	return
}

// RelabelTopics are the threads holding any of an item's statements, with
// their name and summary.
func (s *Store) RelabelTopics(itemID string) ([]TopicRelabel, error) {
	rows, err := s.db.Query(`SELECT DISTINCT t.id, t.label, t.summary FROM topics t JOIN thread_items ti ON ti.topic_id = t.id
		JOIN statements st ON st.id = ti.statement_id WHERE st.item_id = ? ORDER BY t.id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicRelabel
	for rows.Next() {
		var x TopicRelabel
		if err := rows.Scan(&x.ID, &x.Label, &x.Summary); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ThreadStatusRows are a thread's stored statuses by variant.
func (s *Store) ThreadStatusRows(topic int64) (map[string]ThreadStatusRow, error) {
	rows, err := s.db.Query(`SELECT variant, version, input_hash, body FROM thread_status WHERE topic_id = ?`, topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ThreadStatusRow{}
	for rows.Next() {
		r := ThreadStatusRow{Topic: topic}
		if err := rows.Scan(&r.Variant, &r.Version, &r.InputHash, &r.Body); err != nil {
			return nil, err
		}
		out[r.Variant] = r
	}
	return out, rows.Err()
}

// RelabelledChunkStamps are the chunk stamps an item had before relabels
// (RelabelledStage), oldest first.
func (s *Store) RelabelledChunkStamps(itemID string) []string {
	var h string
	if err := s.rdb.QueryRow(`SELECT input_hash FROM stage_runs WHERE stage = ? AND item_id = ?`, RelabelledStage, itemID).Scan(&h); err != nil {
		return nil
	}
	return strings.Fields(h)
}

// ErrRelabelStale: the item changed since the relabel was worked out (or,
// for an undo, since the relabel), so nothing was written.
var ErrRelabelStale = errors.New("the meeting changed in Musubi since; update and try again")

// RelabelSpeaker writes a relabel worked out by the pipeline, in one
// transaction, if the item still has the content hash and chunk stamp it
// was worked out from (wasHash, wasChunk), and records the values it
// replaces under batch. Text vectors of the passages and statements whose
// text changes are left for the caller to make again: a changed
// statement's vector is dropped (a claim of that statement alone keeps the
// old one as its centroid, as PutStatementVec does), so the knowledge
// layer embeds it again if the caller can't.
func (s *Store) RelabelSpeaker(batch, wasHash, wasChunk string, p SpeakerRelabel) error {
	return s.tx(func(tx *sql.Tx) error {
		before, err := applyRelabel(tx, wasHash, wasChunk, p)
		if err != nil {
			return err
		}
		body, err := json.Marshal(relabelRecord{Before: before, After: p})
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO speaker_relabels(batch, item_id, at, body) VALUES (?,?,?,?)`, batch, p.ItemID, time.Now().Unix(), string(body))
		return err
	})
}

// UndoRelabel puts back what batch's relabels of itemID replaced, if the
// item is still as the relabel left it. It returns the record's before
// and after (the pipeline makes the vectors again from them).
func (s *Store) UndoRelabel(batch, itemID string) (before, after SpeakerRelabel, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		var id int64
		var body string
		err := tx.QueryRow(`SELECT id, body FROM speaker_relabels WHERE batch = ? AND item_id = ? AND undone = 0 ORDER BY id DESC LIMIT 1`, batch, itemID).Scan(&id, &body)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no relabel %s of %s to undo", batch, itemID)
		}
		if err != nil {
			return err
		}
		var rec relabelRecord
		if err := json.Unmarshal([]byte(body), &rec); err != nil {
			return err
		}
		if _, err := applyRelabel(tx, rec.After.ContentHash, rec.After.ChunkStamp, rec.Before); err != nil {
			return err
		}
		before, after = rec.Before, rec.After
		_, err = tx.Exec(`UPDATE speaker_relabels SET undone = 1 WHERE id = ?`, id)
		return err
	})
	return
}

// RelabelBatchItems are the items a batch relabelled and hasn't undone.
func (s *Store) RelabelBatchItems(batch string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT item_id FROM speaker_relabels WHERE batch = ? AND undone = 0 ORDER BY item_id`, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

// applyRelabel writes p over the item if it still has wasHash and
// wasChunk, and returns the values it replaced, in p's shape.
func applyRelabel(tx *sql.Tx, wasHash, wasChunk string, p SpeakerRelabel) (SpeakerRelabel, error) {
	b := SpeakerRelabel{ItemID: p.ItemID, Names: map[string]string{}}
	for o, n := range p.Names {
		b.Names[n] = o
	}
	if err := tx.QueryRow(`SELECT segments, participants, native, content_hash FROM items WHERE id = ?`, p.ItemID).
		Scan(&b.Segments, &b.Participants, &b.Native, &b.ContentHash); err != nil {
		return b, err
	}
	var chunk string
	err := tx.QueryRow(`SELECT version || '/' || input_hash FROM stage_runs WHERE stage = 'chunk' AND item_id = ?`, p.ItemID).Scan(&chunk)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	if b.ContentHash != wasHash || chunk != wasChunk {
		return b, ErrRelabelStale
	}
	b.ChunkStamp = chunk
	if _, err := tx.Exec(`UPDATE items SET segments = ?, participants = ?, native = ?, content_hash = ? WHERE id = ?`,
		p.Segments, p.Participants, p.Native, p.ContentHash, p.ItemID); err != nil {
		return b, err
	}
	ver, hash, ok := strings.Cut(p.ChunkStamp, "/")
	if !ok {
		return b, fmt.Errorf("bad chunk stamp %q", p.ChunkStamp)
	}
	if err := stamp(tx, "chunk", p.ItemID, ver, hash); err != nil {
		return b, err
	}
	// The stamp replaced joins the ones knowledge output may still carry.
	var was string
	if err := tx.QueryRow(`SELECT input_hash FROM stage_runs WHERE stage = ? AND item_id = ?`, RelabelledStage, p.ItemID).Scan(&was); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	list := append(strings.Fields(was), chunk)
	list = list[max(0, len(list)-maxRelabelled):]
	if err := stamp(tx, RelabelledStage, p.ItemID, "was", strings.Join(list, " ")); err != nil {
		return b, err
	}

	for _, c := range p.Chunks {
		var old ChunkRelabel
		var oldHash string
		if err := tx.QueryRow(`SELECT id, text, speakers, text_hash FROM chunks WHERE id = ? AND item_id = ?`, c.ID, p.ItemID).Scan(&old.ID, &old.Text, &old.Speakers, &oldHash); err != nil {
			return b, fmt.Errorf("passage %d: %w", c.ID, err)
		}
		b.Chunks = append(b.Chunks, old)
		nh := textHash(c.Text)
		if _, err := tx.Exec(`UPDATE chunks SET text = ?, speakers = ?, text_hash = ? WHERE id = ?`, c.Text, c.Speakers, nh, c.ID); err != nil {
			return b, err
		}
		// Judgements are anchored to their passage's text hash.
		if _, err := tx.Exec(`UPDATE judgments SET chunk_hash = ? WHERE item_id = ? AND chunk_hash = ?`, nh, p.ItemID, oldHash); err != nil {
			return b, err
		}
	}
	for _, st := range p.Statements {
		var old StatementRelabel
		if err := tx.QueryRow(`SELECT id, holder, coalesce(holder_id, ''), who, text, search_text FROM statements WHERE id = ? AND item_id = ?`, st.ID, p.ItemID).
			Scan(&old.ID, &old.Holder, &old.HolderID, &old.Who, &old.Text, &old.SearchText); err != nil {
			return b, fmt.Errorf("statement %d: %w", st.ID, err)
		}
		b.Statements = append(b.Statements, old)
		if _, err := tx.Exec(`UPDATE statements SET holder = ?, holder_id = nullif(?, ''), who = ?, text = ?, search_text = ? WHERE id = ?`,
			st.Holder, st.HolderID, st.Who, st.Text, st.SearchText, st.ID); err != nil {
			return b, err
		}
		if st.Text == old.Text {
			continue
		}
		// The owner's verdicts and to-do links name statements by text.
		for _, q := range []string{
			`UPDATE judgments SET text = ?1 WHERE item_id = ?3 AND text = ?2`,
			`UPDATE owner_links SET text = ?1 WHERE item_id = ?3 AND text = ?2`,
			`UPDATE owner_links SET other_text = ?1 WHERE other_item = ?3 AND other_text = ?2`,
			`UPDATE claims SET centroid = (SELECT vec FROM statement_vecs WHERE statement_id = ?4)
				WHERE centroid IS NULL AND id IN (SELECT claim_id FROM statement_claims WHERE statement_id = ?4)`,
			`DELETE FROM statement_vecs WHERE statement_id = ?4`,
		} {
			if _, err := tx.Exec(q, st.Text, old.Text, p.ItemID, st.ID); err != nil {
				return b, err
			}
		}
	}
	for _, x := range p.Sections {
		var old SectionRelabel
		if err := tx.QueryRow(`SELECT n, subject, text, people FROM meeting_sections WHERE item_id = ? AND n = ?`, p.ItemID, x.N).Scan(&old.N, &old.Subject, &old.Text, &old.People); err != nil {
			return b, fmt.Errorf("section %d: %w", x.N, err)
		}
		b.Sections = append(b.Sections, old)
		if _, err := tx.Exec(`UPDATE meeting_sections SET subject = ?, text = ?, people = ? WHERE item_id = ? AND n = ?`, x.Subject, x.Text, x.People, p.ItemID, x.N); err != nil {
			return b, err
		}
	}
	for _, x := range p.Todos {
		var old TodoRelabel
		if err := tx.QueryRow(`SELECT id, text, owners, requester FROM meeting_todos WHERE id = ? AND item_id = ?`, x.ID, p.ItemID).Scan(&old.ID, &old.Text, &old.Owners, &old.Requester); err != nil {
			return b, fmt.Errorf("to-do %d: %w", x.ID, err)
		}
		b.Todos = append(b.Todos, old)
		if _, err := tx.Exec(`UPDATE meeting_todos SET text = ?, owners = ?, requester = ? WHERE id = ?`, x.Text, x.Owners, x.Requester, x.ID); err != nil {
			return b, err
		}
	}
	if p.Overview != nil {
		var old string
		if err := tx.QueryRow(`SELECT overview FROM meeting_digests WHERE item_id = ?`, p.ItemID).Scan(&old); err != nil {
			return b, fmt.Errorf("digest: %w", err)
		}
		b.Overview = &old
		if _, err := tx.Exec(`UPDATE meeting_digests SET overview = ? WHERE item_id = ?`, *p.Overview, p.ItemID); err != nil {
			return b, err
		}
	}
	for _, x := range p.Topics {
		var old TopicRelabel
		if err := tx.QueryRow(`SELECT id, label, summary FROM topics WHERE id = ?`, x.ID).Scan(&old.ID, &old.Label, &old.Summary); err != nil {
			return b, fmt.Errorf("thread %d: %w", x.ID, err)
		}
		b.Topics = append(b.Topics, old)
		if _, err := tx.Exec(`UPDATE topics SET label = ?, summary = ? WHERE id = ?`, x.Label, x.Summary, x.ID); err != nil {
			return b, err
		}
	}
	for _, x := range p.Statuses {
		var old StatusRelabel
		err := tx.QueryRow(`SELECT topic_id, variant, input_hash, body FROM thread_status WHERE topic_id = ? AND variant = ?`, x.Topic, x.Variant).
			Scan(&old.Topic, &old.Variant, &old.InputHash, &old.Body)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return b, err
		}
		// Written again since it was worked out: leave it to the status
		// writer.
		if old.InputHash != x.Was {
			continue
		}
		old.Was = x.InputHash
		b.Statuses = append(b.Statuses, old)
		if _, err := tx.Exec(`UPDATE thread_status SET input_hash = ?, body = ? WHERE topic_id = ? AND variant = ?`, x.InputHash, x.Body, x.Topic, x.Variant); err != nil {
			return b, err
		}
	}
	return b, nil
}

// UpdateChunkVecs replaces the vectors of some of an item's passages
// (those whose text a relabel changed) and stamps the embed stage.
func (s *Store) UpdateChunkVecs(itemID, stage, model, version, hash string, vecs map[int64][]float32) error {
	return s.tx(func(tx *sql.Tx) error {
		if s.VectorEncoding == VecF16 && len(vecs) > 0 {
			if err := metaSet(tx, metaChunkF16, "1"); err != nil {
				return err
			}
		}
		for id, v := range vecs {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM chunks WHERE id = ? AND item_id = ?`, id, itemID).Scan(&n); err != nil || n == 0 {
				return fmt.Errorf("passage %d is not %s's", id, itemID)
			}
			if _, err := tx.Exec(`INSERT INTO chunk_vecs(chunk_id, model, vec) VALUES (?,?,?) ON CONFLICT(chunk_id, model) DO UPDATE SET vec = excluded.vec`, id, model, s.encodeVec(v)); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO chunk_codes(chunk_id, model, code) VALUES (?,?,?)`, id, model, vindex.Code(v)); err != nil {
				return err
			}
		}
		return stamp(tx, stage, itemID, version, hash)
	})
}

// SetDigestLinkStamp restamps the to-do matching of an item whose
// statements a relabel kept (the links themselves still hold).
func (s *Store) SetDigestLinkStamp(itemID, hash string) error {
	return s.tx(func(tx *sql.Tx) error { return stamp(tx, "digest-link", itemID, "l", hash) })
}

// SetStageInput restamps an item's extract-input or digest-input (an
// earlier version's output kept, whose input a relabel only relabelled).
func (s *Store) SetStageInput(stage, itemID, input string) error {
	if stage != "extract-input" && stage != "digest-input" {
		return fmt.Errorf("not an input stamp: %s", stage)
	}
	return s.tx(func(tx *sql.Tx) error { return stamp(tx, stage, itemID, "in", input) })
}
