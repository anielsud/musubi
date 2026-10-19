package store

import (
	"database/sql"
	"regexp"
	"strings"
	"time"
)

// LearnedAlias is another way a thing is written or heard, learned from
// the corpus and decided by the owner (schema: learned_aliases; the
// registry's own aliases are Alias).
type LearnedAlias struct {
	ID            int64
	Keep, Variant string
	Kind, Scope   string
	Status        string
	Evidence      string
	Score         float64
	By            string
	At            time.Time
}

// ProposeAlias records a candidate (from the corpus); a decided one is
// left as decided.
func (s *Store) ProposeAlias(a LearnedAlias) error {
	_, err := s.db.Exec(`INSERT INTO learned_aliases(keep, variant, kind, scope, status, evidence, score, by, at) VALUES (?,?,?,?,'proposed',?,?,?,?)
		ON CONFLICT(keep, variant, kind) DO UPDATE SET evidence = excluded.evidence, score = excluded.score WHERE status = 'proposed'`,
		a.Keep, a.Variant, a.Kind, a.Scope, a.Evidence, a.Score, a.By, time.Now().Unix())
	return err
}

// DecideAlias confirms or rejects an alias (the owner's call).
func (s *Store) DecideAlias(id int64, confirm bool, by string) error {
	st := "rejected"
	if confirm {
		st = "confirmed"
	}
	_, err := s.db.Exec(`UPDATE learned_aliases SET status = ?, by = ?, at = ? WHERE id = ?`, st, by, time.Now().Unix(), id)
	return err
}

// LearnedAliases lists them with the given status ("" for all), best first.
func (s *Store) LearnedAliases(status string) ([]LearnedAlias, error) {
	rows, err := s.db.Query(`SELECT id, keep, variant, kind, scope, status, evidence, score, by, at FROM learned_aliases
		WHERE ? = '' OR status = ? ORDER BY score DESC, id`, status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LearnedAlias
	for rows.Next() {
		var a LearnedAlias
		var at int64
		if err := rows.Scan(&a.ID, &a.Keep, &a.Variant, &a.Kind, &a.Scope, &a.Status, &a.Evidence, &a.Score, &a.By, &at); err != nil {
			return nil, err
		}
		a.At = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// AliasImpact is what applying an alias would touch, shown before it is
// confirmed.
type AliasImpact struct {
	Statements, Items int
	Threads           []int64 // threads holding statements with the variant
	// NewLinks are threads with the variant but not the kept form: an
	// alias would tie them to the kept form's subject for the first time.
	NewLinks []int64
	Sample   []string // a few statements with the variant
}

// ImpactOf reads what an alias would touch. Unscoped: for the owner's
// review.
func (s *Store) ImpactOf(a LearnedAlias) (AliasImpact, error) {
	var im AliasImpact
	word := func(w string) *regexp.Regexp { return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`) }
	vre, kre := word(a.Variant), word(a.Keep)
	rows, err := s.db.Query(`SELECT st.id, st.item_id, st.text, coalesce(ti.topic_id, 0) FROM statements st
		LEFT JOIN thread_items ti ON ti.statement_id = st.id WHERE st.text LIKE ?`, "%"+a.Variant+"%")
	if err != nil {
		return im, err
	}
	items, threads, stmts := map[string]bool{}, map[int64]bool{}, map[int64]bool{}
	for rows.Next() {
		var id, tid int64
		var item, text string
		rows.Scan(&id, &item, &text, &tid)
		if !vre.MatchString(text) {
			continue
		}
		if !stmts[id] && len(im.Sample) < 5 {
			im.Sample = append(im.Sample, text)
		}
		stmts[id], items[item] = true, true
		if tid != 0 {
			threads[tid] = true
		}
	}
	rows.Close()
	im.Statements, im.Items = len(stmts), len(items)
	for tid := range threads {
		im.Threads = append(im.Threads, tid)
		has, err := s.threadMentions(tid, kre)
		if err != nil {
			return im, err
		}
		if !has {
			im.NewLinks = append(im.NewLinks, tid)
		}
	}
	return im, nil
}

func (s *Store) threadMentions(tid int64, re *regexp.Regexp) (bool, error) {
	rows, err := s.db.Query(`SELECT st.text, st.who FROM thread_items ti JOIN statements st ON st.id = ti.statement_id WHERE ti.topic_id = ?`, tid)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var text, who string
		rows.Scan(&text, &who)
		if re.MatchString(text) || re.MatchString(who) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ConfirmedSpellings maps each confirmed spelling variant to its kept form
// (kind spelling, everywhere), lower-cased, for matching names and terms.
func (s *Store) ConfirmedSpellings() (map[string]string, error) {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT keep, variant FROM learned_aliases WHERE status = 'confirmed' AND kind = 'spelling' AND scope = ''`)
	if err != nil {
		if err == sql.ErrNoRows {
			return out, nil
		}
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		out[strings.ToLower(v)] = k
	}
	return out, rows.Err()
}

// AliasUsage is one place an alias's variant is used.
type AliasUsage struct {
	Statement int64     `json:"statement"`
	ItemID    string    `json:"itemId"`
	ChunkID   int64     `json:"chunkId"`
	ItemTitle string    `json:"itemTitle"`
	At        time.Time `json:"at"`
	Text      string    `json:"text"`
	Thread    string    `json:"thread"`
	ThreadID  int64     `json:"threadId"`
	Current   bool      `json:"current"`
	HasKept   bool      `json:"hasKept"` // the thread mentions the kept form too
}

// UsagesOf lists the statements using an alias's variant, newest first.
// Unscoped: the owner's review.
func (s *Store) UsagesOf(a LearnedAlias, limit int) ([]AliasUsage, error) {
	vre := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(a.Variant) + `\b`)
	kre := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(a.Keep) + `\b`)
	rows, err := s.db.Query(`SELECT st.id, st.item_id, st.chunk_id, i.title, st.said_at, st.text, coalesce(t.label, ''), coalesce(t.id, 0), coalesce(ti.current, 0)
		FROM statements st JOIN items i ON i.id = st.item_id
		LEFT JOIN thread_items ti ON ti.statement_id = st.id LEFT JOIN topics t ON t.id = ti.topic_id
		WHERE st.text LIKE ? ORDER BY st.said_at DESC`, "%"+a.Variant+"%")
	if err != nil {
		return nil, err
	}
	var out []AliasUsage
	seen := map[int64]bool{}
	for rows.Next() {
		var u AliasUsage
		var at int64
		var cur int
		rows.Scan(&u.Statement, &u.ItemID, &u.ChunkID, &u.ItemTitle, &at, &u.Text, &u.Thread, &u.ThreadID, &cur)
		if !vre.MatchString(u.Text) || seen[u.Statement] {
			continue
		}
		seen[u.Statement] = true
		u.At, u.Current = time.Unix(at, 0), cur == 1
		out = append(out, u)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	rows.Close()
	kept := map[int64]bool{}
	for i := range out {
		t := out[i].ThreadID
		if t == 0 {
			continue
		}
		if _, ok := kept[t]; !ok {
			has, err := s.threadMentions(t, kre)
			if err != nil {
				return nil, err
			}
			kept[t] = has
		}
		out[i].HasKept = kept[t]
	}
	return out, nil
}
