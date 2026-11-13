package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// digestScript answers a digest's two calls and counts them.
type digestScript struct {
	calls  atomic.Int32
	hosted bool
}

func (d *digestScript) Local() bool { return !d.hosted }
func (d *digestScript) ModelName() string {
	if d.hosted {
		return "digest-fake-hosted"
	}
	return "digest-fake"
}
func (d *digestScript) Complete(_ context.Context, _ []provider.Message, opt provider.ChatOptions, _ func(string)) (string, error) {
	d.calls.Add(1)
	if opt.Kind == "digest-summary" {
		return `{"overview":"A meeting.","sections":[{"subject":"First subject","summary":"Something was discussed.","people":[],"lines":[[1,1]],"sensitive":[]}]}`, nil
	}
	return `{"todos":[{"text":"Somebody will do something.","owner":["unclear"],"requester":"self","agreed":"agreed","due_text":"","due_date":"","evidence":[{"line":1,"quote":"x"}]}]}`, nil
}

func meetingsWithDigest(t *testing.T, r *Runner) int {
	t.Helper()
	items, _ := r.Store.AllItems()
	n := 0
	for _, it := range items {
		if st, _ := r.Store.StageStamp("digest", it.ID); st != "" {
			if it.Kind != item.KindMeeting {
				t.Errorf("%s: a digest of a %s", it.ID, it.Kind)
			}
			n++
		}
	}
	return n
}

// Digests: a meeting is read once for its passages, labels and facts; a
// new digest version waits until asked; re-extraction redoes only the
// matching of to-dos with statements, with no model call; a hosted model
// needs knowledge.allow_hosted.
func TestDigestStage(t *testing.T) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.Updates = "statements", 0, "explicit"
	r.Cfg.Knowledge.Digest.Enabled = true
	r.Cfg.Knowledge.NameMeetings = false // names use the digest model too; counted in names_test
	fake := &digestScript{}
	run := func(extractor string, redo ...string) KnowledgeStats {
		return knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{extractor}, DigestLLM: fake, Redo: redo})
	}

	ks := run("m1")
	n := meetingsWithDigest(t, r)
	if n == 0 || ks.Digested != n || int(fake.calls.Load()) != 2*n || ks.DigestFailed != 0 {
		t.Fatalf("first run: %d digested (%d stored), %d calls, %d failed", ks.Digested, n, fake.calls.Load(), ks.DigestFailed)
	}
	items, _ := st.AllItems()
	var id string
	for _, it := range items {
		if s, _ := st.StageStamp("digest", it.ID); s != "" {
			id = it.ID
		}
	}
	d, err := st.MeetingDigest(sensitivity.Owner(), id)
	if err != nil || d == nil || len(d.Sections) != 1 || len(d.Todos) != 1 || d.Todos[0].Owners[0] != "unclear" {
		t.Fatalf("stored digest: %+v %v", d, err)
	}
	link0, _ := st.StageStamp("digest-link", id)
	if link0 == "" {
		t.Error("to-dos weren't matched with statements")
	}

	// Nothing changed: nothing read.
	if ks := run("m1"); ks.Digested != 0 || int(fake.calls.Load()) != 2*n {
		t.Fatalf("second run read %d meetings (%d calls)", ks.Digested, fake.calls.Load())
	}

	// Re-classification with the same labels (other reason, other stamp):
	// not read again. A stamp from the earlier scheme is renewed, not redone.
	ls, _ := st.ItemLabels(id)
	own := ls[-1]
	delete(ls, -1)
	own.Reason += " (reworded)"
	if err := st.ReplaceLabels(id, "other-version", "other-hash", own, ls, nil); err != nil {
		t.Fatal(err)
	}
	if cls, _ := st.StageStamp("classify", id); cls != "other-version/other-hash" {
		t.Fatalf("classify stamp %q", cls)
	}
	before, _ := st.StageStamp("digest", id)
	if ks := run("m1"); ks.Digested != 0 || int(fake.calls.Load()) != 2*n {
		t.Fatalf("same labels, new classification: %d read (%d calls)", ks.Digested, fake.calls.Load())
	}
	if after, _ := st.StageStamp("digest", id); after != before {
		t.Errorf("stamp moved with the classification: %s -> %s", before, after)
	}

	// Re-extraction under another model: the digest is not read again, only
	// matched again.
	run("m2", "extract")
	if int(fake.calls.Load()) != 2*n {
		t.Fatalf("re-extraction made %d digest calls", fake.calls.Load()-int32(2*n))
	}
	if link1, _ := st.StageStamp("digest-link", id); link1 == link0 {
		t.Error("to-dos weren't matched again after re-extraction")
	}

	// A hosted model is refused without allow_hosted, and used with it.
	hosted := &digestScript{hosted: true}
	r.Cfg.Knowledge.AllowHosted = false
	r.Cfg.Knowledge.Digest.Model.Provider = ""
	_, err = r.Knowledge(context.Background(), KnowledgeOptions{LLM: emptyLLM{"m2"}, Embedder: provider.Hash{Dim: 64}, DigestLLM: hosted, Redo: []string{"digest"}})
	if err == nil || !strings.Contains(err.Error(), "allow_hosted") || hosted.calls.Load() != 0 {
		t.Fatalf("hosted without allow_hosted: %v, %d calls", err, hosted.calls.Load())
	}
	r.Cfg.Knowledge.AllowHosted = true
	if ks, err := r.Knowledge(context.Background(), KnowledgeOptions{LLM: emptyLLM{"m2"}, Embedder: provider.Hash{Dim: 64}, DigestLLM: hosted, Redo: []string{"digest"}}); err != nil || ks.Digested != n {
		t.Fatalf("hosted with allow_hosted and --redo digest: %v, %d digested of %d", err, ks.Digested, n)
	}

	// Off: no digest, and the to-do view reads statements.
	r2, st2 := build(t)
	r2.Cfg.Knowledge.Extract, r2.Cfg.Knowledge.Gate = "statements", 0
	r2.Cfg.Knowledge.Digest.Enabled = false
	knowledgeRun(t, r2, KnowledgeOptions{LLM: emptyLLM{"m1"}, DigestLLM: fake})
	if n := meetingsWithDigest(t, r2); n != 0 {
		t.Errorf("%d digests with knowledge.digest off", n)
	}
	_ = st2
}
