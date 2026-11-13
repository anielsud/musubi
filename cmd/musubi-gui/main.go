// Command musubi-gui is the Musubi desktop app: the live side panel, Q&A
// and the explorer.
package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/anielsud/musubi/internal/gui"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is set at build time (make build-gui).
var version = "dev"

// channel is "release" in the DMG the release workflow builds (make
// CHANNEL=release); only that build checks for and installs updates.
var channel = "local"

func main() {
	gui.Version, gui.Channel = version, channel
	// A Dock-launched app has no terminal; keep a log for diagnosis.
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, "Library", "Logs", "Musubi")
		os.MkdirAll(dir, 0o700)
		if f, err := os.OpenFile(filepath.Join(dir, "musubi-gui.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			log.SetOutput(f)
			log.SetFlags(log.LstdFlags | log.Lmicroseconds)
			// A crash (an unrecovered panic or a fatal runtime error) writes
			// its stack here too; otherwise it goes to stderr, which a
			// Dock-launched app does not keep.
			if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
				log.Printf("crash output: %v", err)
			}
		}
	}
	app := gui.NewApp()
	err := wails.Run(&options.App{
		Title:            "Musubi",
		Width:            1080,
		Height:           900,
		MinWidth:         360,
		MinHeight:        520,
		BackgroundColour: &options.RGBA{R: 14, G: 17, B: 22, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets, Handler: app.AudioHandler()},
		OnStartup:        app.Startup,
		OnShutdown:       app.Shutdown,
		// Wails's warnings and errors, including panics it recovers in
		// calls from the screen, go to the log (gui.WailsLog).
		Logger:             gui.WailsLog{},
		LogLevelProduction: logger.WARNING,
		Bind:               []interface{}{app},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			Appearance:           mac.NSAppearanceNameDarkAqua,
			WebviewIsTransparent: false,
			About:                &mac.AboutInfo{Title: "Musubi", Message: "Local-first knowledge, tied together."},
		},
	})
	if err != nil {
		log.Println("error:", err)
	}
}
