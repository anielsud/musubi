package backup

import "golang.org/x/sys/unix"

// clone makes dst an APFS clone of src, a file or a whole directory tree
// (clonefile(2)): it takes no space until either copy changes.
func clone(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}
