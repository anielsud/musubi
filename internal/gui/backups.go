package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/anielsud/musubi/internal/backup"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// Version is the app's version, for the backups' manifests (set by
// cmd/musubi-gui).
var Version = "dev"

func (a *App) backupSource() backup.Source {
	return backup.SourceFor(a.eng.Cfg, a.cfgPath, a.eng.Store, Version)
}

// backupBefore backs the dataset up before a step that deletes or
// rewrites stored data in bulk ([backup] before_changes). The step must
// not run if it fails.
func (a *App) backupBefore(reason string) error {
	dir, err := backup.BeforeChange(backupsDir(), a.backupSource(), a.eng.Cfg.Backup, reason, time.Now())
	if err != nil {
		return err
	}
	if dir != "" {
		log.Printf("backed up before %s: %s", reason, dir)
		a.emit("storage:changed", nil)
	}
	return nil
}

// dailyBackups makes the open dataset's daily backup ([backup] daily),
// checking hourly until ctx (the dataset's) ends; not while an update or
// run is going or a meeting is being recorded (it waits for the next
// check).
func (a *App) dailyBackups(ctx context.Context, eng *engine.Engine, cfgPath string) {
	t := time.NewTimer(time.Minute) // let the app settle after opening
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(time.Hour)
		if !backup.DailyDue(backupsDir(), datasetName(cfgPath), eng.Cfg.Backup, time.Now()) {
			continue
		}
		a.mu.Lock()
		busy := a.updating || a.running != ""
		a.mu.Unlock()
		a.rec.mu.Lock()
		recording := a.rec.status != nil && a.rec.status.Recording
		a.rec.mu.Unlock()
		if busy || recording || ctx.Err() != nil {
			continue
		}
		b, err := backup.Snapshot(backupsDir(), backup.SourceFor(eng.Cfg, cfgPath, eng.Store, Version), backup.Daily, "daily", eng.Cfg.Backup.KeepDaily, time.Now())
		if err != nil {
			log.Printf("daily backup: %v", err)
			continue
		}
		log.Printf("daily backup: %s", b.Dir)
		a.emit("storage:changed", nil)
	}
}

// BackupView is a backup in Settings → Dataset.
type BackupView struct {
	Name     string    `json:"name"`
	Dir      string    `json:"dir"`
	Created  time.Time `json:"created"`
	Kind     string    `json:"kind"`
	Reason   string    `json:"reason"`
	Bytes    int64     `json:"bytes"`
	Recorder bool      `json:"recorder"` // holds the recorder's folder
	Legacy   bool      `json:"legacy"`   // restore by hand
}

// Backups are the open dataset's backups, newest first.
func (a *App) Backups() ([]BackupView, error) {
	if a.eng == nil {
		return nil, a.notReady()
	}
	all, err := backup.List(backupsDir(), datasetName(a.cfgPath))
	if err != nil {
		return nil, err
	}
	out := make([]BackupView, 0, len(all))
	for _, b := range all {
		out = append(out, BackupView{Name: b.Name, Dir: b.Dir, Created: b.Created, Kind: string(b.Kind), Reason: b.Reason,
			Bytes: b.Bytes, Recorder: b.Has("recorder"), Legacy: b.Legacy})
	}
	return out, nil
}

// RestoreBackup puts the backup named name back: the dataset as it is is
// backed up first ("before restore"), the store is closed, the backup's
// data folder copied in, and the dataset opened again. withRecorder also
// restores the recorder's sessions and voices: refused while a meeting is
// being recorded, and the recorder is restarted afterwards so it reads the
// restored voices. It returns the undo backup's name.
func (a *App) RestoreBackup(name string, withRecorder bool) (string, error) {
	if a.eng == nil {
		return "", a.notReady()
	}
	// Only a listed backup of this dataset, by name: never a path from
	// the page.
	all, err := backup.List(backupsDir(), datasetName(a.cfgPath))
	if err != nil {
		return "", err
	}
	var b *backup.Backup
	for i := range all {
		if all[i].Name == name {
			b = &all[i]
		}
	}
	if b == nil {
		return "", fmt.Errorf("no such backup")
	}
	recRunning := false
	if withRecorder {
		var st control.Status
		switch err := a.recCall(control.OpStatus, nil, &st); {
		case errors.Is(err, control.ErrNotRunning):
		case err != nil:
			return "", err
		case st.Recording:
			return "", fmt.Errorf("the recorder is recording a meeting; restore its folder once the meeting is over, or restore without it")
		default:
			recRunning = true
		}
	}
	a.mu.Lock()
	if a.updating || a.running != "" {
		a.mu.Unlock()
		return "", fmt.Errorf("Musubi is updating; restore once it is done")
	}
	a.updating = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
	}()
	cfg, path := a.eng.Cfg, a.cfgPath
	a.closeDataset("restoring a backup")
	undo, rerr := backup.Restore(backupsDir(), *b, backup.SourceFor(cfg, path, nil, Version), withRecorder, time.Now())
	// Open it again either way: a failed restore leaves the dataset as it
	// was.
	if err := a.openConfig(path, "after restoring a backup"); err != nil {
		a.err = err.Error()
		if rerr == nil {
			rerr = fmt.Errorf("restored, but opening the dataset failed: %w", err)
		}
	}
	if rerr == nil && recRunning {
		if err := a.RestartRecorder(); err != nil {
			rerr = fmt.Errorf("restored; restart the recorder from Status so it reads the restored voices (%v)", err)
		}
	}
	a.emit("app:status", a.Status())
	a.emit("storage:changed", nil)
	a.emit("meetings:changed", "")
	a.emit("queue:changed", nil)
	if rerr != nil {
		return undo.Name, rerr
	}
	return undo.Name, nil
}
