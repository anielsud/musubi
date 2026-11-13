package main

import (
	"fmt"
	"os/exec"
)

// cloneCopy copies src to dst (which must not exist) with cp -c, which
// makes APFS clones (clonefile(2)): no extra disk space until either copy
// changes. -p keeps modification times.
func cloneCopy(src, dst string) error {
	out, err := exec.Command("/bin/cp", "-c", "-R", "-p", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp -c: %v: %s", err, out)
	}
	return nil
}
