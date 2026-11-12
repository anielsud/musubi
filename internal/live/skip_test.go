package live

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/store"
)

// A pick that would read what the last one read is skipped (live.skip_idle),
// on the usual cadence; a changed line within the span runs it again, and
// with skip_idle off every due pick runs.
func TestPickSkipsWhenNothingNew(t *testing.T) {
	for _, skip := range []bool{true, false} {
		st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.DefaultConfig()
		cfg.Live.Pick.Enabled, cfg.Live.SkipIdle = true, skip
		eng := &engine.Engine{Cfg: cfg, Store: st, Embedder: &perfEmbedder{model: "m", pool: [][]float32{{1, 0}}}}
		skipped := 0
		l := New(eng, func(_ string, d any) {
			if tr, ok := d.(Trace); ok && strings.HasPrefix(tr.Title, "pick skipped") {
				skipped++
			}
		}, nil)
		if err := l.Reload(); err != nil {
			t.Fatal(err)
		}
		l.rel = &perfClassifier{}
		l.stepped.Store(true)
		l.setStepClock(0)
		l.handle(Event{V: 1, Type: "session", State: "started", Session: &Session{ID: "s"}}, "live")
		say := func(at float64, id, text string) {
			l.setStepClock(at)
			l.handle(Event{V: 1, Type: "segment", SessionID: "s", Segment: &Segment{ID: id, Speaker: "Person 2", Text: text, Start: at - 1, End: at}}, "live")
		}
		say(1, "a", "where does the budget stand")
		say(25, "a", "where does the budget stand") // the same line again: nothing new
		say(50, "b", "and the hiring plan")
		want := map[bool]int{true: 1, false: 0}[skip]
		if skipped != want {
			t.Errorf("skip_idle %v: %d picks skipped, want %d", skip, skipped, want)
		}
		st.Close()
	}
}
