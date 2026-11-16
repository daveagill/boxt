//go:build !linux && !darwin

package workspace

import (
	"errors"
	"os"
)

// errReflinkUnsupported is returned on hosts without a reflink primitive; the
// copy engine falls back to a byte copy.
var errReflinkUnsupported = errors.New("reflink is not supported on this platform")

func cloneFile(src, dst string, perm os.FileMode) error {
	return errReflinkUnsupported
}
