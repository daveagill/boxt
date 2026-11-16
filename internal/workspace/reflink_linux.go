//go:build linux

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile attempts a copy-on-write reflink from src to dst with the FICLONE
// ioctl. dst must not exist. Unsupported filesystems return an error and the
// caller falls back to a byte copy.
func cloneFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return &os.PathError{Op: "reflink", Path: dst, Err: err}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
