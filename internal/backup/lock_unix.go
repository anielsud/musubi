//go:build unix

package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockName is the file in the data folder the app holds a shared lock on
// while it has the dataset open, so a restore from the command line can
// tell. The lock goes with the process, however it ends.
const LockName = "musubi.lock"

// ErrInUse is a restore refused because the app has the dataset open.
var ErrInUse = errors.New("Musubi has this dataset open; quit it first (or restore from Settings → Dataset)")

func lockFile(dataDir string, how int) (func(), error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, LockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrInUse
		}
		return nil, fmt.Errorf("locking %s: %w", f.Name(), err)
	}
	return func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}

// HoldOpen marks the dataset in dataDir open until release is called.
// It fails while a restore is running.
func HoldOpen(dataDir string) (release func(), err error) {
	release, err = lockFile(dataDir, unix.LOCK_SH)
	if errors.Is(err, ErrInUse) {
		return nil, errors.New("a backup is being restored into this dataset; open it once that is done")
	}
	return release, err
}

// holdExclusive is held for a restore: no app has the dataset open.
func holdExclusive(dataDir string) (func(), error) { return lockFile(dataDir, unix.LOCK_EX) }
