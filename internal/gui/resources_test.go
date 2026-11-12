package gui

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/procstat"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// oneProc is a process table holding only this test process.
type oneProc struct{}

func (oneProc) Procs() ([]procstat.Proc, error) {
	return []procstat.Proc{{PID: os.Getpid(), PPID: 1, Path: "/x/musubi-gui"}}, nil
}
func (oneProc) Args(int) ([]string, error)        { return nil, errors.New("none") }
func (oneProc) Usage(int) (procstat.Usage, error) { return procstat.Usage{RSS: 1 << 20}, nil }
func (oneProc) Responsible(pid int) (int, bool)   { return pid, true }

func resApp(on bool) *App {
	cfg := config.DefaultConfig()
	cfg.Diagnostics.Resources = on
	a := &App{ctx: context.Background(), eng: &engine.Engine{Cfg: cfg}, emitFn: func(string, any) {}}
	a.res.newSource = func() (procstat.Source, error) { return oneProc{}, nil }
	return a
}

func TestResourcesOffNeverSamples(t *testing.T) {
	a := resApp(false)
	if a.ResourcesOn() {
		t.Fatal("on")
	}
	snap := a.WatchResources()
	if snap.Supported || snap.Reason == "" || a.res.sampler != nil {
		t.Fatalf("sampled while off: %+v", snap)
	}
}

func TestResourcesWatchAndUnwatch(t *testing.T) {
	a := resApp(true)
	if !config.DefaultConfig().Diagnostics.Resources {
		t.Fatal("resources should default on")
	}
	snap := a.WatchResources()
	if !snap.Supported || snap.IntervalMs != 2000 {
		t.Fatalf("snapshot %+v", snap)
	}
	if !a.res.sampler.Running() {
		t.Fatal("not sampling while watched")
	}
	a.UnwatchResources()
	if a.res.sampler.Running() {
		t.Fatal("still sampling after the panel closed")
	}
}

func TestResourcesUnsupportedSystem(t *testing.T) {
	a := resApp(true)
	a.res.newSource = func() (procstat.Source, error) { return nil, procstat.ErrUnsupported }
	if snap := a.WatchResources(); snap.Supported || snap.Reason != procstat.ErrUnsupported.Error() {
		t.Fatalf("snapshot %+v", snap)
	}
}

func TestRecorderPIDFromFeed(t *testing.T) {
	a := resApp(true)
	if a.recorderPID() != 0 {
		t.Fatal("pid without a recorder")
	}
	a.rec.status = &control.Status{PID: 4242}
	if a.recorderPID() != 0 {
		t.Fatal("pid while disconnected")
	}
	a.rec.connected = true
	if got := a.recorderPID(); got != 4242 {
		t.Fatalf("pid %d", got)
	}
}
