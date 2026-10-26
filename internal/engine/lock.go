package engine

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrBusy: another process is running the knowledge layer on this dataset.
var ErrBusy = errors.New("another knowledge run is going on this dataset (the app or the CLI)")

// lockFile takes an exclusive lock on path without waiting.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
