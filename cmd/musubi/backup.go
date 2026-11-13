package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/backup"
	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/store"
)

// backupBefore backs the dataset up before a step that deletes or
// rewrites stored data in bulk ([backup] before_changes); the step must
// not run if it fails.
func backupBefore(cfg config.Config, st *store.Store, reason string) error {
	dir, err := backup.BeforeChange(backup.DefaultRoot(), backup.SourceFor(cfg, cfgPath, st, version), cfg.Backup, reason, time.Now())
	if err != nil {
		return err
	}
	if dir != "" {
		fmt.Fprintf(os.Stderr, "backed up first: %s\n", dir)
	}
	return nil
}

func backupCmd() *cobra.Command {
	c := &cobra.Command{Use: "backup", Short: "Back the dataset up, list its backups, restore one"}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "This dataset's backups, newest first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := backup.List(backup.DefaultRoot(), config.DatasetName(cfgPath))
			if err != nil {
				return err
			}
			for _, b := range all {
				note := b.Reason
				if b.Legacy {
					note += " — restore by hand"
				}
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", b.Name, b.Created.Format("2006-01-02 15:04"), b.Kind, size(b.Bytes), note)
			}
			if len(all) == 0 {
				fmt.Fprintln(os.Stderr, "no backups yet in", backup.DefaultRoot())
			} else {
				fmt.Fprintln(os.Stderr, "in", backup.DefaultRoot())
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "now",
		Short: "Back up the whole dataset: database, data folder, the recorder's folder, config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			b, err := backup.Make(backup.DefaultRoot(), backup.SourceFor(cfg, cfgPath, st, version), backup.Manual, "backed up by hand", time.Now())
			if err != nil {
				return err
			}
			fmt.Println(b.Dir)
			return nil
		},
	})
	var withRecorder, yes bool
	restore := &cobra.Command{
		Use:   "restore <backup folder>",
		Short: "Put a backup back; the dataset as it is now is backed up first",
		Long: `Puts a backup's data folder (database included) back in place of this
dataset's, after backing the dataset up as it is now (reason "before
restore"), so the restore can itself be undone by restoring that.

The config is not restored (the backup's copy is in its config/ folder).
The recorder's folder (sessions, voices) is restored only with
--recorder, and never while the recorder is recording; without it,
recordings made since the backup stay and are read in again by the next
update.

Refused while Musubi (the app) has the dataset open. Stop any
"musubi mcp" servers (Claude Code) and other musubi commands first: they
can't be detected, and would keep the replaced database open.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			b, err := findBackup(args[0], config.DatasetName(cfgPath))
			if err != nil {
				return err
			}
			recRunning := false
			if withRecorder {
				var st control.Status
				switch err := control.NewClient(cfg.RecorderControlSocket()).Call(cmd.Context(), control.OpStatus, nil, &st); {
				case errors.Is(err, control.ErrNotRunning):
				case err != nil:
					return fmt.Errorf("asking the recorder whether it is recording: %w", err)
				case st.Recording:
					return errors.New("the recorder is recording a meeting; restore its folder once the meeting is over (or leave out --recorder)")
				default:
					recRunning = true
				}
			}
			what := "the data folder " + cfg.DataDir
			if withRecorder {
				what += " and the recorder's folder " + cfg.Recorder.Data
			}
			fmt.Fprintf(os.Stderr, "Restore %s (%s, %s)\nover %s?\n", b.Name, b.Kind, b.Reason, what)
			fmt.Fprintln(os.Stderr, "Stop any `musubi mcp` servers and other musubi commands first.")
			if !yes {
				fmt.Fprint(os.Stderr, "Type yes to go ahead: ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(line) != "yes" {
					return errors.New("not restored")
				}
			}
			undo, err := backup.Restore(backup.DefaultRoot(), b, backup.SourceFor(cfg, cfgPath, nil, version), withRecorder, time.Now())
			if err != nil {
				return err
			}
			fmt.Printf("restored %s\nthe dataset as it was is in %s (musubi backup restore %s undoes this)\n", b.Name, undo.Dir, undo.Name)
			if recRunning {
				fmt.Fprintln(os.Stderr, "restart the recorder now, so it reads the restored voices: launchctl kickstart -k gui/$(id -u)/com.anielsud.musubi.recorder")
			}
			return nil
		},
	}
	restore.Flags().BoolVar(&withRecorder, "recorder", false, "also restore the recorder's folder (sessions, voices)")
	restore.Flags().BoolVar(&yes, "yes", false, "don't ask")
	c.AddCommand(restore)
	return c
}

// findBackup is a backup of dataset by folder or by name.
func findBackup(arg, dataset string) (backup.Backup, error) {
	all, err := backup.List(backup.DefaultRoot(), dataset)
	if err != nil {
		return backup.Backup{}, err
	}
	abs, _ := filepath.Abs(arg)
	for _, b := range all {
		if b.Name == arg || b.Dir == abs {
			return b, nil
		}
	}
	return backup.Backup{}, fmt.Errorf("%s isn't one of this dataset's backups (musubi backup list)", arg)
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
}
