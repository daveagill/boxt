//go:build darwin

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile attempts an APFS copy-on-write clone from src to dst. dst must not
// exist. Unsupported filesystems return an error and the caller falls back to a
// byte copy.
func cloneFile(src, dst string, perm os.FileMode) error {
	if err := unix.Clonefile(src, dst, 0); err != nil {
		return &os.LinkError{Op: "reflink", Old: src, New: dst, Err: err}
	}
	return nil
}
