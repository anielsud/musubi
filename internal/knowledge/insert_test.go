package knowledge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/provider"
)

// insertLLM threads like scriptedThreads; asked to relate, it says the
// later count answers the earlier question.
type insertLLM struct {
	scriptedThreads
	relates int
}

func (s *insertLLM) Complete(ctx context.Context, msgs []provider.Message, opt provider.ChatOptions, tok func(string)) (string, error) {
	if msgs[0].Content != relateSystem {
		return s.scriptedThreads.Complete(ctx, msgs, opt, tok)
	}
	s.relates++
	var q, a int64
	for _, m := range reItem.FindAllStringSubmatch(msgs[len(msgs)-1].Content, -1) {
		var id int64
		fmt.Sscan(m[1], &id)
		switch {
		case strings.Contains(m[2], "How many of the 40"):
			q = id
		case strings.Contains(m[2], "25 names received"):
			a = id
		}
	}
	if q == 0 || a == 0 {
		return `{"relations":[]}`, nil
	}
	return fmt.Sprintf(`{"relations":[{"later":%d,"earlier":%d,"relation":"answers"}]}`, a, q), nil
}

// The 30 Sept meeting is threaded first; the 16 Sept one is inserted
// after. Its open question, answered by a later count, is not current, and
// nothing was replayed.
func TestInsertOlderMeeting(t *testing.T) {
	f := newCardFixture(t)
	f.st.ClearThreads()
	llm := &insertLLM{}
	// The scripted model knows the assign prompt without the t4 additions.
	th := &Threader{Store: f.st, LLM: llm, Chunk: 70, Parallel: 2, Version: "v-test"}
	if _, err := th.Thread(context.Background(), ThreadItem{ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	st, err := th.Insert(context.Background(), ThreadItem{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if llm.relates == 0 || st.Relations == 0 {
		t.Fatalf("relates %d, relations %d", llm.relates, st.Relations)
	}
	sts, _ := f.st.AllStatements()
	cur := map[int64]bool{}
	rows, _ := f.st.Threads()
	for _, r := range rows {
		for _, id := range r.Current {
			cur[id] = true
		}
	}
	for _, s := range sts {
		switch {
		case strings.Contains(s.Text, "How many of the 40"):
			if cur[s.ID] {
				t.Errorf("answered question still current")
			}
		case strings.Contains(s.Text, "25 names received"), strings.Contains(s.Text, "Batch Juniper changes"):
			if !cur[s.ID] {
				t.Errorf("%q not current", s.Text)
			}
		}
	}
	// What was stored is what the graph says.
	c, err := CheckDerived(f.st, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Agree != c.Memberships {
		t.Errorf("derived and stored disagree: %+v", c)
	}
}

// Taking a meeting out of the graph removes its memberships and the
// relations it took part in; inserting it again places it.
func TestRemoveAndInsert(t *testing.T) {
	f := newCardFixture(t)
	f.st.ClearThreads()
	llm := &insertLLM{}
	th := &Threader{Store: f.st, LLM: llm, Chunk: 70, Parallel: 2, Version: "v-test"}
	for _, it := range []ThreadItem{{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}, {ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)}} {
		if _, err := th.Thread(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	sts, _ := f.st.LinkedItemStatements("m:1")
	affected := map[int64]bool{}
	rows, _ := f.st.ThreadItemRows()
	for _, r := range rows {
		affected[r.Topic] = true
	}
	if _, err := f.st.RemoveFromThreads(sts); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.st.ThreadItemRows()
	for _, r := range rows {
		for _, s := range sts {
			if r.Statement == s.ID {
				t.Fatalf("statement %d still in a thread", s.ID)
			}
		}
	}
	if err := th.Rederive(ThreadItem{ID: "m:1"}, affected); err != nil {
		t.Fatal(err)
	}
	if _, err := th.Insert(context.Background(), ThreadItem{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.st.ThreadItemRows()
	back := 0
	for _, r := range rows {
		for _, s := range sts {
			if r.Statement == s.ID {
				back++
			}
		}
	}
	if back == 0 {
		t.Error("nothing placed again")
	}
}

// Forgetting the meeting that answered a question makes the question
// current again, with nothing replayed.
func TestForgetItemMendsThreads(t *testing.T) {
	f := newCardFixture(t)
	f.st.ClearThreads()
	llm := &insertLLM{}
	th := &Threader{Store: f.st, LLM: llm, Chunk: 70, Parallel: 2, Version: "v-test"}
	if _, err := th.Thread(context.Background(), ThreadItem{ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if _, err := th.Insert(context.Background(), ThreadItem{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	question := func() bool {
		rows, _ := f.st.Threads()
		sts, _ := f.st.AllStatements()
		for _, s := range sts {
			if strings.Contains(s.Text, "How many of the 40") {
				for _, r := range rows {
					for _, id := range r.Current {
						if id == s.ID {
							return true
						}
					}
				}
			}
		}
		return false
	}
	if question() {
		t.Fatal("precondition: the question is answered")
	}
	if _, _, err := ForgetItem(f.st, "m:2"); err != nil {
		t.Fatal(err)
	}
	if !question() {
		t.Error("the question is still retired after its answer was forgotten")
	}
	c, err := CheckDerived(f.st, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Agree != c.Memberships {
		t.Errorf("derived and stored disagree: %+v", c)
	}
}

// A statement corrected so it no longer answers a question: the question
// is current again, and the correction is on record.
func TestCorrectStatement(t *testing.T) {
	f := newCardFixture(t)
	f.st.ClearThreads()
	llm := &insertLLM{}
	th := &Threader{Store: f.st, LLM: llm, Chunk: 70, Parallel: 2, Version: "v-test"}
	if _, err := th.Thread(context.Background(), ThreadItem{ID: "m:2", Start: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if _, err := th.Insert(context.Background(), ThreadItem{ID: "m:1", Start: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	sts, _ := f.st.AllStatements()
	var answer, question int64
	for _, s := range sts {
		switch {
		case strings.Contains(s.Text, "25 names received"):
			answer = s.ID
		case strings.Contains(s.Text, "How many of the 40"):
			question = s.ID
		}
	}
	current := func(id int64) bool {
		rows, _ := f.st.Threads()
		for _, r := range rows {
			for _, c := range r.Current {
				if c == id {
					return true
				}
			}
		}
		return false
	}
	if current(question) {
		t.Fatal("precondition: answered")
	}
	embed := func(string) ([]float32, string, error) { return []float32{1, 0, 0}, "test", nil }
	if _, err := th.Correct(context.Background(), answer, "Juniper: the names list is delayed to next month.", []string{"Dana Ruiz"}, "owner", embed); err != nil {
		t.Fatal(err)
	}
	if !current(question) {
		t.Error("question still retired by a statement that no longer answers it")
	}
	got, _ := f.st.StatementsByID([]int64{answer})
	if !strings.Contains(got[answer].Text, "delayed") || got[answer].Holder != "Dana Ruiz" {
		t.Errorf("not corrected: %+v", got[answer])
	}
	if c, _ := CheckDerived(f.st, time.Time{}); c.Agree != c.Memberships {
		t.Errorf("derived and stored disagree: %+v", c)
	}
}
