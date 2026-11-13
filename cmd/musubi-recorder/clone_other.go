//go:build !darwin

package main

import "errors"

// cloneCopy has no clone to make here: cloneTree copies byte by byte.
func cloneCopy(src, dst string) error { return errors.New("no clones on this platform") }
