package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// The watchdog: a recorder that isn't running loses meetings without
// anyone noticing (2026-11-12: an install left it stopped twice). launchd
// restarts it whenever it exits; this covers the rest (unloaded, or
// failing to start) while Musubi is open.
const (
	watchEvery   = 10 * time.Second
	watchGrace   = 30 * time.Second // an install or restart takes a few seconds
	watchRetry   = 2 * time.Minute
	watchWarnAge = 90 * time.Second
)

// RecorderWatch is what the app shows while the recorder is down.
type RecorderWatch struct {
	Down    bool      `json:"down"`
	Since   time.Time `json:"since,omitzero"`
	Tried   time.Time `json:"tried,omitzero"`
	Problem string    `json:"problem,omitempty"`
}

// launchctl runs launchctl with fixed arguments; a variable so tests
// don't. notify shows a macOS notification; the same.
var (
	launchctl = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	}
	notify = func(title, text string) {
		// Fixed text only: nothing from a meeting or the network goes in.
		script := fmt.Sprintf("display notification %q with title %q", text, title)
		_ = exec.Command("osascript", "-e", script).Run()
	}
)

func recorderDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func recorderPlist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", recorderAgent+".plist")
}

// startRecorderAgent gets the recorder running: loads its LaunchAgent if
// it isn't loaded, else asks launchd to start it (kill first with
// restart, which RestartRecorder uses to apply settings). With no
// LaunchAgent, install (when not nil) writes it first.
func startRecorderAgent(ctx context.Context, restart bool, install func() error) error {
	service := recorderDomain() + "/" + recorderAgent
	if _, err := launchctl(ctx, "print", service); err != nil {
		if _, err := os.Stat(recorderPlist()); err != nil {
			if install == nil {
				return fmt.Errorf("the recorder isn't installed (no %s)", recorderPlist())
			}
			if err := install(); err != nil {
				return err
			}
		}
		if out, err := launchctl(ctx, "bootstrap", recorderDomain(), recorderPlist()); err != nil {
			return fmt.Errorf("starting the recorder: %v: %s", err, trimOut(out))
		}
		return nil
	}
	args := []string{"kickstart"}
	if restart {
		args = append(args, "-k")
	}
	if out, err := launchctl(ctx, append(args, service)...); err != nil {
		return fmt.Errorf("starting the recorder: %v: %s", err, trimOut(out))
	}
	return nil
}

func trimOut(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// recorderUp asks the recorder for its status.
func (a *App) recorderUp() bool {
	return a.recCall(control.OpStatus, nil, nil) == nil
}

// watchRecorder checks the recorder until ctx ends.
func (a *App) watchRecorder(ctx context.Context) {
	if a.eng == nil || !a.eng.Cfg.Recorder.Watchdog {
		return
	}
	// A new install from the DMG: start the bundled recorder now, not
	// after the grace a restart gets. Errors show at the first check.
	if _, err := os.Stat(recorderPlist()); err != nil {
		if exe, _ := bundledRecorder(); exe != "" && a.recorderInstall() != nil {
			_ = startRecorderAgent(ctx, false, a.recorderInstall())
		}
	}
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	var w RecorderWatch
	warned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		w, warned = a.watchStep(ctx, w, warned, time.Now())
	}
}

// watchStep is one check: up clears the watch; down long enough starts
// the recorder (at most every watchRetry) and warns once per outage.
func (a *App) watchStep(ctx context.Context, w RecorderWatch, warned bool, now time.Time) (RecorderWatch, bool) {
	if a.recorderUp() {
		if w.Down {
			a.emit("recorder:watch", RecorderWatch{})
		}
		return RecorderWatch{}, false
	}
	if !w.Down {
		w = RecorderWatch{Down: true, Since: now}
	}
	if now.Sub(w.Since) < watchGrace {
		return w, warned
	}
	if w.Tried.IsZero() || now.Sub(w.Tried) >= watchRetry {
		w.Tried, w.Problem = now, ""
		if err := startRecorderAgent(ctx, false, a.recorderInstall()); err != nil {
			w.Problem = err.Error()
		}
	}
	a.emit("recorder:watch", w)
	if !warned && now.Sub(w.Since) >= watchWarnAge {
		notify("Musubi recorder isn't running", "Meetings aren't being recorded. Musubi is trying to start it: open Musubi to check.")
		warned = true
	}
	return w, warned
}

// recorderInstall installs the bundled recorder's LaunchAgent, or is nil
// when [recorder] install_agent is off.
func (a *App) recorderInstall() func() error {
	if a.eng == nil || !a.eng.Cfg.Recorder.InstallAgent {
		return nil
	}
	data := a.eng.Cfg.Recorder.Data
	return func() error { return installRecorderAgent(data) }
}
