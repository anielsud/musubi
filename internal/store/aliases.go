package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	HasKept   bool      `json:"hasKept"`       // the thread mentions the kept form too
	New       bool      `json:"new,omitempty"` // not reviewed yet (a rule for some uses)
}

// UsagesOf lists the statements using an alias's variant, newest first.
// Unscoped: the owner's review.
func (s *Store) UsagesOf(a LearnedAlias, limit int) ([]AliasUsage, error) {
	vre := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(a.Variant) + `\b`)
	kre := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(a.Keep) + `\b`)
	rows, err := s.db.Query(`SELECT st.id, st.item_id, st.chunk_id, `+displayTitle("i")+`, st.said_at, st.text, coalesce(t.label, ''), coalesce(t.id, 0), coalesce(ti.current, 0)
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

// aliasScope is a partly confirmed alias's reach: the uses it was
// confirmed for, and every use the owner has reviewed (confirmed or not).
type aliasScope struct {
	Statements []int64 `json:"statements"`
	Reviewed   []int64 `json:"reviewed,omitempty"`
	// ReplacedBy: a proposal the owner answered with another right form
	// (status replaced), kept so it isn't proposed again.
	ReplacedBy int64 `json:"replacedBy,omitempty"`
}

func parseScope(s string) aliasScope {
	var sc aliasScope
	json.Unmarshal([]byte(s), &sc)
	return sc
}

func union(a, b []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, x := range append(append([]int64{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// ConfirmAliasIn confirms an alias for some of its uses only (the
// statements given): "Wade" is Wayde where it's about the person, the verb
// elsewhere. reviewed are all the uses the owner looked at; uses made
// later come back for review (UnreviewedUses). Called again, it adds to
// what was confirmed and reviewed before.
func (s *Store) ConfirmAliasIn(id int64, statements, reviewed []int64, by string) error {
	var old string
	s.db.QueryRow(`SELECT scope FROM learned_aliases WHERE id = ?`, id).Scan(&old)
	sc := parseScope(old)
	sc.Statements = union(sc.Statements, statements)
	sc.Reviewed = union(union(sc.Reviewed, reviewed), statements)
	b, _ := json.Marshal(sc)
	_, err := s.db.Exec(`UPDATE learned_aliases SET status = 'confirmed', scope = ?, by = ?, at = ? WHERE id = ?`, string(b), by, time.Now().Unix(), id)
	return err
}

// Reviewed returns the uses of a partly confirmed alias the owner has
// looked at; nil for one decided everywhere.
func (a LearnedAlias) Reviewed() map[int64]bool {
	if a.Scope == "" {
		return nil
	}
	out := map[int64]bool{}
	for _, id := range parseScope(a.Scope).Reviewed {
		out[id] = true
	}
	for _, id := range parseScope(a.Scope).Statements {
		out[id] = true
	}
	return out
}

// ScopedSpellings maps statements to the aliases confirmed for them only
// (statement → variant lower-cased → kept form).
func (s *Store) ScopedSpellings() (map[int64]map[string]string, error) {
	rows, err := s.db.Query(`SELECT keep, variant, scope FROM learned_aliases WHERE status = 'confirmed' AND scope != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]string{}
	for rows.Next() {
		var k, v, sc string
		rows.Scan(&k, &v, &sc)
		for _, id := range parseScope(sc).Statements {
			if out[id] == nil {
				out[id] = map[string]string{}
			}
			out[id][strings.ToLower(v)] = k
		}
	}
	return out, rows.Err()
}

// DropProposedAliases removes undecided proposals (decided ones stay).
func (s *Store) DropProposedAliases() error {
	_, err := s.db.Exec(`DELETE FROM learned_aliases WHERE status = 'proposed'`)
	return err
}

