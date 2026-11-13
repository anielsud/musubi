package live

import (
	"context"
	"runtime/pprof"

	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// reads is the meeting's read cache (live.cache): started with each
// meeting and after each Reload, dropped by the store whenever the
// database changes, and keyed by scope inside, so a toggle to the shared
// screen never sees what was read in private. nil with live.cache off.
func (l *Live) reads() *store.ReadCache {
	if !l.eng.Cfg.Live.Cache {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rc == nil {
		l.rc = l.eng.Store.NewReadCache()
	}
	return l.rc
}

func (l *Live) threadStatuses(sc sensitivity.Scope) (map[int64]store.ThreadStatusRow, error) {
	if rc := l.reads(); rc != nil {
		return rc.ThreadStatuses(sc)
	}
	return l.eng.Store.ThreadStatuses(sc)
}

func (l *Live) entityGraph(sc sensitivity.Scope, id string, exclude []string, limit int) (*store.Graph, error) {
	if rc := l.reads(); rc != nil {
		return rc.EntityGraph(sc, id, exclude, limit)
	}
	return l.eng.Store.EntityGraph(sc, id, exclude, limit)
}

func (l *Live) statementsNear(sc sensitivity.Scope, q []float32, model string, claims int, exclude []string) ([]store.StatementHit, error) {
	if rc := l.reads(); rc != nil {
		return rc.StatementsNear(sc, q, model, claims, exclude)
	}
	return l.eng.Store.StatementsNear(sc, q, model, claims, exclude)
}

// labelled runs f with the pprof label step=name, so a CPU profile
// (Settings' diagnostics, "Record a 30 s profile") attributes its time,
// SQLite's included, to the Live step. Goroutines f starts inherit it; a
// labelled step called inside another (cards inside a pick) clears the
// outer label when it returns, which costs only the rest of that call.
func labelled(name string, f func()) {
	pprof.Do(context.Background(), pprof.Labels("step", name), func(context.Context) { f() })
}
