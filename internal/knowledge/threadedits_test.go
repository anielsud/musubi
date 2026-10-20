package knowledge

import (
	"context"
	"testing"
	"time"
)

// Split statements out of a thread; a rethread from scratch joins them
// again and the split is made again. Merging them back supersedes the
// split, so the two don't undo each other; undoing the merge splits them.
func TestThreadEdits(t *testing.T) {
	f := newCardFixture(t)
	thread := func() {
		f.st.ClearThreads()
		th := &Threader{Store: f.st, LLM: &insertLLM{}, Chunk: 70, Parallel: 2, Version: "v-test"}
		for _, it := range []ThreadItem{{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}, {ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)}} {
			if _, err := th.Thread(context.Background(), it); err != nil {
				t.Fatal(err)
			}
		}
	}
	agree := func(when string) {
		c, err := CheckDerived(f.st, time.Time{})
		if err != nil || c.Agree != c.Memberships {
			t.Errorf("%s: derived and stored disagree: %+v %v", when, c, err)
		}
	}
	thread()
	rows, _ := f.st.Threads()
	src := rows[0].ID
	ids, _ := f.st.ThreadStatementIDs(src)
	if len(ids) < 3 {
		t.Fatalf("fixture thread has %d statements", len(ids))
	}
	out, keep := ids[:1], ids[1]
	apart := func() bool {
		where, _ := f.st.ThreadOf([]int64{out[0], keep})
		return where[out[0]] != where[keep] && where[out[0]] != 0
	}
	to, split, err := SplitThread(f.st, src, out, "Split out", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.ThreadStatementIDs(to); len(got) != 1 || got[0] != out[0] {
		t.Errorf("split thread holds %v", got)
	}
	agree("split")
	thread()
	if apart() {
		t.Fatal("precondition: the rethread joins them")
	}
	if n, err := ApplyThreadEdits(f.st); err != nil || n != 1 || !apart() {
		t.Fatalf("split not applied after the rethread: %d, %v", n, err)
	}
	if n, _ := ApplyThreadEdits(f.st); n != 0 {
		t.Errorf("applied again where it holds: %d", n)
	}
	agree("split again")
	where, _ := f.st.ThreadOf([]int64{out[0], keep})
	merge, err := MergeThreads(f.st, where[keep], where[out[0]], "owner")
	if err != nil {
		t.Fatal(err)
	}
	if apart() {
		t.Error("not merged")
	}
	if n, _ := ApplyThreadEdits(f.st); n != 0 || apart() {
		t.Errorf("the split it superseded came back: %d", n)
	}
	agree("merged")
	if es, _ := f.st.ThreadEdits(false); len(es) != 1 || es[0].ID != merge || split == merge {
		t.Errorf("edits in force %+v", es)
	}
	if err := UndoThreadEdit(f.st, merge); err != nil {
		t.Fatal(err)
	}
	if !apart() {
		t.Error("merge not undone")
	}
	agree("undone")
}

// Overlapped threading (each item's review while the next is threaded)
// ends with the same threads and the same current items as one item after
// another.
func TestThreadOverlapSameResult(t *testing.T) {
	items := []ThreadItem{{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}, {ID: "m:3", Start: time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)}, {ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)}}
	state := func(f *cardFixture) map[int64]bool {
		ms, _ := f.st.ThreadMembers()
		rows, _ := f.st.Threads()
		cur := map[int64]bool{}
		for _, r := range rows {
			for _, id := range r.Current {
				cur[id] = true
			}
		}
		out := map[int64]bool{}
		for _, m := range ms {
			out[m.Statement] = cur[m.Statement]
		}
		return out
	}
	f1 := newCardFixture(t)
	f1.st.ClearThreads()
	th := &Threader{Store: f1.st, LLM: &insertLLM{}, Chunk: 70, Parallel: 2, Version: "v-test", AuditFixes: true}
	for _, it := range items {
		if _, err := th.Thread(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	want := state(f1)
	f2 := newCardFixture(t)
	f2.st.ClearThreads()
	th2 := &Threader{Store: f2.st, LLM: &insertLLM{}, Chunk: 70, Parallel: 2, Version: "v-test", AuditFixes: true, Overlap: true}
	var prev chan error
	for _, it := range items {
		_, tail, err := th2.ThreadFront(context.Background(), it)
		if err != nil {
			t.Fatal(err)
		}
		if prev != nil {
			if err := <-prev; err != nil {
				t.Fatal(err)
			}
			prev = nil
		}
		if tail != nil {
			ch := make(chan error, 1)
			go func() { _, e := tail(context.Background()); ch <- e }()
			prev = ch
		}
	}
	if prev != nil {
		if err := <-prev; err != nil {
			t.Fatal(err)
		}
	}
	got := state(f2)
	if len(got) != len(want) {
		t.Fatalf("memberships %d, want %d", len(got), len(want))
	}
	for id, c := range want {
		if got[id] != c {
			t.Errorf("statement %d current=%v, want %v", id, got[id], c)
		}
	}
}
