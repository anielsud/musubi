package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/config"
)

// firstRun gives a new owner a config that stands alone. With no config
// file at path and no Tomoe on this Mac, Musubi's own recorder is of
// record with its live feed on, and Tomoe's files are never looked for; the defaults would
// instead follow a Tomoe that isn't there, and no meeting would arrive.
// An owner with Tomoe keeps the defaults and switches in Settings →
// Recording. It returns whether it wrote the file.
func firstRun(path, tomoeData string, now time.Time) (bool, error) {
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if _, err := os.Stat(tomoeData); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	// The recorder's live feed is off in its own defaults (Tomoe's config
	// turned it on); Live follows it.
	edits := append(ofRecordEdits("musubi", now),
		config.Edit{Section: "recorder", Key: "tomoe", Value: "off"},
		config.Edit{Section: "recorder.meeting", Key: "live_feed", Value: true})
	return true, editConfig(path, edits)
}

// bundledRecorder is the recorder this app carries and the template of
// its LaunchAgent, as `make app-mac` packs them; "" when the app has none
// (make install-gui-mac: scripts/install-recorder.sh installs it).
func bundledRecorder() (exe, tmpl string) {
	self, err := os.Executable()
	if err != nil {
		return "", ""
	}
	contents := filepath.Dir(filepath.Dir(self))
	exe = filepath.Join(contents, "Helpers", "Musubi Recorder.app", "Contents", "MacOS", "musubi-recorder")
	tmpl = filepath.Join(contents, "Resources", "recorder-launchagent.plist.template")
	for _, p := range []string{exe, tmpl} {
		if _, err := os.Stat(p); err != nil {
			return "", ""
		}
	}
	return exe, tmpl
}

// installRecorderAgent writes the LaunchAgent for the bundled recorder,
// recording into data. The agent points into this app, so replacing the
// app updates the recorder too (launchd restarts it).
func installRecorderAgent(data string) error {
	exe, tmpl := bundledRecorder()
	if exe == "" {
		return fmt.Errorf("the recorder isn't installed (no %s)", recorderPlist())
	}
	return writeRecorderAgent(recorderPlist(), exe, tmpl, data)
}

func writeRecorderAgent(plist, exe, tmpl, data string) error {
	// The values go into XML unescaped, and launchd splits nothing.
	if strings.ContainsAny(exe+data, "<>&") {
		return fmt.Errorf("can't install the recorder: a path contains < > or &")
	}
	b, err := os.ReadFile(tmpl)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(data, "logs"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	out := strings.NewReplacer("__EXECUTABLE__", exe, "__DATA__", data).Replace(string(b))
	return os.WriteFile(plist, []byte(out), 0o644)
}
