// Package dataformat versions the recorder's data folder (sessions,
// voices, models, settings) the way internal/store/upgrade.go versions
// the database: a build finding a folder of a newer format leaves it
// alone, and one of an older format (or none recorded: every folder
// before 2026-11-12) is copied, as far as it can change, before this
// build's conversions run (docs/data-safety.md, item 6).
package dataformat

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/anielsud/musubi/internal/recorder/voices"
)

// Version is the folder's format. 1 (2026-11-12): the first recorded;
// sessions and voices as they were. Bump it with any change to what the
// folder holds that an older build would misread, and give the change a
// line in the release notes.
const Version = 1

// File holds the format, in the folder itself.
const File = "format"

// ErrNewer is a folder written by a newer Musubi than this one.
var ErrNewer = errors.New("the recorder's folder was written by a newer Musubi; nothing is recorded into it or changed in it until that version is installed again")

// Read is the folder's format: 0 when none is recorded (a folder from
// before formats, a new one, or Tomoe's).
func Read(dir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	v, err := strconv.Atoi(string(bytes.TrimSpace(b)))
	if err != nil || v < 0 {
		// Unreadable is treated as unknown, not as old: an older build
		// mustn't convert what it can't place.
		return 0, fmt.Errorf("%w (its %s file says %q)", ErrNewer, File, bytes.TrimSpace(b))
	}
	return v, nil
}

// Check is nil for a folder this build may read and write, else why not
// (ErrNewer for a newer one).
func Check(dir string) error {
	v, err := Read(dir)
	if err != nil {
		return err
	}
	if v > Version {
		return fmt.Errorf("%w (format %d; this build reads up to %d)", ErrNewer, v, Version)
	}
	return nil
}

// Prepare readies the folder for this build when the recorder starts:
// ErrNewer for a newer one, untouched. An older one has its voices folder
// copied (voices.Backup; backup false skips that, [recorder.meeting]
// voice_profile_backups) before the format is recorded, so the
// conversions that follow (voices files converted on load, the relearn
// for new rules) start from a copy. Sessions aren't converted: readers
// ignore fields they don't know. from is the format found.
func Prepare(dir string, backup bool, now time.Time) (from int, err error) {
	if err := Check(dir); err != nil {
		return 0, err
	}
	from, _ = Read(dir)
	if from == Version {
		return from, nil
	}
	if backup {
		if _, err := voices.Backup(filepath.Join(dir, "voices"), now); err != nil {
			return from, fmt.Errorf("copying the voice profiles before upgrading the recorder's folder: %w (free some space, or set [recorder.meeting] voice_profile_backups = false)", err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return from, err
	}
	tmp := filepath.Join(dir, File+".tmp")
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(Version)+"\n"), 0o600); err != nil {
		return from, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, File)); err != nil {
		os.Remove(tmp)
		return from, err
	}
	return from, nil
}
