package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
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

Tomoe's config and models are read, never written; the [recorder.audio],
[recorder.transcription] and [recorder.meeting] tables of Musubi's config
(--musubi-config) override its keys. With [recorder] tomoe = "off" there
(see migrate-from-tomoe) nothing of Tomoe's is read: built-in defaults
stand in for its config, and models come from --data/models only.
Sessions, a copy of Tomoe's voices (unless Musubi's recorder is of record
or Tomoe is off) and the sockets (live feed, and control.sock for the Musubi app) go under
--data, which may not be Tomoe's data folder. SIGTERM or SIGINT stops and
saves a recording in progress before exiting.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		data, _ := cmd.Flags().GetString("data")
		musubi, _ := cmd.Flags().GetString("musubi-config")
		return withMainLoop(func() error { return runRecorder(data, musubi) })
	},
}

func init() {
	runCmd.Flags().String("data", defaultDataDir(), "the recorder's data folder (never Tomoe's)")
	runCmd.Flags().String("musubi-config", "", "Musubi's config file, whose [recorder.*] tables override Tomoe's settings (default: $MUSUBI_CONFIG, else the one the Musubi app last named, else ~/.config/musubi/config.toml)")
	rootCmd.AddCommand(runCmd)
}

func runRecorder(dataFlag, musubiConfig string) error {
	dir, err := useDataDir(dataFlag)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.SessionDir(), 0o700); err != nil {
		return err
	}
	if musubiConfig == "" {
		musubiConfig = config.ResolveMusubiPath(dir)
	}
	cfg, err := loadConfig(musubiConfig)
	if err != nil {
		return err
	}
	fmt.Printf("musubi-recorder %s: data %s, models %s, live socket %s\n", version, dir, strings.Join(config.ModelDirs(cfg), " then "), livefeed.SocketPath())

	recording.Version = version
	recording.CheckPermissions()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	r := recording.New(cfg)
	r.SetMusubiConfig(musubiConfig)
	// Before the models load, so the Musubi app can see the recorder
	// starting (status) rather than "not running".
	ctl, err := control.Listen(control.SocketPath(dir), r.Handle)
	if err != nil {
		fmt.Printf("Warning: control socket not started: %v\n", err)
	} else {
		fmt.Printf("control: serving %s\n", ctl.Path())
		defer ctl.Close()
	}
	if err := r.Start(ctx); err != nil {
		r.Shutdown()
		return err
	}
	r.Run(ctx)
	fmt.Println("musubi-recorder: stopped")
	return nil
}

// loadConfig reads Tomoe's config file, read-only (without one the
// defaults apply, as they would for Tomoe), with musubi's [recorder.*]
// tables over it. With [recorder] tomoe = "off" in musubi, Tomoe's file is
// not read: the recorder's defaults stand in for it (and Tomoe's models
// folder is not searched, config.SetTomoeOff). A Musubi file that can't
// be read is reported and left out, so a typo in the app's settings never
// stops meetings being recorded.
func loadConfig(musubi string) (*config.Config, error) {
	off, err := config.MusubiTomoeOff(musubi)
	if err != nil {
		fmt.Printf("Warning: %v; reading Tomoe's settings\n", err)
		off = false
	}
	config.SetTomoeOff(off)
	var cfg *config.Config
	if off {
		fmt.Println("config: Tomoe off ([recorder] tomoe); built-in defaults with Musubi's settings over them")
		cfg = config.StandaloneDefaults()
	} else if cfg, err = config.Load(config.Path()); errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("config: %s not found; using defaults\n", config.Path())
		cfg, err = config.DefaultConfig(), nil
	}
	if err != nil {
		return nil, err
	}
	if used, err := config.OverlayMusubi(cfg, musubi); err != nil {
		fmt.Printf("Warning: %v; using Tomoe's settings alone\n", err)
	} else if used {
		fmt.Printf("config: [recorder] settings from %s\n", musubi)
	}
	return cfg, nil
}
