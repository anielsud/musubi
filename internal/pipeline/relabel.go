package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/stages"
	"github.com/anielsud/musubi/internal/store"
)

// A speaker relabel puts a corrected speaker name on a meeting's stored
// copies with no model calls (docs/recorder-screens.md, "Renaming a voice
// profile renames its speakers"). RelabelSpeaker works out every changed
// value from the item as ingest will now translate it, checks it is a
// label change and nothing else, and has the store write it in one
// transaction; then it embeds the changed text again locally. The
// knowledge stages see their output came from the same passages before
// the relabel (store.RelabelledStage) and restamp instead of running.

// ErrNotLabelOnly: the meeting changed in more than its speaker labels,
// or its passages would break differently, so only a normal update (and
// a re-extraction) can take it in.
var ErrNotLabelOnly = errors.New("not a pure speaker relabel")

// RelabelRequest is a relabel of one meeting.
type RelabelRequest struct {
	ItemID string
	// Session is the recording's rewritten session.json, translated as
	// ingest will. Without it, every line Old speaks becomes New's.
	Session  []byte
	Old, New string
	// Batch ties the record to the recorder's relabel, for undo.
	Batch string
}

// RelabelReport is what a relabel (or its undo) changed.
type RelabelReport struct {
	ItemID string            `json:"item"`
	Names  map[string]string `json:"names"` // old → new speaker names
	Lines  int               `json:"lines"`
	// Passages, Holders, Texts (statement prose), Sections, Todos,
	// Overview, Threads and Statuses count the rows rewritten.
	Passages, Holders, Texts, Sections, Todos int
	Overview                                  bool
	Threads, Statuses                         int
	// Embedded passages and statements (again, locally); EmbedNote says
	// why some weren't (they are left to the next update).
	EmbeddedPassages, EmbeddedStatements int
	EmbedNote                            string `json:"embedNote,omitempty"`
	// HolderIDs is the entity each new name resolves to now.
	HolderIDs map[string]string `json:"holderIds,omitempty"`
	// Notes say what was left as it was, and why.
	Notes []string `json:"notes,omitempty"`
}

