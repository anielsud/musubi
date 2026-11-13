//go:build !darwin

package backup

import "errors"

// clone has no clones to make here: copyTree copies byte by byte.
func clone(src, dst string) error { return errors.New("no clones on this platform") }
