package backup

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyTree copies src to dst (which must not exist): a whole tree at once
// as an APFS clone where it can (no space until either copy changes),
// else file by file. skip names paths left out; a directory holding a
// skipped path is walked instead of cloned whole. Only directories,
// regular files and symlinks are copied (never the recorder's sockets).
// It returns the bytes copied (their logical size: clones share theirs).
func copyTree(src, dst string, skip func(path string, d fs.DirEntry) bool, holdsSkipped func(dir string) bool) (int64, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return 0, err
	}
	if skip != nil && skip(src, fs.FileInfoToDirEntry(info)) {
		return 0, nil
	}
	if info.IsDir() && (holdsSkipped == nil || !holdsSkipped(src)) || info.Mode().IsRegular() {
		if err := cloneFn(src, dst); err == nil {
			return treeSize(dst)
		}
		// Not APFS, another volume, or no clones on this OS: copy (over
		// whatever a failed clone left).
		os.RemoveAll(dst)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return 0, err
		}
		return 0, os.Symlink(target, dst)
	case info.Mode().IsRegular():
		return copyFile(src, dst, info.Mode().Perm())
	case !info.IsDir():
		return 0, nil
	}
	if err := os.Mkdir(dst, info.Mode().Perm()|0o700); err != nil {
		return 0, err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range ents {
		n, err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), skip, holdsSkipped)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// cloneFn is clone; tests replace it to take the copying path.
var cloneFn = clone

func copyFile(src, dst string, perm fs.FileMode) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm|0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// treeSize is the logical size of the regular files under path.
func treeSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}