// AddAlias records an alias the owner names (variant → keep), proposed so
// it can be reviewed use by use; an existing one is returned.
func (s *Store) AddAlias(variant, keep, by string) (int64, error) {
	if _, err := s.db.Exec(`INSERT INTO learned_aliases(keep, variant, kind, status, by, at) VALUES (?,?,'spelling','proposed',?,?)
		ON CONFLICT(keep, variant, kind) DO NOTHING`, keep, variant, by, time.Now().Unix()); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM learned_aliases WHERE keep = ? AND variant = ? AND kind = 'spelling'`, keep, variant).Scan(&id)
	return id, err
}

// Alias rule modes: what the owner decided an alias means.
const (
	RuleAlways  = "always"   // every use is the kept form, new meetings too
	RuleThese   = "these"    // the given uses only; new uses come back for review
	RuleApart   = "apart"    // both forms are right: nothing changes, not proposed again
	RuleReview  = "review"   // undecided (proposed)
	RuleReplace = "replaced" // answered with another right form (ReplacedBy)
)

// Mode is the alias's rule mode.
func (a LearnedAlias) Mode() string {
	switch a.Status {
	case "confirmed":
		if a.Scope == "" {
			return RuleAlways
		}
		return RuleThese
	case "rejected":
		return RuleApart
	case "replaced":
		return RuleReplace
	}
	return RuleReview
}

// Only lists the statements a "these" rule applies to.
func (a LearnedAlias) Only() []int64 {
	if a.Mode() != RuleThese {
		return nil
	}
	return parseScope(a.Scope).Statements
}

// ReplacedBy is the alias that replaced this one, 0 if none.
func (a LearnedAlias) ReplacedBy() int64 {
	if a.Status != "replaced" {
		return 0
	}
	return parseScope(a.Scope).ReplacedBy
}

// SetAliasRule sets what an alias means, outright (an edit replaces what
// was decided before rather than adding to it): RuleAlways, RuleThese with
// the statements it applies to (reviewed: every use the owner looked at;
// uses made later come back for review), RuleApart, or RuleReview to undo
// the decision.
func (s *Store) SetAliasRule(id int64, mode string, statements, reviewed []int64, by string) error {
	var status, scope string
	switch mode {
	case RuleAlways:
		status = "confirmed"
	case RuleThese:
		if len(statements) == 0 {
			return fmt.Errorf("a rule for some uses needs at least one")
		}
		b, _ := json.Marshal(aliasScope{Statements: union(nil, statements), Reviewed: union(reviewed, statements)})
		status, scope = "confirmed", string(b)
	case RuleApart:
		status = "rejected"
	case RuleReview:
		status = "proposed"
	default:
		return fmt.Errorf("unknown rule %q", mode)
	}
	res, err := s.db.Exec(`UPDATE learned_aliases SET status = ?, scope = ?, by = ?, at = ? WHERE id = ?`, status, scope, by, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no alias %d", id)
	}
	return nil
}

// MarkAliasReplaced records that the owner answered a proposal with
// another right form (the alias with) rather than deciding it.
func (s *Store) MarkAliasReplaced(id, with int64, by string) error {
	b, _ := json.Marshal(aliasScope{ReplacedBy: with})
	_, err := s.db.Exec(`UPDATE learned_aliases SET status = 'replaced', scope = ?, by = ?, at = ? WHERE id = ?`, string(b), by, time.Now().Unix(), id)
	return err
}

// StatementsUsing lists the statements whose text uses word (whole word,
// any case).
func (s *Store) StatementsUsing(word string) ([]int64, error) {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
	rows, err := s.db.Query(`SELECT id, text FROM statements WHERE text LIKE ?`, "%"+word+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		if re.MatchString(t) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// markReplacedAliases: before the replaced status, a proposal answered
// with another right form was marked rejected, in the same moment the new
// alias was made. Those are replaced, not the owner's rejection.
func markReplacedAliases(db *sql.DB) error {
	_, err := db.Exec(`UPDATE learned_aliases SET status = 'replaced',
		scope = (SELECT json_object('replacedBy', n.id) FROM learned_aliases n WHERE n.variant = learned_aliases.variant AND n.kind = learned_aliases.kind
			AND n.keep != learned_aliases.keep AND n.by = 'owner' AND n.status != 'rejected' AND n.id > learned_aliases.id AND abs(n.at - learned_aliases.at) <= 2 ORDER BY n.id LIMIT 1)
		WHERE status = 'rejected' AND by = 'owner' AND EXISTS (SELECT 1 FROM learned_aliases n WHERE n.variant = learned_aliases.variant AND n.kind = learned_aliases.kind
			AND n.keep != learned_aliases.keep AND n.by = 'owner' AND n.status != 'rejected' AND n.id > learned_aliases.id AND abs(n.at - learned_aliases.at) <= 2)`)
	return err
}
