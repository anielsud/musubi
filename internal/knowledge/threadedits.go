package knowledge

import (
	"fmt"
	"time"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// The owner's thread edits (docs/incremental-design.md): merging two
// threads about one subject, or splitting statements out to a thread of
// their own. Both are local and need no model: memberships move, "current"
// is derived again from the relations (which are between statements, not
// threads), and the threads' statuses are dropped to be written again.
// Each edit is recorded to the side by statement, and ApplyThreadEdits
// does it again wherever a rethread put those statements.

// MergeThreads joins from into into, keeping into's name.
func MergeThreads(st *store.Store, into, from int64, by string) (int64, error) {
	if into == from {
		return 0, fmt.Errorf("a thread can't be merged into itself")
	}
	fromIDs, err := st.ThreadStatementIDs(from)
	if err != nil {
		return 0, err
	}
	intoIDs, err := st.ThreadStatementIDs(into)
	if err != nil {
		return 0, err
	}
	if len(fromIDs) == 0 || len(intoIDs) == 0 {
		return 0, fmt.Errorf("both threads need statements")
	}
	name, _, err := st.ThreadName(into)
	if err != nil {
		return 0, err
	}
	oldName, _, err := st.ThreadName(from)
	if err != nil {
		return 0, err
	}
	moved, err := st.StatementKeys(fromIDs)
	if err != nil {
		return 0, err
	}
	stay, err := st.StatementKeys(intoIDs)
	if err != nil {
		return 0, err
	}
	if err := supersede(st, moved, stay); err != nil {
		return 0, err
	}
	id, err := st.RecordThreadEdit(store.ThreadEdit{Kind: "merge", Moved: moved, Stay: stay, Name: name, OldName: oldName, By: by})
	if err != nil {
		return 0, err
	}
	return id, move(st, from, into, fromIDs, fmt.Sprintf("edit %d", id))
}

// SplitThread moves statements of a thread out to a new thread with the
// given name, and returns it.
func SplitThread(st *store.Store, from int64, ids []int64, name, by string) (int64, int64, error) {
	all, err := st.ThreadStatementIDs(from)
	if err != nil {
		return 0, 0, err
	}
	in := map[int64]bool{}
	for _, id := range ids {
		in[id] = true
	}
	var moving, rest []int64
	for _, id := range all {
		if in[id] {
			moving = append(moving, id)
		} else {
			rest = append(rest, id)
		}
	}
	if len(moving) == 0 || len(rest) == 0 {
		return 0, 0, fmt.Errorf("a split moves some of a thread's statements, not none or all")
	}
	oldName, scope, err := st.ThreadName(from)
	if err != nil {
		return 0, 0, err
	}
	moved, err := st.StatementKeys(moving)
	if err != nil {
		return 0, 0, err
	}
	stay, err := st.StatementKeys(rest)
	if err != nil {
		return 0, 0, err
	}
	if err := supersede(st, moved, stay); err != nil {
		return 0, 0, err
	}
	eid, err := st.RecordThreadEdit(store.ThreadEdit{Kind: "split", Moved: moved, Stay: stay, Name: name, OldName: oldName, By: by})
	if err != nil {
		return 0, 0, err
	}
	to, err := st.NewThread(name, scope, sensitivity.Public)
	if err != nil {
		return 0, 0, err
	}
	return to, eid, move(st, from, to, moving, fmt.Sprintf("edit %d", eid))
}

// UndoThreadEdit reverses an edit where its statements are now: a merge's
// statements go back out under their old name, a split's back in.
func UndoThreadEdit(st *store.Store, id int64) error {
	es, err := st.ThreadEdits(false)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.ID != id {
			continue
		}
		if err := st.MarkThreadEditUndone(id); err != nil {
			return err
		}
		// The reverse of a merge is a split of what it moved; of a split,
		// a merge of what it moved.
		rev := e
		if e.Kind == "merge" {
			rev.Kind, rev.Name = "split", e.OldName
		} else {
			rev.Kind = "merge"
		}
		_, err := applyEdit(st, rev)
		return err
	}
	return fmt.Errorf("no edit %d to undo", id)
}

