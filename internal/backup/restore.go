package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/store"
)

// Restore puts backup b back into the dataset cur describes, whose
// database no one may have open (cur.CopyDB nil; the app closes its store
// first, and the command line refuses while the app holds the dataset).
// First the dataset as it is now is backed up (kind Undo, "before
// restore"), so a restore can itself be undone; then the data folder's
// contents are moved aside, the backup's copied in, and what was moved
// aside removed. With withRecorder the recorder's folder is restored the
// same way (its models and logs stay); the caller makes sure nothing is
// being recorded. The config is not restored: the backup's copy is in its
// config/ folder. It returns the undo backup.
func Restore(root string, b Backup, cur Source, withRecorder bool, now time.Time) (Backup, error) {
	if b.Legacy {
		return Backup{}, errors.New("this backup was made before backups had a manifest; restore it by hand (docs/upgrades.md, §7)")
	}
	if b.Format > Format {
		return Backup{}, fmt.Errorf("this backup was made by a newer Musubi (layout %d; this one reads up to %d)", b.Format, Format)
	}
	if schema, format := store.Versions(); b.Schema > schema || b.DBFormat > format {
		return Backup{}, fmt.Errorf("this backup's database was written by a newer Musubi (schema %d, format %d; this one reads up to %d, %d)", b.Schema, b.DBFormat, schema, format)
	}
	if !b.Has("data") {
		return Backup{}, errors.New("this backup holds no data folder")
	}
	if filepath.Clean(b.DataDir) != filepath.Clean(cur.DataDir) {
		return Backup{}, fmt.Errorf("this backup is of the data folder %s, not this dataset's (%s)", b.DataDir, cur.DataDir)
	}
	if withRecorder {
		if !b.Has("recorder") {
			return Backup{}, errors.New("this backup holds no recorder folder")
		}
		if filepath.Clean(b.RecorderDir) != filepath.Clean(cur.RecorderDir) {
			return Backup{}, fmt.Errorf("this backup's recorder folder is %s, not this dataset's (%s)", b.RecorderDir, cur.RecorderDir)
		}
	}
	if cur.CopyDB != nil {
		return Backup{}, errors.New("close the dataset before restoring into it")
	}
	release, err := holdExclusive(cur.DataDir)
	if err != nil {
		return Backup{}, err
	}
	defer release()
	mu.Lock()
	defer mu.Unlock()

	undo, err := makeLocked(root, cur, Undo, "before restore", now)
	if err != nil {
		return Backup{}, fmt.Errorf("backing up the dataset as it is, before restoring: %w", err)
	}
	stamp := now.Format("20060102-150405")
	data, err := swapIn(cur.DataDir, filepath.Join(b.Dir, "data"), dataStays, special(root, cur), asidePrefix+stamp)
	if err != nil {
		return undo, fmt.Errorf("restoring the data folder (nothing changed; the dataset as it was is also in %s): %w", undo.Dir, err)
	}
	if withRecorder {
		rec, err := swapIn(cur.RecorderDir, filepath.Join(b.Dir, "recorder"), recorderKeep, []string{root}, asidePrefix+stamp)
		if err != nil {
			data.rollback()
			return undo, fmt.Errorf("restoring the recorder's folder (nothing changed; the dataset as it was is also in %s): %w", undo.Dir, err)
		}
		rec.commit()
	}
	data.commit()
	return undo, nil
}

// dataStays are what a restore leaves in the data folder: the database
// and its journal are replaced with the rest.
func dataStays(name string) bool {
	return name == "backups" || name == LockName || strings.HasPrefix(name, asidePrefix)
}

type swap struct {
	dir, aside string
	moved      []string
	copied     []string
}

// swapIn moves dir's entries (but those keep holds back, and specials)
// into dir/<aside>, then copies from's entries (but those keep holds back)
// into dir. On failure it puts dir back as it was.
func swapIn(dir, from string, keep func(string) bool, specials []string, aside string) (*swap, error) {
	s := &swap{dir: dir, aside: filepath.Join(dir, aside)}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(s.aside, 0o700); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		s.rollback()
		return nil, err
	}
	isSpecial := func(p string) bool {
		for _, sp := range specials {
			if filepath.Clean(sp) != filepath.Clean(dir) && within(sp, p) {
				return true
			}
		}
		return false
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		t := e.Type()
		if p == s.aside || keep(e.Name()) || isSpecial(p) || !(t.IsDir() || t.IsRegular() || t&fs.ModeSymlink != 0) {
			continue
		}
		if err := os.Rename(p, filepath.Join(s.aside, e.Name())); err != nil {
			s.rollback()
			return nil, err
		}
		s.moved = append(s.moved, e.Name())
	}
	in, err := os.ReadDir(from)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.rollback()
		return nil, err
	}
	for _, e := range in {
		if keep(e.Name()) {
			continue
		}
		if _, err := copyTree(filepath.Join(from, e.Name()), filepath.Join(dir, e.Name()), nil, nil); err != nil {
			s.copied = append(s.copied, e.Name())
			s.rollback()
			return nil, err
		}
		s.copied = append(s.copied, e.Name())
	}
	return s, nil
}

func (s *swap) rollback() {
	for _, n := range s.copied {
		os.RemoveAll(filepath.Join(s.dir, n))
	}
	for _, n := range s.moved {
		os.Rename(filepath.Join(s.aside, n), filepath.Join(s.dir, n))
	}
	os.RemoveAll(s.aside)
}

func (s *swap) commit() { os.RemoveAll(s.aside) }
