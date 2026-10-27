package live

import (
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
)

// Cards pushed off the panel by newer ones are kept (live.keep_topics),
// most recent first, and come back when raised again; without the key
// they are dropped.
func TestKeepTopics(t *testing.T) {
	ids := func(ts []shownTopic) []int64 {
		var out []int64
		for _, t := range ts {
			out = append(out, t.id.Topic)
		}
		return out
	}
	same := func(a, b []int64) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	run := func(keep int, raise ...int64) *Live {
		cfg := config.DefaultConfig()
		cfg.Live.Focus, cfg.Live.KeepTopics = true, keep
		l := &Live{eng: &engine.Engine{Cfg: cfg}, emit: func(string, any) {}}
		l.stepped.Store(true)
		for i, id := range raise {
			l.setStepClock(float64(i))
			l.showTopic(0, knowledge.Anchor{Topic: id}, "says it", -1, "")
		}
		return l
	}

	l := run(0, 1, 2, 3, 4, 5, 6)
	if !same(ids(l.topics), []int64{6, 5, 4, 3}) || len(l.dropped) != 0 {
		t.Fatalf("keep 0: topics %v dropped %v", ids(l.topics), ids(l.dropped))
	}
	l = run(3, 1, 2, 3, 4, 5, 6, 7, 8)
	if !same(ids(l.topics), []int64{8, 7, 6, 5}) || !same(ids(l.dropped), []int64{4, 3, 2}) {
		t.Fatalf("keep 3: topics %v dropped %v", ids(l.topics), ids(l.dropped))
	}
	l = run(10, 1, 2, 3, 4, 5, 6, 2)
	if !same(ids(l.topics), []int64{2, 6, 5, 4}) || !same(ids(l.dropped), []int64{3, 1}) {
		t.Fatalf("raised again: topics %v dropped %v", ids(l.topics), ids(l.dropped))
	}
}