// RelabelSpeaker relabels a meeting's stored copies. Nothing is written
// unless the whole relabel holds.
func (r *Runner) RelabelSpeaker(ctx context.Context, req RelabelRequest) (*RelabelReport, error) {
	old, oldHash, err := r.Store.RelabelItem(req.ItemID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", req.ItemID, err)
	}
	if old.Kind != item.KindMeeting || old.Source != "tomoe" {
		return nil, fmt.Errorf("%s is not a recording", req.ItemID)
	}
	var nw item.Item
	if req.Session != nil {
		its, err := item.Translate(source.Record{Source: old.Source, Ref: old.Ref, ContentType: item.TomoeContentType, Data: req.Session}, old.RawHash)
		if err != nil || len(its) != 1 {
			return nil, fmt.Errorf("reading the session: %v", err)
		}
		nw = its[0]
	} else {
		var ok bool
		if nw, ok = item.RenameSpeaker(old, req.Old, req.New); !ok {
			return nil, fmt.Errorf("%w: %q speaks no line of %s, or %q isn't a person's name", ErrNotLabelOnly, req.Old, req.ItemID, req.New)
		}
	}
	if err := labelOnly(old, nw); err != nil {
		return nil, err
	}
	rep := &RelabelReport{ItemID: req.ItemID, Names: map[string]string{}, HolderIDs: map[string]string{}}
	if old.ContentHash() == nw.ContentHash() {
		return rep, nil
	}
	targets := map[string]map[string]bool{}
	for i := range old.Segments {
		o, n := old.Segments[i].Speaker, nw.Segments[i].Speaker
		if o == n {
			continue
		}
		rep.Lines++
		if targets[o] == nil {
			targets[o] = map[string]bool{}
		}
		targets[o][n] = true
	}
	stillSpoken := map[string]bool{}
	for _, s := range nw.Segments {
		stillSpoken[s.Speaker] = true
	}
	// gone maps an old name no line has any more, renamed to one new
	// name, to that name: only such a name can be replaced where the text
	// doesn't say who (lists of people, prose).
	gone := map[string]string{}
	for o, ns := range targets {
		var list []string
		for n := range ns {
			list = append(list, n)
		}
		sort.Strings(list)
		rep.Names[o] = strings.Join(list, ", ")
		if len(list) == 1 && !stillSpoken[o] {
			gone[o] = list[0]
		} else {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%q still names other lines here: lists and prose that mention it are left", o))
		}
	}

	// The passages as the chunk stage makes them, from the item as
	// stitched with its Teams transcript, before and after.
	items, err := r.Store.AllItems()
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range items {
		if items[i].ID == req.ItemID {
			idx = i
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%s: not found", req.ItemID)
	}
	items[idx] = old
	stOld, err := r.stitchedOne(items, idx)
	if err != nil {
		return nil, err
	}
	items[idx] = nw
	stNew, err := r.stitchedOne(items, idx)
	if err != nil {
		return nil, err
	}
	links, err := r.Store.LinksFrom("occurred_as")
	if err != nil {
		return nil, err
	}
	var linked *item.Item
	if l, ok := links[req.ItemID]; ok {
		for i := range items {
			if items[i].ID == l {
				linked = &items[i]
			}
		}
	}
	given, err := r.Store.GivenTitles()
	if err != nil {
		return nil, err
	}
	title := r.passageTitle(&old, linked, given[req.ItemID].Title)
	oldCS := hashOf("chunk", stages.ChunkVersion, stOld.ContentHash(), title)
	newCS := hashOf("chunk", stages.ChunkVersion, stNew.ContentHash(), title)
	curChunk, err := r.Store.StageStamp("chunk", req.ItemID)
	if err != nil {
		return nil, err
	}
	if curChunk != stages.ChunkVersion+"/"+oldCS {
		return nil, fmt.Errorf("%w: %s isn't up to date in Musubi (its passages wait for an update)", ErrNotLabelOnly, req.ItemID)
	}
	stored, err := r.Store.Chunks(req.ItemID)
	if err != nil {
		return nil, err
	}
	before, after := stages.Chunk(stOld, title), stages.Chunk(stNew, title)
	if len(before) != len(stored) || len(after) != len(stored) {
		return nil, fmt.Errorf("%w: its passages would break differently", ErrNotLabelOnly)
	}
	plan := store.SpeakerRelabel{ItemID: req.ItemID, Names: map[string]string{}, ContentHash: nw.ContentHash(), ChunkStamp: stages.ChunkVersion + "/" + newCS}
	for o, n := range rep.Names {
		plan.Names[o] = n
	}
	b, _ := json.Marshal(nw.Segments)
	plan.Segments = string(b)
	b, _ = json.Marshal(nw.Participants)
	plan.Participants = string(b)
	b, _ = json.Marshal(nw.Native)
	plan.Native = string(b)
	chunkByID := map[int64]int{}
	var changed []store.Chunk
	for i, c := range stored {
		o, n := before[i], after[i]
		switch {
		case c.Seq != o.Seq || c.SegFrom != o.SegFrom || c.SegTo != o.SegTo:
			return nil, fmt.Errorf("%w: passage %d covers lines %d-%d, the chunk stage makes %d-%d", ErrNotLabelOnly, c.Seq, c.SegFrom, c.SegTo, o.SegFrom, o.SegTo)
		case c.Text != o.Text || c.Speakers != o.Speakers:
			return nil, fmt.Errorf("%w: passage %d's text isn't what the chunk stage makes of it", ErrNotLabelOnly, c.Seq)
		case n.SegFrom != o.SegFrom || n.SegTo != o.SegTo || n.Start != o.Start || n.End != o.End:
			return nil, fmt.Errorf("%w: passage %d would break differently", ErrNotLabelOnly, c.Seq)
		}
		chunkByID[c.ID] = i
		if n.Text != c.Text || n.Speakers != c.Speakers {
			plan.Chunks = append(plan.Chunks, store.ChunkRelabel{ID: c.ID, Text: n.Text, Speakers: n.Speakers})
			c.Text, c.Speakers = n.Text, n.Speakers
			changed = append(changed, c)
		}
	}
	rep.Passages = len(plan.Chunks)

	// Who each passage line is, before and after (1-based, as statements
	// cite them).
	lineSpeakers := func(it item.Item, c store.Chunk) []string {
		var out []string
		for i := c.SegFrom; i < c.SegTo && i < len(it.Segments); i++ {
			if len(strings.Fields(it.Segments[i].Text)) == 0 {
				continue
			}
			out = append(out, it.Segments[i].Speaker)
		}
		return out
	}
	spokenText := strings.Builder{}
	for _, s := range stNew.Segments {
		spokenText.WriteString(s.Text + "\n")
	}
	// prose replaces gone names in text written from the labels, unless
	// the name is also said in the meeting (then it may mean something
	// else).
	var proseNames []string
	for o := range gone {
		if wordRe(o).MatchString(spokenText.String()) {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%q is said in the meeting: prose that mentions it is left", o))
			continue
		}
		proseNames = append(proseNames, o)
	}
	sort.Strings(proseNames)
	prose := func(s string) string {
		for _, o := range proseNames {
			s = replaceWord(s, o, gone[o])
		}
		return s
	}
	// name maps an entry of a list of people.
	name := func(s string) string {
		if n, ok := gone[s]; ok {
			return n
		}
		for o, n := range gone {
			if wordRe(o).MatchString(s) {
				return n
			}
		}
		return s
	}
	nameList := func(js string) string {
		var names []string
		if js == "" || json.Unmarshal([]byte(js), &names) != nil {
			return js
		}
		var out []string
		seen := map[string]bool{}
		for _, n := range names {
			m := name(n)
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
		b, _ := json.Marshal(out)
		if string(b) == js || (len(out) == len(names) && sameStrings(out, names)) {
			return js
		}
		return string(b)
	}

	reg, err := registry.Build(r.Cfg, r.CuratedPath, items)
	if err != nil {
		return nil, err
	}
	holderID := func(h string) string {
		if h == "You" || strings.EqualFold(h, r.Cfg.Owner.Name) {
			return reg.Owner()
		}
		return reg.Participant(item.Participant{Name: h})
	}
	for _, ns := range targets {
		for n := range ns {
			rep.HolderIDs[n] = holderID(n)
		}
	}

	sts, err := r.Store.RelabelStatements(req.ItemID)
	if err != nil {
		return nil, err
	}
	newText := map[int64]string{}
	for _, st := range sts {
		upd := st.StatementRelabel
		line := st.Line
		if line <= 0 {
			line = evidenceLine(st.Evidence, st.ChunkID)
		}
		if i, ok := chunkByID[st.ChunkID]; ok && line > 0 {
			ob, na := lineSpeakers(stOld, stored[i]), lineSpeakers(stNew, stored[i])
			if line <= len(ob) && line <= len(na) && st.Holder == ob[line-1] && na[line-1] != ob[line-1] {
				upd.Holder = na[line-1]
			}
		}
		if upd.Holder == st.Holder {
			if n, ok := gone[st.Holder]; ok {
				upd.Holder = n
			}
		}
		if upd.Holder != st.Holder {
			upd.HolderID = holderID(upd.Holder)
			// The holder's own entry in who, by the line, then the rest.
			var who []string
			if json.Unmarshal([]byte(st.Who), &who) == nil {
				for k := range who {
					if who[k] == st.Holder {
						who[k] = upd.Holder
					}
				}
				b, _ := json.Marshal(who)
				upd.Who = string(b)
			}
		}
		upd.Who = nameList(upd.Who)
		if !st.Corrected {
			upd.Text = prose(st.Text)
		} else if prose(st.Text) != st.Text {
			rep.Notes = append(rep.Notes, fmt.Sprintf("statement %d: corrected by you, its text is left", st.ID))
		}
		if upd.Text != st.Text {
			upd.SearchText = knowledge.SearchForm(upd.Text)
			newText[st.ID] = upd.Text
			rep.Texts++
		}
		if upd.Holder != st.Holder {
			rep.Holders++
		}
		if upd != st.StatementRelabel {
			plan.Statements = append(plan.Statements, upd)
		}
	}

	overview, sections, todos, err := r.Store.RelabelDigest(req.ItemID)
	if err != nil {
		return nil, err
	}
	if overview != nil {
		if n := prose(*overview); n != *overview {
			plan.Overview, rep.Overview = &n, true
		}
	}
	for _, x := range sections {
		y := store.SectionRelabel{N: x.N, Subject: prose(x.Subject), Text: prose(x.Text), People: nameList(x.People)}
		if y != x {
			plan.Sections = append(plan.Sections, y)
		}
	}
	for _, x := range todos {
		y := store.TodoRelabel{ID: x.ID, Text: prose(x.Text), Owners: nameList(x.Owners), Requester: name(x.Requester)}
		if y != x {
			plan.Todos = append(plan.Todos, y)
		}
	}
	rep.Sections, rep.Todos = len(plan.Sections), len(plan.Todos)

	// Threads holding its statements: a name or summary naming a gone
	// name changes only once no statement in the thread still has it;
	// statuses whose input changed only by this are restamped.
	topics, err := r.Store.RelabelTopics(req.ItemID)
	if err != nil {
		return nil, err
	}
	fix, err := r.Store.Corrector()
	if err != nil {
		return nil, err
	}
	for _, tp := range topics {
		tsts, err := r.Store.ThreadStatements(tp.ID)
		if err != nil {
			return nil, err
		}
		after := append([]store.ThreadStatement(nil), tsts...)
		for i := range after {
			if t, ok := newText[after[i].ID]; ok {
				after[i].Text = t
			}
		}
		clear := true
		for _, s := range after {
			for _, o := range proseNames {
				if wordRe(o).MatchString(s.Text) {
					clear = false
				}
			}
		}
		threadProse := func(s string) string {
			if !clear {
				return s
			}
			return prose(s)
		}
		ntp := store.TopicRelabel{ID: tp.ID, Label: threadProse(tp.Label), Summary: threadProse(tp.Summary)}
		if ntp != tp {
			plan.Topics = append(plan.Topics, ntp)
			rep.Threads++
		}
		for i := range tsts {
			tsts[i].Text = fix.Text(tsts[i].ID, tsts[i].Text)
			after[i].Text = fix.Text(after[i].ID, after[i].Text)
		}
		bFull, bOpen := statusInputs(tp.Label, tp.Summary, tsts)
		aFull, aOpen := statusInputs(ntp.Label, ntp.Summary, after)
		rows, err := r.Store.ThreadStatusRows(tp.ID)
		if err != nil {
			return nil, err
		}
		for variant, row := range rows {
			was, now := bFull.Hash, aFull.Hash
			if variant == "open" {
				was, now = bOpen.Hash, aOpen.Hash
			}
			if row.Version != knowledge.StatusVersion || row.InputHash != was || was == now {
				continue
			}
			plan.Statuses = append(plan.Statuses, store.StatusRelabel{Topic: tp.ID, Variant: variant, Was: was, InputHash: now, Body: threadProse(row.Body)})
		}
	}
	rep.Statuses = len(plan.Statuses)
	if req.Batch == "" {
		req.Batch = "musubi"
	}
	if err := r.Store.RelabelSpeaker(req.Batch, oldHash, curChunk, plan); err != nil {
		return nil, err
	}

	var stmts []store.StatementRelabel
	for _, st := range plan.Statements {
		if _, ok := newText[st.ID]; ok {
			stmts = append(stmts, st)
		}
	}
	r.embedRelabelled(ctx, rep, req.ItemID, oldCS, newCS, title, changed, stmts)
	return rep, nil
}

// UndoRelabel puts back what a batch's relabel of an item changed in
// Musubi, and embeds the restored text again.
func (r *Runner) UndoRelabel(ctx context.Context, batch, itemID string) (*RelabelReport, error) {
	before, after, err := r.Store.UndoRelabel(batch, itemID)
	if err != nil {
		return nil, err
	}
	rep := &RelabelReport{ItemID: itemID, Names: before.Names, Passages: len(before.Chunks), Statuses: len(before.Statuses),
		Sections: len(before.Sections), Todos: len(before.Todos), Overview: before.Overview != nil, Threads: len(before.Topics)}
	chunks, err := r.Store.Chunks(itemID)
	if err != nil {
		return nil, err
	}
	restored := map[int64]bool{}
	for _, c := range before.Chunks {
		restored[c.ID] = true
	}
	var changed []store.Chunk
	title := ""
	for _, c := range chunks {
		title = c.Title
		if restored[c.ID] {
			changed = append(changed, c)
		}
	}
	afterSt := map[int64]store.StatementRelabel{}
	for _, st := range after.Statements {
		afterSt[st.ID] = st
	}
	var stmts []store.StatementRelabel
	for _, st := range before.Statements {
		if afterSt[st.ID].Holder != st.Holder {
			rep.Holders++
		}
		if afterSt[st.ID].Text != st.Text {
			stmts = append(stmts, st)
			rep.Texts++
		}
	}
	_, wasCS, _ := strings.Cut(after.ChunkStamp, "/")
	_, nowCS, _ := strings.Cut(before.ChunkStamp, "/")
	r.embedRelabelled(ctx, rep, itemID, wasCS, nowCS, title, changed, stmts)
	return rep, nil
}

// embedRelabelled embeds the passages and statements whose text a relabel
// changed, with the local embedders, and stamps the embed stage where it
// was up to date before. On failure the passages are left to the next
// update (their stamp stays stale) and the statements to the next
// knowledge run (they have no vector).
func (r *Runner) embedRelabelled(ctx context.Context, rep *RelabelReport, itemID, oldCS, newCS, title string, changed []store.Chunk, stmts []store.StatementRelabel) {
	note := func(f string, a ...any) {
		if rep.EmbedNote != "" {
			rep.EmbedNote += "; "
		}
		rep.EmbedNote += fmt.Sprintf(f, a...)
	}
	if len(r.Embedders) == 0 {
		note("no embedder: %d passages and %d statements left for the next update", len(changed), len(stmts))
		return
	}
	for _, e := range r.Embedders {
		if !e.Local() {
			note("%s isn't local: left for the next update", e.Model())
			continue
		}
		stage := "embed/" + e.Model()
		cur, _ := r.Store.StageStamp(stage, itemID)
		if cur != stages.EmbedVersion+"/"+hashOf(stage, stages.EmbedVersion, oldCS) {
			continue // not up to date before: the next update embeds it whole
		}
		for i := range changed {
			changed[i].Title = title
		}
		vecs, err := stages.Embed(ctx, e, changed)
		if err != nil {
			note("passages left for the next update (%v)", err)
			continue
		}
		if err := r.Store.UpdateChunkVecs(itemID, stage, e.Model(), stages.EmbedVersion, hashOf(stage, stages.EmbedVersion, newCS), vecs); err != nil {
			note("passages left for the next update (%v)", err)
			continue
		}
		rep.EmbeddedPassages += len(vecs)
	}
	if len(stmts) == 0 {
		return
	}
	e := r.Embedders[0]
	if !e.Local() {
		return
	}
	texts := make([]string, len(stmts))
	for i, s := range stmts {
		texts[i] = s.Text
	}
	vs, err := e.Embed(ctx, texts, false)
	if err != nil || len(vs) != len(stmts) {
		note("statements left for the next knowledge run (%v)", err)
		return
	}
	for i, s := range stmts {
		if err := r.Store.PutStatementVec(s.ID, e.Model(), vs[i]); err != nil {
			note("statements left for the next knowledge run (%v)", err)
			return
		}
		rep.EmbeddedStatements++
	}
}

// stitchedOne is items[idx] as the chunk stage reads it: stitched with
// its meeting's Teams transcript when knowledge.stitch says so.
func (r *Runner) stitchedOne(items []item.Item, idx int) (item.Item, error) {
	st, _, err := r.stitches(items, false)
	if err != nil {
		return item.Item{}, err
	}
	if s, ok := st[items[idx].ID]; ok {
		return s, nil
	}
	return items[idx], nil
}

// labelOnly checks nw is old with only speakers (and the participants
// and unknown-speaker flag that follow from them) changed.
func labelOnly(old, nw item.Item) error {
	// Times to the second, as items store them and ContentHash reads them.
	if old.Kind != nw.Kind || old.Title != nw.Title || old.OccurredAt.Unix() != nw.OccurredAt.Unix() || old.EndedAt.Unix() != nw.EndedAt.Unix() ||
		old.Thread != nw.Thread || len(old.Segments) != len(nw.Segments) {
		return fmt.Errorf("%w: the recording changed in more than its speakers", ErrNotLabelOnly)
	}
	for i := range old.Segments {
		a, b := old.Segments[i], nw.Segments[i]
		a.Speaker, b.Speaker = "", ""
		if a != b {
			return fmt.Errorf("%w: line %d changed in more than its speaker", ErrNotLabelOnly, i)
		}
	}
	for k, v := range old.Native {
		if k != "unknown_participants" && nw.Native[k] != v {
			return fmt.Errorf("%w: the recording changed in more than its speakers", ErrNotLabelOnly)
		}
	}
	for k, v := range nw.Native {
		if k != "unknown_participants" && old.Native[k] != v {
			return fmt.Errorf("%w: the recording changed in more than its speakers", ErrNotLabelOnly)
		}
	}
	return nil
}

// evidenceLine is the first line a statement's evidence cites in its own
// passage (0 for none).
func evidenceLine(ev string, chunkID int64) int {
	var es []struct {
		ChunkID int64 `json:"chunkId"`
		Line    int   `json:"line"`
	}
	if json.Unmarshal([]byte(ev), &es) != nil {
		return 0
	}
	for _, e := range es {
		if e.ChunkID == chunkID && e.Line > 0 {
			return e.Line
		}
	}
	return 0
}

// wordRe matches name as a whole word, ignoring case.
func wordRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^\pL\pN])(` + regexp.QuoteMeta(name) + `)($|[^\pL\pN])`)
}

// replaceWord replaces name, as a whole word, with to.
func replaceWord(s, name, to string) string {
	return wordRe(name).ReplaceAllString(s, "${1}"+strings.ReplaceAll(to, "$", "$$")+"${3}")
}

func sameStrings(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return len(a) == len(b)
}
