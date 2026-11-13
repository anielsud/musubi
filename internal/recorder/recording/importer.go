package recording

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// Imports (control.OpImport, docs/phone.md): a recording made elsewhere,
// a phone's, becomes a session as if the recorder had heard it on the
// mic. Each runs as `musubi-recorder import --here` in a child process,
// one at a time, never while a meeting records: a meeting that starts
// mid-import stops the child, and the import waits and starts over once
// the meeting is saved. A crash in the models costs only the attempt.

// PlatformPhone marks a session imported from the phone.
const PlatformPhone = session.PlatformPhone

// importPoll is how often a running import checks for a meeting, and a
// waiting one for its end.
var importPoll = 2 * time.Second

type importJob struct {
	args  control.ImportArgs
	state string
	err   string
}

type importQueue struct {
	mu      sync.Mutex
	jobs    map[string]*importJob
	pending []string
	wake    chan struct{}
	started bool
	// run imports one job; nil is the child process. Tests replace it.
	run    func(ctx context.Context, args control.ImportArgs) error
	ctx    context.Context
	cancel context.CancelFunc
}

// Import queues args, or reports the import already asked for under the
// same id.
func (a *Recorder) Import(p control.ImportArgs) (control.ImportStatus, error) {
	if err := p.Check(config.DataDir()); err != nil {
		return control.ImportStatus{}, err
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	q := &a.imports
	q.mu.Lock()
	defer q.mu.Unlock()
	if j := q.jobs[p.ID]; j != nil {
		if j.state == control.ImportFailed && p.Retry {
			if _, err := importAudio(p.Path); err != nil {
				return control.ImportStatus{}, err
			}
			j.args, j.state, j.err = p, control.ImportWaiting, ""
			q.enqueueLocked(a, p.ID)
		}
		return control.ImportStatus{Session: p.ID, State: j.state, Error: j.err}, nil
	}
	// Asked again after a restart: a session already saved is done; one
	// under this id that isn't an import is never touched.
	if sess, err := a.store.Load(p.ID); err == nil && sess.ID == p.ID {
		if sess.Platform != PlatformPhone {
			return control.ImportStatus{}, fmt.Errorf("a session with this id already exists")
		}
		return control.ImportStatus{Session: p.ID, State: control.ImportImported}, nil
	}
	if _, err := importAudio(p.Path); err != nil {
		return control.ImportStatus{}, err
	}
	if q.jobs == nil {
		q.jobs = map[string]*importJob{}
	}
	q.jobs[p.ID] = &importJob{args: p, state: control.ImportWaiting}
	q.enqueueLocked(a, p.ID)
	fmt.Printf("import %s: queued\n", p.ID)
	return control.ImportStatus{Session: p.ID, State: control.ImportWaiting}, nil
}

// importAudio is the inbox file's size, refusing anything but a regular
// file (a link could point anywhere).
func importAudio(path string) (int64, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("no such file in the inbox")
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return 0, fmt.Errorf("the inbox file isn't audio")
	}
	return fi.Size(), nil
}

func (q *importQueue) enqueueLocked(a *Recorder, id string) {
	q.pending = append(q.pending, id)
	if !q.started {
		q.started = true
		q.wake = make(chan struct{}, 1)
		q.ctx, q.cancel = context.WithCancel(context.Background())
		go a.importLoop(q.ctx)
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// stopImports ends the import loop and any child it runs (Shutdown).
func (a *Recorder) stopImports() {
	q := &a.imports
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancel != nil {
		q.cancel()
	}
}

func (a *Recorder) importLoop(ctx context.Context) {
	q := &a.imports
	for {
		q.mu.Lock()
		var id string
		if len(q.pending) > 0 {
			id, q.pending = q.pending[0], q.pending[1:]
		}
		q.mu.Unlock()
		if id == "" {
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
			continue
		}
		a.importOne(ctx, id)
		if ctx.Err() != nil {
			return
		}
	}
}

// importOne runs one queued import to the end: saved, failed, or (ctx
// ended) left waiting.
func (a *Recorder) importOne(ctx context.Context, id string) {
	q := &a.imports
	set := func(state, msg string) {
		q.mu.Lock()
		if j := q.jobs[id]; j != nil {
			j.state, j.err = state, msg
		}
		q.mu.Unlock()
	}
	q.mu.Lock()
	j := q.jobs[id]
	var args control.ImportArgs
	if j != nil {
		args = j.args
	}
	q.mu.Unlock()
	if j == nil {
		return
	}
	for {
		if !a.waitNotRecording(ctx) {
			return
		}
		set(control.ImportTranscribing, "")
		fmt.Printf("import %s: transcribing\n", id)
		runCtx, stop := context.WithCancel(ctx)
		interrupted := make(chan bool, 1)
		go func() {
			// A meeting that starts takes the CPU back: the import stops
			// and starts over once the meeting is saved.
			t := time.NewTicker(importPoll)
			defer t.Stop()
			for {
				select {
				case <-runCtx.Done():
					interrupted <- false
					return
				case <-t.C:
					if a.Recording() {
						interrupted <- true
						stop()
						return
					}
				}
			}
		}()
		run := q.run
		if run == nil {
			run = runImportChild
		}
		err := run(runCtx, args)
		stop()
		if <-interrupted && ctx.Err() == nil {
			fmt.Printf("import %s: a meeting started; waiting for it to end\n", id)
			set(control.ImportWaiting, "")
			continue
		}
		if ctx.Err() != nil {
			set(control.ImportWaiting, "")
			return
		}
		if err != nil {
			fmt.Printf("import %s: failed: %v\n", id, err)
			set(control.ImportFailed, "transcription failed")
			return
		}
		break
	}
	sess, err := a.store.Load(id)
	if err != nil || sess.ID != id {
		fmt.Printf("import %s: no session after the import\n", id)
		set(control.ImportFailed, "transcription failed")
		return
	}
	// The audio is in the session now; the inbox copy isn't needed.
	if err := os.Remove(args.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Printf("import %s: inbox file kept: %v\n", id, err)
	}
	a.reindexSession(id, nil)
	set(control.ImportImported, "")
	fmt.Printf("import %s: imported\n", id)
	// Musubi takes in a saved session on this message, as it does a
	// recorded meeting's.
	a.feedSession(livefeed.StateSaved, sess)
}

// runImportChild runs the import as `musubi-recorder import --here` on
// this data folder, its output in this process's log.
func runImportChild(ctx context.Context, p control.ImportArgs) error {
	worker := session.FindDiarizeWorker() // the recorder's own executable
	if worker == "" {
		return fmt.Errorf("musubi-recorder not found to run the import")
	}
	args := []string{"import", "--here", "--id", p.ID, "--started-at", p.StartedAt.Format(time.RFC3339Nano), "--data", config.DataDir()}
	if p.EventID != "" {
		args = append(args, "--event-id", p.EventID)
	}
	args = append(args, "--", p.Path)
	cmd := exec.CommandContext(ctx, worker, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		// Nothing half-made stays behind for "imported" to find.
		if dir, derr := sessionDir(p.ID); derr == nil {
			if _, serr := os.Stat(filepath.Join(dir, "session.json")); serr != nil {
				os.RemoveAll(dir)
			}
		}
		return err
	}
	return nil
}
