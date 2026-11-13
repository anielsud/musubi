package gui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/updates"
)

// newVersion is the newest release the last check found, or nil.
var (
	newVersionMu sync.Mutex
	newVersion   *updates.Release
)

// checkUpdates asks GitHub for a newer release ([updates]) a minute
// after the dataset opens and then daily, until ctx (the dataset's)
// ends. A failed check is logged and tried again next time.
func (a *App) checkUpdates(ctx context.Context, cfgRepo string, prereleases bool) {
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
		r, err := updates.Check(ctx, client, cfgRepo, Version, prereleases)
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
