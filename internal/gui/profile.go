package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"time"
)

// Profiling ([diagnostics] profiling): the diagnostics panel records a CPU
// profile of the app for profileFor, with a goroutine dump, into the
// dataset's data folder under profiles/. Live's steps carry pprof labels
// (step=pick, suggest, fastlane, cards; queue for the work queue), so time
// in SQLite shows under the Go caller and the step. Read a profile with
// `go tool pprof -http : <file>`.

const profileFor = 30 * time.Second

var profileMu sync.Mutex

// ProfilingOn says whether the diagnostics panel offers a profile.
func (a *App) ProfilingOn() bool {
	return a.eng != nil && a.eng.Cfg.Diagnostics.Profiling
}

// RecordProfile records a CPU profile for 30 s, then a goroutine dump, and
// returns the profile's path. One at a time.
func (a *App) RecordProfile() (string, error) {
	if !a.ProfilingOn() {
		return "", fmt.Errorf("profiling is off ([diagnostics] profiling)")
	}
	if !profileMu.TryLock() {
		return "", fmt.Errorf("a profile is being recorded already")
	}
	defer profileMu.Unlock()
	dir := filepath.Join(a.eng.Cfg.DataDir, "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	cpuPath := filepath.Join(dir, "cpu-"+stamp+".pprof")
	f, err := os.OpenFile(cpuPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		os.Remove(cpuPath)
		return "", err
	}
	var done <-chan struct{}
	if a.ctx != nil {
		done = a.ctx.Done()
	}
	select {
	case <-time.After(profileFor):
	case <-done:
	}
	pprof.StopCPUProfile()
	if err := f.Close(); err != nil {
		return "", err
	}
	g, err := os.OpenFile(filepath.Join(dir, "goroutines-"+stamp+".txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return cpuPath, err
	}
	defer g.Close()
	// debug=1: goroutines grouped by stack, with their labels.
	if err := pprof.Lookup("goroutine").WriteTo(g, 1); err != nil {
		return cpuPath, err
	}
	return cpuPath, nil
}
