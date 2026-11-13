package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// The Status page shows the knowledge layer's waiting work (pipeline.Queue)
// and starts the runs an automatic update leaves to someone: catching up
// on a big batch of new meetings, and rethreading everything in time order
// to take in a backlog.

// QueueView is the Status page's work queue.
type QueueView struct {
	pipeline.Queue
	Running string  `json:"running"` // "", "catchup", "rethread" or "upgrades"
	Spent   float64 `json:"spent"`   // dollars in the spend ledger
	Ledger  string  `json:"ledger"`
}

var runMu sync.Mutex

func (a *App) ledgerPath() string {
	if p := os.Getenv("MUSUBI_SPEND_LEDGER"); p != "" {
		return p
	}
	// Hosted calls are metered even when nobody set a ledger, so a run's
	// spend cap can work.
	p := filepath.Join(a.eng.Cfg.DataDir, "spend.jsonl")
	os.Setenv("MUSUBI_SPEND_LEDGER", p)
	return p
}

// Queue is the work waiting in the knowledge layer.
func (a *App) Queue() (*QueueView, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	q, err := a.cachedQueue()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	path := a.ledgerPath()
	return &QueueView{Queue: q, Running: running, Spent: provider.LedgerTotal(path), Ledger: path}, nil
}

// RunKnowledge starts a run an automatic update doesn't: "catchup" takes
// every new item, "rethread" also takes the backlog by rethreading
// everything in time order, "upgrades" redoes what a new version left
// waiting (pipeline.Upgrades). Hosted calls stop at the estimate and a half.
func (a *App) RunKnowledge(kind string) error {
	if a.eng == nil {
		return fmt.Errorf("not ready: %s", a.err)
	}
	q, err := a.eng.Queue()
	if err != nil {
		return err
	}
	o := pipeline.KnowledgeOptions{}
	estimate := q.CatchUpUSD
	switch kind {
	case "catchup":
		o.NewOnly = true
	case "rethread":
		o.Redo = []string{"threads"}
		estimate = q.RethreadUSD
	case "upgrades":
		// What a new version would redo on existing data, held for the
		// owner (knowledge.hold_upgrades).
		if o.Redo = q.Upgrades.Redo(); len(o.Redo) == 0 {
			return fmt.Errorf("no upgrade is waiting")
		}
		estimate = q.Upgrades.USD
	default:
		return fmt.Errorf("no such run %q", kind)
	}
	a.mu.Lock()
	if a.running != "" || a.updating {
		a.mu.Unlock()
		return fmt.Errorf("a run is going already")
	}
	a.running = kind
	a.mu.Unlock()
	if err := a.backupBefore(map[string]string{"catchup": "Catch up", "rethread": "Rethread"}[kind]); err != nil {
		a.mu.Lock()
		a.running = ""
		a.mu.Unlock()
		return err
	}
	ledger := a.ledgerPath()
	limit := provider.LedgerTotal(ledger) + 1.5*estimate + 0.05
	go func() {
		runMu.Lock()
		prev := os.Getenv("MUSUBI_SPEND_CAP")
		os.Setenv("MUSUBI_SPEND_CAP", fmt.Sprintf("%.4f", limit))
		// A rethread rewrites every thread: the owner's word must come
		// through it whole (store.KeptGuard).
		var guard *store.KeptGuard
		var gerr error
		if len(o.Redo) > 0 && a.eng.Cfg.Store.CheckCounts {
			guard, gerr = a.eng.Store.GuardKept()
		}
		var ks pipeline.KnowledgeStats
		var err error
		if gerr == nil {
			ks, err = a.eng.Knowledge(a.ctx, io.Discard, o)
		}
		counts := ""
		if guard != nil {
			var after store.Counts
			after, gerr = guard.Finish()
			counts = fmt.Sprintf("; before: %s; after: %s", guard.Before, after)
		}
		err = errors.Join(gerr, err)
		if prev == "" {
			os.Unsetenv("MUSUBI_SPEND_CAP")
		} else {
			os.Setenv("MUSUBI_SPEND_CAP", prev)
		}
		runMu.Unlock()
		a.mu.Lock()
		a.running = ""
		a.mu.Unlock()
		msg := fmt.Sprintf("%d extracted, %d threaded%s", ks.Items, ks.Threads.Items, counts)
		if err != nil {
			msg = err.Error() + counts
		}
		a.live.Reload()
		a.emit("update:done", msg)
		a.emit("app:status", a.Status())
		a.emit("queue:changed", nil)
	}()
	a.emit("queue:changed", nil)
	return nil
}

// queueCache keeps the last work queue: working it out reads every item
// and compares transcripts (seconds on a large dataset), and the database
// has one connection, so recomputing it on every Status refresh held up
// every other screen. It is worked out again when the store's WorkStamp
// or the knowledge settings change, or after queueMaxAge in any case.
type queueCache struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	queue pipeline.Queue
}

const queueMaxAge = 10 * time.Minute

func (a *App) cachedQueue() (pipeline.Queue, error) {
	stamp, err := a.eng.Store.WorkStamp()
	if err != nil {
		return a.eng.Queue()
	}
	key := fmt.Sprintf("%s|%+v", stamp, a.eng.Cfg.Knowledge)
	a.qcache.mu.Lock()
	defer a.qcache.mu.Unlock()
	if a.qcache.key == key && time.Since(a.qcache.at) < queueMaxAge {
		return a.qcache.queue, nil
	}
	var q pipeline.Queue
	pprof.Do(context.Background(), pprof.Labels("step", "queue"), func(context.Context) { q, err = a.eng.Queue() })
	if err != nil {
		return q, err
	}
	a.qcache.key, a.qcache.at, a.qcache.queue = key, time.Now(), q
	return q, nil
}
