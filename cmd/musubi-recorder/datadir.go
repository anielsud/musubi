package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anielsud/musubi/internal/recorder/config"
)

// defaultDataDir is the recorder's own data folder,
// $XDG_DATA_HOME/musubi/recorder (~/.local/share by default): its
// sessions, voice copies, live socket and logs, kept apart from Tomoe's
// while the two record side by side.
func defaultDataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "musubi", "recorder")
}

// resolveDataDir turns a --data value into the absolute folder the
// recorder may write, refusing Tomoe's data folder and anything inside
// it (through symlinks too): the recorder must never write Tomoe's
// sessions, voices or socket.
func resolveDataDir(flag string) (string, error) {
	dir := flag
	if dir == "" {
		dir = defaultDataDir()
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, rest)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	tomoe, err := filepath.Abs(config.TomoeDataDir())
	if err != nil {
		return "", err
	}
	if within(realPath(dir), realPath(tomoe)) {
		return "", fmt.Errorf("data folder %s is Tomoe's (%s) or inside it; the recorder keeps its own (default %s)", dir, tomoe, defaultDataDir())
	}
	return dir, nil
}

// useDataDir resolves flag and points config.DataDir (and so sessions,
// voices and the live socket) at it.
func useDataDir(flag string) (string, error) {
	dir, err := resolveDataDir(flag)
	if err != nil {
		return "", err
	}
	config.SetDataDir(dir)
	return dir, nil
}

// realPath is path with symlinks resolved as far as it exists, so a
// folder not created yet is still compared by where it would land.
func realPath(path string) string {
	var tail []string
	for p := filepath.Clean(path); ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{r}, tail...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Clean(path)
		}
		tail = append([]string{filepath.Base(p)}, tail...)
		p = parent
	}
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
