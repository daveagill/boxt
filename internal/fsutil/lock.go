package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WithLock serializes an operation on a path across processes. The lock file
// lives in the system temp directory so project directories stay clean.
func WithLock(path string, fn func() error) error {
	sum := sha256.Sum256([]byte(path))
	lockPath := filepath.Join(os.TempDir(), "boxt-lock-"+hex.EncodeToString(sum[:8])+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open configuration lock: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock configuration: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// WriteFileExclusive creates path atomically and fails if it already exists.
func WriteFileExclusive(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if perm == 0 {
		perm = 0o644
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Link(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		if os.IsExist(err) {
			return os.ErrExist
		}
		// Some network/filesystems do not support hard links. Fall back to an
		// exclusive create; it still cannot overwrite an existing file.
		f, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if openErr != nil {
			if os.IsExist(openErr) {
				return os.ErrExist
			}
			return fmt.Errorf("create %s: %w", path, openErr)
		}
		if _, writeErr := f.Write(data); writeErr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return fmt.Errorf("write %s: %w", path, writeErr)
		}
		if syncErr := f.Sync(); syncErr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return fmt.Errorf("sync %s: %w", path, syncErr)
		}
		if closeErr := f.Close(); closeErr != nil {
			_ = os.Remove(path)
			return fmt.Errorf("close %s: %w", path, closeErr)
		}
		return nil
	}
	_ = os.Remove(tmpName)
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
