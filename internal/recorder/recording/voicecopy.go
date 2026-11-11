package recording

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyVoices brings Tomoe's voice files (each speaker model's profiles,
// voices.Path, and the user's own voice, voices.OwnerPath) from
// fromData/voices into toData/voices, so the recorder names people and
// tells the user's voice apart exactly as Tomoe would. Tomoe is the
// recorder of record: its copy wins over anything the recorder learnt
// since. Files that already match are left alone, so a profile store
// open in this process isn't reread for nothing. Only Tomoe's *.json
// files are read; nothing in Tomoe's folder is written. No Tomoe voices
// folder is not an error.
func CopyVoices(fromData, toData string) error {
	from, to := filepath.Join(fromData, "voices"), filepath.Join(toData, "voices")
	if same, err := sameDir(from, to); err != nil || same {
		return err
	}
	ents, err := os.ReadDir(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		// Tomoe's backups (name.json.bak-…) and half-written saves
		// (name.json.tmp) don't end in .json.
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := copyIfChanged(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sameDir reports whether a and b are one folder, so a misconfigured data
// folder can't make CopyVoices copy files onto themselves.
func sameDir(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, nil // a missing side is handled by the caller
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, nil
	}
	return os.SameFile(sa, sb), nil
}

// copyIfChanged writes src's bytes to dst unless dst already holds them,
// through a temporary file so a reader never sees half a file.
func copyIfChanged(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".copy"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// forgetOwnerVoices drops the owner voices loaded so far (see ownerFor),
// so the next recording reads the copy CopyVoices just made instead of
// what an earlier recording had in memory.
func forgetOwnerVoices() {
	ownersMu.Lock()
	defer ownersMu.Unlock()
	clear(owners)
}
