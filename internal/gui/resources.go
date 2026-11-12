package gui

import (
	"os"
	"sync"

	"github.com/anielsud/musubi/internal/procstat"
)

// The diagnostics panel's resource readout ([diagnostics] resources): CPU
// and memory of Musubi, its web view, the recorder, Ollama and Tomoe
// (internal/procstat). The panel watches while it is open and renews the
// watch; nothing is sampled otherwise. Each sample goes out as
// "diag:resources".

type resourceState struct {
	mu      sync.Mutex
	sampler *procstat.Sampler
	// newSource replaces the real process table in tests.
	newSource func() (procstat.Source, error)
}

// ResourcesOn says whether the panel offers the resources view.
func (a *App) ResourcesOn() bool {
	return a.eng != nil && a.eng.Cfg.Diagnostics.Resources
}

// WatchResources starts sampling, or renews the watch (the panel calls it
// every few seconds while open), and returns the history so far.
func (a *App) WatchResources() procstat.Snapshot {
	if !a.ResourcesOn() {
		a.UnwatchResources()
		return procstat.Snapshot{Reason: "the resource readout is off ([diagnostics] resources)"}
	}
	a.res.mu.Lock()
	if a.res.sampler == nil {
		newSource := a.res.newSource
		if newSource == nil {
			newSource = procstat.System
		}
		src, err := newSource()
		if err != nil {
			a.res.mu.Unlock()
			return procstat.Snapshot{Reason: err.Error(), Machine: procstat.MachineInfo()}
		}
		a.res.sampler = procstat.NewSampler(src, procstat.MachineInfo(), os.Getpid(), a.recorderPID,
			func(s procstat.Sample) { a.emit("diag:resources", s) })
	}
	s := a.res.sampler
	a.res.mu.Unlock()
	return s.Watch()
}

// UnwatchResources stops sampling (the panel closed); the history stays.
func (a *App) UnwatchResources() {
	a.res.mu.Lock()
	s := a.res.sampler
	a.res.mu.Unlock()
	if s != nil {
		s.Unwatch()
	}
}

// recorderPID is the pid Musubi's recorder reported on its feed, 0 when
// not connected.
func (a *App) recorderPID() int {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	if !a.rec.connected || a.rec.status == nil {
		return 0
	}
	return a.rec.status.PID
}
