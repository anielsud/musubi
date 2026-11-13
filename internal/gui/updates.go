package gui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/anielsud/musubi/internal/updates"
)

// Channel is "release" only in the DMG the release workflow builds (set
// by cmd/musubi-gui). Every other build (make install-gui-mac, a dev
// build) is "local": it never checks for or installs a release, so a
// build from source, which runs ahead of the releases, is never replaced
// by an older one.
var Channel = "local"

// newVersion is the newest release the last check found, or nil.
var (
	newVersionMu sync.Mutex
	newVersion   *updates.Release
	installing   bool
)

// UpdatesOn says whether this build checks for releases at all.
func updatesOn(check bool) bool { return check && Channel == "release" }

// checkUpdates asks GitHub for a newer release ([updates]) a minute
// after the dataset opens and then daily, until ctx (the dataset's)
// ends. A failed check is logged and tried again next time.
func (a *App) checkUpdates(ctx context.Context, repo string, prereleases bool) {
	if app, err := updates.AppPath(); err == nil {
		updates.RemovePrevious(app)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(24 * time.Hour)
		r, err := updates.Check(ctx, client, repo, Version, prereleases)
		if err != nil {
			log.Printf("update check: %v", err)
			continue
		}
		newVersionMu.Lock()
		changed := (r == nil) != (newVersion == nil) || (r != nil && r.Version != newVersion.Version)
		newVersion = r
		newVersionMu.Unlock()
		if changed {
			if r != nil {
				log.Printf("update check: %s is available (running %s)", r.Version, Version)
			}
			a.emit("app:status", a.Status())
		}
	}
}

func currentNewVersion() *updates.Release {
	newVersionMu.Lock()
	defer newVersionMu.Unlock()
	return newVersion
}

// OpenNewVersion opens the newer release's page in the browser. Only the
// link the check found (a page of the releases repository) is ever
// opened.
func (a *App) OpenNewVersion() error {
	r := currentNewVersion()
	if r == nil {
		return fmt.Errorf("no newer version found")
	}
	return exec.Command("open", r.URL).Run()
}

// CanInstallUpdate says whether "Update" can replace the app in place
// ([updates] install, a release build, a release with an installer, and
// an app that isn't running from the disk image); otherwise the header
// offers the release page.
func (a *App) CanInstallUpdate() bool {
	r := currentNewVersion()
	if r == nil || r.DMG == "" || r.SHA256 == "" || a.eng == nil || !a.eng.Cfg.Updates.Install || Channel != "release" {
		return false
	}
	_, err := updates.AppPath()
	return err == nil
}

// InstallUpdate replaces Musubi with the newer release and restarts it,
// in the background: "selfupdate:step" reports progress and
// "selfupdate:failed" an error, after which the current version runs on
// unchanged. Refused while a meeting is recorded or Musubi is updating.
func (a *App) InstallUpdate() error {
	r := currentNewVersion()
	if !a.CanInstallUpdate() {
		return fmt.Errorf("this copy of Musubi can't update itself; download it from the release page")
	}
	app, _ := updates.AppPath()
	a.rec.mu.Lock()
	recording := a.rec.status != nil && a.rec.status.Recording
	a.rec.mu.Unlock()
	if recording {
		return fmt.Errorf("a meeting is being recorded; update once it is over")
	}
	a.mu.Lock()
	if a.updating || a.running != "" {
		a.mu.Unlock()
		return fmt.Errorf("Musubi is working; update once it is done")
	}
	a.mu.Unlock()
	newVersionMu.Lock()
	if installing {
		newVersionMu.Unlock()
		return fmt.Errorf("already updating")
	}
	installing = true
	newVersionMu.Unlock()

	go func() {
		defer func() {
			newVersionMu.Lock()
			installing = false
			newVersionMu.Unlock()
		}()
		log.Printf("updating from %s to %s", Version, r.Version)
		client := &http.Client{Timeout: 15 * time.Minute}
		err := updates.Install(a.ctx, client, r, app, func(s string) { a.emit("selfupdate:step", s) })
		if err != nil {
			log.Printf("update to %s: %v", r.Version, err)
			a.emit("selfupdate:failed", err.Error())
			return
		}
		log.Printf("updated to %s; restarting", r.Version)
		// The recorder's agent runs the copy inside the app: restart it
		// from the new one.
		if err := a.RestartRecorder(); err != nil {
			log.Printf("update: restarting the recorder: %v", err)
		}
		if err := updates.Relaunch(app, os.Getpid()); err != nil {
			a.emit("selfupdate:failed", fmt.Sprintf("updated to %s; quit and open Musubi to start it (%v)", r.Version, err))
			return
		}
		a.emit("selfupdate:step", "Restarting")
		wruntime.Quit(a.ctx)
	}()
	return nil
}
