package voices

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileVersion is the voices file format this build reads and writes. A
// file of a newer version is refused, never rewritten: an older build
// would drop what it doesn't know (docs/data-safety.md, item 6).
const FileVersion = 1

// ErrNewer is a voices file written by a newer Musubi than this one.
var ErrNewer = errors.New("these voice profiles were written by a newer Musubi; install that version again to learn or name voices (recording carries on without them)")

// KeepBackups is how many copies of the voices folder are kept
// (<voices>.bak-<time>, beside it); older ones go once a new one is made.
const KeepBackups = 5

// CheckFile reports ErrNewer for a voices file of a newer version; a
// missing file is fine, and one that can't be read is left to Open.
func CheckFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	_, err = fileVersion(path, b)
	return err
}

func fileVersion(path string, b []byte) (int, error) {
	var f struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, nil
	}
	if f.Version > FileVersion {
		return f.Version, fmt.Errorf("%w (%s: version %d; this build reads up to %d)", ErrNewer, filepath.Base(path), f.Version, FileVersion)
	}
	return f.Version, nil
}

// Backup copies the voices folder dir to dir.bak-<now> before a rewrite
// that changes profiles in bulk (a load-time conversion, a relearn), and
// keeps the newest KeepBackups. Nothing is copied when there is no folder
// (""), or when the newest copy already holds the same files (its path):
// one upgrade's conversions and relearn share a copy.
func Backup(dir string, now time.Time) (string, error) {
	have, err := readTree(dir)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(have) == 0) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	old := backups(dir)
	if n := len(old); n > 0 {
		if prev, err := readTree(old[n-1]); err == nil && sameTree(prev, have) {
			return old[n-1], nil
		}
	}
	out := dir + ".bak-" + now.Format("20060102-150405")
	for i := 2; ; i++ {
		if _, err := os.Lstat(out); errors.Is(err, fs.ErrNotExist) {
			break
		}
		out = fmt.Sprintf("%s.bak-%s-%d", dir, now.Format("20060102-150405"), i)
	}
	tmp := out + ".partial"
	os.RemoveAll(tmp)
	for rel, b := range have {
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
			err = os.WriteFile(p, b, 0o600)
		}
		if err != nil {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("copying %s: %w", dir, err)
		}
	}
	if err := os.Rename(tmp, out); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("copying %s: %w", dir, err)
	}
	old = backups(dir)
	for len(old) > KeepBackups {
		os.RemoveAll(old[0])
		old = old[1:]
	}
	return out, nil
}

// backups are dir's copies, oldest first (their names sort by time).
func backups(dir string) []string {
	ms, _ := filepath.Glob(dir + ".bak-*")
	var out []string
	for _, m := range ms {
		if st, err := os.Lstat(m); err == nil && st.IsDir() && !strings.HasSuffix(m, ".partial") {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// readTree is every regular file under dir by its path within it, but
// half-written saves (*.tmp).
func readTree(dir string) (map[string][]byte, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = b
		return nil
	})
	return out, err
}

func sameTree(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}