// ApplyThreadEdits does the recorded edits again where they no longer hold
// (after a rethread), in the order they were made. It returns how many it
// applied; edits that hold already are left alone.
func ApplyThreadEdits(st *store.Store) (int, error) {
	es, err := st.ThreadEdits(false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range es {
		did, err := applyEdit(st, e)
		if err != nil {
			return n, fmt.Errorf("thread edit %d: %w", e.ID, err)
		}
		if did {
			n++
		}
	}
	return n, nil
}

// applyEdit makes one edit hold: a merge, its moved statements in the
// thread holding most of its others; a split, its moved statements out of
// that thread. Statements no longer there (forgotten, re-extracted away)
// are passed over.
func applyEdit(st *store.Store, e store.ThreadEdit) (bool, error) {
	moved, err := st.ResolveKeys(e.Moved)
	if err != nil {
		return false, err
	}
	stay, err := st.ResolveKeys(e.Stay)
	if err != nil {
		return false, err
	}
	where, err := st.ThreadOf(append(append([]int64{}, moved...), stay...))
	if err != nil {
		return false, err
	}
	home := majority(stay, where)
	if home == 0 {
		return false, nil
	}
	why := fmt.Sprintf("edit %d", e.ID)
	switch e.Kind {
	case "merge":
		byThread := map[int64][]int64{}
		for _, id := range moved {
			if t := where[id]; t != 0 && t != home {
				byThread[t] = append(byThread[t], id)
			}
		}
		for t, ids := range byThread {
			if err := move(st, t, home, ids, why); err != nil {
				return false, err
			}
		}
		return len(byThread) > 0, nil
	case "split":
		var out []int64
		for _, id := range moved {
			if where[id] == home {
				out = append(out, id)
			}
		}
		if len(out) == 0 {
			return false, nil
		}
		// Into the thread holding the rest of what was split out, if a
		// rethread kept some apart; else a new one.
		var to int64
		for _, id := range moved {
			if t := where[id]; t != 0 && t != home {
				to = t
				break
			}
		}
		if to == 0 {
			_, scope, err := st.ThreadName(home)
			if err != nil {
				return false, err
			}
			if to, err = st.NewThread(e.Name, scope, sensitivity.Public); err != nil {
				return false, err
			}
		}
		return true, move(st, home, to, out, why)
	}
	return false, fmt.Errorf("unknown edit %q", e.Kind)
}

// supersede retires earlier edits a new one decides again: those naming
// statements on both of its sides (a merge of what was split, a split of
// what was merged). Kept, they would undo each other on every run.
func supersede(st *store.Store, moved, stay []store.StatementKey) error {
	side := func(ks []store.StatementKey) map[store.StatementKey]bool {
		m := map[store.StatementKey]bool{}
		for _, k := range ks {
			m[k] = true
		}
		return m
	}
	a, b := side(moved), side(stay)
	es, err := st.ThreadEdits(false)
	if err != nil {
		return err
	}
	for _, e := range es {
		inA, inB := false, false
		for _, k := range append(append([]store.StatementKey{}, e.Moved...), e.Stay...) {
			inA = inA || a[k]
			inB = inB || b[k]
		}
		if inA && inB {
			if err := st.MarkThreadEditUndone(e.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// majority is the thread holding most of ids (0 if none is in one).
func majority(ids []int64, where map[int64]int64) int64 {
	count := map[int64]int{}
	var best int64
	for _, id := range ids {
		if t := where[id]; t != 0 {
			count[t]++
			if count[t] > count[best] || (count[t] == count[best] && t < best) {
				best = t
			}
		}
	}
	return best
}

func move(st *store.Store, from, to int64, ids []int64, why string) error {
	if err := st.MoveStatements(from, to, ids); err != nil {
		return err
	}
	if err := st.DeleteEmptyThreads(map[int64]bool{from: true}); err != nil {
		return err
	}
	return RederiveThreads(st, map[int64]bool{from: true, to: true}, time.Time{}, store.Provenance{During: why, Why: "owner edit"})
}
