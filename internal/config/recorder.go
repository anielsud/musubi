package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// Recorder is Musubi's own meeting recorder (cmd/musubi-recorder,
// docs/recorder-screens.md). The app always shows its state, sources and
// diagnostics; which recorder's meetings, live feed and voices Musubi
// uses is OfRecord.
type Recorder struct {
	// OfRecord is "tomoe" (the default: meetings from tomoe_sessions, the
	// live feed from [live] tomoe_socket, exactly as before) or "musubi"
	// (both from the recorder's folder, Data).
	OfRecord string `toml:"of_record"`
	// Data is the recorder's folder: sessions/, live.sock, control.sock.
	Data string `toml:"data"`
	// Since is when the owner switched to "musubi": only recorder sessions
	// created after it are imported, since earlier ones shadowed meetings
	// Tomoe already gave Musubi.
	Since time.Time `toml:"since"`
	// Tomoe is "on" (the default, or unset: Tomoe's config, models and
	// voices may be read, and its session folder searched for meetings'
	// files) or "off": Musubi stands alone. The recorder then reads none
	// of Tomoe's files, and the app looks for meetings' files only in the
	// recorder's folder (SessionDirs). musubi-recorder migrate-from-tomoe
	// copies Tomoe's data in and sets it. tomoe_sessions stays, for that
	// command's repairs and for going back.
	Tomoe string `toml:"tomoe"`
	// Watchdog: while Musubi is open it checks the recorder every 10 s
	// and, after 30 s down, starts it (launchd) and warns (a red button,
	// a notification after 90 s). false: no checks, as before.
	Watchdog bool `toml:"watchdog"`
	// InstallAgent: when the recorder's LaunchAgent is missing and this
	// app carries the recorder (Contents/Helpers, as the DMG ships it),
	// Musubi installs the agent itself, as scripts/install-recorder.sh does
	// for a recorder built from source. false: Musubi only reports it
	// missing, as before.
	InstallAgent bool `toml:"install_agent"`
	// Relabel: renaming a voice profile can put the new name on the
	// speaker labels of the meetings it identified, and renaming a
	// speaker on a saved meeting relabels Musubi's copy, with no model
	// calls (docs/recorder-screens.md, "Renaming a voice profile renames
	// its speakers"). false: a profile rename changes only the profile,
	// and a renamed speaker re-extracts the meeting, as before.
	Relabel bool `toml:"relabel"`
	// Audio, Transcription and Meeting override the keys of the same
	// tables in Tomoe's config for the recorder (meanings in
	// internal/recorder/config). Kept as raw values: the recorder reads
	// them, the app only writes them from Settings.
	Audio         map[string]any `toml:"audio"`
	Transcription map[string]any `toml:"transcription"`
	Meeting       map[string]any `toml:"meeting"`
}

// MusubiRecords reports whether Musubi's recorder is of record.
func (c Config) MusubiRecords() bool { return c.Recorder.OfRecord == "musubi" }

// TomoeOff reports whether Musubi stands alone ([recorder] tomoe = "off").
func (c Config) TomoeOff() bool { return c.Recorder.Tomoe == "off" }

func (c Config) RecorderSessions() string { return filepath.Join(c.Recorder.Data, "sessions") }

// RecorderLiveSocket is the recorder's live feed, followed for its state
// and diagnostics whichever recorder is of record.
func (c Config) RecorderLiveSocket() string { return filepath.Join(c.Recorder.Data, "live.sock") }

func (c Config) RecorderControlSocket() string { return control.SocketPath(c.Recorder.Data) }

// SessionsDir is the session folder ingest reads: the recorder of
// record's.
func (c Config) SessionsDir() string {
	if c.MusubiRecords() {
		return c.RecorderSessions()
	}
	return c.TomoeSessions
}

// SessionsSince is the earliest creation time ingest takes from
// SessionsDir (zero: all).
func (c Config) SessionsSince() time.Time {
	if c.MusubiRecords() {
		return c.Recorder.Since
	}
	return time.Time{}
}

// LiveSocket is the live feed the panel follows: the recorder of
// record's.
func (c Config) LiveSocket() string {
	if c.MusubiRecords() {
		return c.RecorderLiveSocket()
	}
	return c.Live.TomoeSocket
}

// SessionDirs are the folders a saved session's files (audio, looks) may
// be in, the recorder of record's first. After the switch, meetings
// imported from Tomoe before it still have their files in Tomoe's folder,
// until they are copied over (migrate-from-tomoe): with Tomoe off, only
// the recorder's folder is searched.
func (c Config) SessionDirs() []string {
	if c.TomoeOff() {
		return []string{c.RecorderSessions()}
	}
	var out []string
	for _, d := range []string{c.SessionsDir(), c.TomoeSessions} {
		if d != "" && (len(out) == 0 || out[0] != d) {
			out = append(out, d)
		}
	}
	return out
}

// ContextSessionsDir is the session folder calendar.json and title.json
// are written into (m365 tomoe_context): the recorder's once it is of
// record, so Tomoe's folder is never written after the switch; Tomoe's
// before it.
func (c Config) ContextSessionsDir() string {
	if c.MusubiRecords() {
		return c.RecorderSessions()
	}
	return c.TomoeSessions
}

// SessionDir is the folder holding session id (a directory name, already
// validated by the caller), or "" if none has it.
func (c Config) SessionDir(id string) string {
	for _, d := range c.SessionDirs() {
		p := filepath.Join(d, id)
		if _, err := os.Stat(filepath.Join(p, "session.json")); err == nil {
			return p
		}
	}
	return ""
}
