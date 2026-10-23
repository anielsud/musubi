package gui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/provider"
)

// The Status page shows the knowledge layer's waiting work (pipeline.Queue)
// and starts the runs an automatic update leaves to someone: catching up
// on a big batch of new meetings, and rethreading everything in time order
// to take in a backlog.

// QueueView is the Status page's work queue.
type QueueView struct {
	pipeline.Queue
	Running string  `json:"running"` // "", "catchup" or "rethread"
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
	q, err := a.eng.Queue()
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
// everything in time order. Hosted calls stop at the estimate and a half.
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
	case "rethread":
		o.Redo = []string{"threads"}
		estimate = q.RethreadUSD
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
	ledger := a.ledgerPath()
	limit := provider.LedgerTotal(ledger) + 1.5*estimate + 0.05
	go func() {
		runMu.Lock()
		prev := os.Getenv("MUSUBI_SPEND_CAP")
		os.Setenv("MUSUBI_SPEND_CAP", fmt.Sprintf("%.4f", limit))
		ks, err := a.eng.Knowledge(a.ctx, io.Discard, o)
		if prev == "" {
			os.Unsetenv("MUSUBI_SPEND_CAP")
		} else {
			os.Setenv("MUSUBI_SPEND_CAP", prev)
		}
		runMu.Unlock()
		a.mu.Lock()
		a.running = ""
		a.mu.Unlock()
		msg := fmt.Sprintf("%d extracted, %d threaded", ks.Items, ks.Threads.Items)
		if err != nil {
			msg = err.Error()
		}
		a.live.Reload()
		a.emit("update:done", msg)
		a.emit("app:status", a.Status())
		a.emit("queue:changed", nil)
	}()
	a.emit("queue:changed", nil)
	return nil
}
