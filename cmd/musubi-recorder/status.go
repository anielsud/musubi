package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// exitRecording is status's exit code while a meeting is being recorded,
// so scripts (install-recorder.sh) can refuse to restart the recorder.
const exitRecording = 3

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Say whether the running recorder is recording",
	Long: `Asks the running recorder over its control socket and prints one line:
recording (with the meeting and since when), idle, or not running.

Exit status: 0 idle or not running, 3 recording, 1 the recorder did not
answer (treat as busy: it may be saving).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		flag, _ := cmd.Flags().GetString("data")
		dir, err := resolveDataDir(flag)
		if err != nil {
			return err
		}
		c := control.NewClient(filepath.Join(dir, "control.sock"))
		c.Timeout = 5 * time.Second
		var st control.Status
		err = c.Call(context.Background(), control.OpStatus, nil, &st)
		switch {
		case errors.Is(err, control.ErrNotRunning):
			fmt.Println("not running")
			return nil
		case err != nil:
			return fmt.Errorf("the recorder did not answer: %w", err)
		case st.Recording:
			what := "a meeting"
			if st.Session != nil && st.Session.Title != "" {
				what = st.Session.Title
			}
			fmt.Printf("recording %s since %s\n", what, st.StartedAt.Local().Format("15:04"))
			os.Exit(exitRecording)
		}
		fmt.Println("idle")
		return nil
	},
}

func init() {
	statusCmd.Flags().String("data", "", "the recorder's data folder (default "+defaultDataDir()+")")
	rootCmd.AddCommand(statusCmd)
}
