package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/recording"
)

// version is the build version, set with -ldflags "-X main.version=..."
// and reported in the live feed's hello message.
var version = "dev"

// runCmd is the recorder proper: no window, no hotkey; it records each
// meeting the detector sees, beside Tomoe, into its own data folder.
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Record meetings as they happen, beside Tomoe, into the recorder's own folder",
	Long: `Watches for meetings (Tomoe's [meeting] auto_detect) and records each one
the way Tomoe's app does: live transcript, saved audio and session.json,
diarization after the meeting. The live feed, if [meeting] live_feed is
on, is served on the recorder's own socket.

Tomoe's config and models are read, never written. Sessions, a copy of
Tomoe's voices and the socket go under --data, which may not be Tomoe's
data folder. SIGTERM or SIGINT stops and saves a recording in progress
before exiting.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		data, _ := cmd.Flags().GetString("data")
		return withMainLoop(func() error { return runRecorder(data) })
	},
}

func init() {
	runCmd.Flags().String("data", defaultDataDir(), "the recorder's data folder (never Tomoe's)")
	rootCmd.AddCommand(runCmd)
}

func runRecorder(dataFlag string) error {
	dir, err := useDataDir(dataFlag)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.SessionDir(), 0o700); err != nil {
		return err
	}
	cfg, err := loadTomoeConfig()
	if err != nil {
		return err
	}
	fmt.Printf("musubi-recorder %s: data %s, models %s, live socket %s\n", version, dir, cfg.Transcription.ModelPath, livefeed.SocketPath())

	recording.Version = version
	recording.CheckPermissions()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	r := recording.New(cfg)
	if err := r.Start(ctx); err != nil {
		r.Shutdown()
		return err
	}
	r.Run(ctx)
	fmt.Println("musubi-recorder: stopped")
	return nil
}

// loadTomoeConfig reads Tomoe's config file, read-only. Without one the
// defaults apply, as they would for Tomoe.
func loadTomoeConfig() (*config.Config, error) {
	cfg, err := config.Load(config.Path())
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("config: %s not found; using defaults\n", config.Path())
		return config.DefaultConfig(), nil
	}
	return cfg, err
}
