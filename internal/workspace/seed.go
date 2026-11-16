package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daveagill/boxt/internal/config"
)

// SeedResult reports ignored files copied from the default workspace.
type SeedResult struct {
	Copied    int
	Conflicts []string
	Skipped   []string
}

// Seed copies all ignored files from the default workspace into a named
// workspace. It is equivalent to SeedWithRules with empty rules.
func (m *Manager) Seed(ctx context.Context, name string) (SeedResult, error) {
	return m.SeedWithRules(ctx, name, config.SeedConfig{})
}

// SeedWithRules copies ignored files from the default workspace into a named
// workspace, applying include/exclude rules. It never copies tracked files,
// never overwrites an existing destination path and never writes outside the
// workspace root. An empty include list copies every ignored file; excludes are
// applied afterwards and win. Source symlinks are preserved as symlinks rather
// than followed, so they may be absolute, escaping or dangling. Regular files
// are copied with a reflink when the host filesystem supports it and fall back
// to a byte copy otherwise; permissions and modification times are preserved.
// Source directories/files that do not exist are simply absent from Git's
// ignored-file list.
func (m *Manager) SeedWithRules(ctx context.Context, name string, rules config.SeedConfig) (SeedResult, error) {
	if name == DefaultName {
		return SeedResult{}, &ErrDefaultWorkspace{}
	}
	if err := ValidateName(name); err != nil {
		return SeedResult{}, err
	}
	matcher, err := newSeedMatcher(rules)
	if err != nil {
		return SeedResult{}, err
	}
	if _, err := m.repoRoot(ctx); err != nil {
		return SeedResult{}, err
	}
	ws, err := m.Get(ctx, name)
	if err != nil {
		return SeedResult{}, err
	}
	root := canonicalPath(ws.HostPath)
	if info, err := os.Stat(root); err != nil {
		return SeedResult{}, fmt.Errorf("inspect workspace %s: %w", ws.HostPath, err)
	} else if !info.IsDir() {
		return SeedResult{}, fmt.Errorf("workspace path is not a directory: %s", ws.HostPath)
	}

	out, err := m.Git.Output(ctx, m.ProjectRoot, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return SeedResult{}, fmt.Errorf("list ignored files: %w", err)
	}

	copier := m.seedCopierFor()
	result := SeedResult{}
	createdDirs := make([]seededDir, 0, 4)
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			result.Skipped = append(result.Skipped, rel+": unsafe path")
			continue
		}
		if !matcher.Match(rel) {
			continue
		}
		src := filepath.Join(m.ProjectRoot, clean)
		if rel == "." || src == m.ProjectRoot {
			continue
		}
		info, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return SeedResult{}, fmt.Errorf("inspect ignored file %s: %w", rel, err)
		}
		mode := info.Mode()
		kind := classifySeedSource(mode)
		switch kind {
		case seedSourceSymlink, seedSourceRegular:
		default:
			result.Skipped = append(result.Skipped, rel+": special file skipped")
			continue
		}
		symlink := kind == seedSourceSymlink

		// Resolve (and create) the destination ancestry before touching the
		// leaf. A parent component that is a symlink must stay inside the
		// workspace root; the final leaf is checked as a plain conflict.
		parent, err := ensureSeedParent(root, filepath.Dir(clean), m.ProjectRoot, &createdDirs)
		if err != nil {
			return SeedResult{}, fmt.Errorf("seed ignored file %s: %w", rel, err)
		}
		if err := checkSeedParent(root, parent); err != nil {
			return SeedResult{}, fmt.Errorf("seed ignored file %s: %w", rel, err)
		}
		dst := filepath.Join(parent, filepath.Base(clean))
		if _, err := os.Lstat(dst); err == nil {
			result.Conflicts = append(result.Conflicts, rel)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return SeedResult{}, fmt.Errorf("inspect seed destination %s: %w", rel, err)
		}

		if symlink {
			err = copySymlink(src, dst)
		} else {
			err = copier.copyRegularFile(src, dst, info)
		}
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				result.Conflicts = append(result.Conflicts, rel)
				continue
			}
			return SeedResult{}, fmt.Errorf("seed ignored file %s: %w", rel, err)
		}
		result.Copied++
	}

	// Directory times are applied after all children are written. Only
	// directories created by this seed are touched; pre-existing destination
	// directories keep their own permissions and timestamps.
	for _, dir := range createdDirs {
		if err := os.Chmod(dir.path, dir.perm); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("preserve directory permissions for %s: %w", dir.path, err)
		}
		if err := os.Chtimes(dir.path, dir.modTime, dir.modTime); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("preserve directory time for %s: %w", dir.path, err)
		}
	}
	return result, nil
}

// seededDir records a destination directory created by Seed together with the
// source directory mode and modification time to restore once its children
// exist.
type seededDir struct {
	path    string
	modTime time.Time
	perm    os.FileMode
}

// ensureSeedParent walks rel under root, creating missing directories and
// rejecting parent components whose symlink resolution leaves the workspace.
// It returns the resolved parent directory path.
func ensureSeedParent(root, rel, projectRoot string, created *[]seededDir) (string, error) {
	current := root
	if rel == "." || rel == "" {
		return current, nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	prefix := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("seed destination %q contains a traversal component", rel)
		}
		// Track the source-relative path separately from the resolved
		// destination path so directory times come from the right source dir.
		prefix = append(prefix, part)
		for {
			next := filepath.Join(current, part)
			info, err := os.Lstat(next)
			if err == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					resolved, resolveErr := filepath.EvalSymlinks(next)
					if resolveErr != nil {
						return "", fmt.Errorf("seed destination parent %s is a dangling symlink: %w", next, resolveErr)
					}
					if !pathWithin(root, resolved) {
						return "", fmt.Errorf("seed destination parent %s resolves outside the workspace: %s", next, resolved)
					}
					dirInfo, statErr := os.Stat(resolved)
					if statErr != nil {
						return "", fmt.Errorf("inspect seed destination parent %s: %w", resolved, statErr)
					}
					if !dirInfo.IsDir() {
						return "", fmt.Errorf("seed destination parent %s is not a directory", next)
					}
					current = resolved
					break
				}
				if !info.IsDir() {
					return "", fmt.Errorf("seed destination parent %s is not a directory", next)
				}
				current = next
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("inspect seed destination parent %s: %w", next, err)
			}
			srcDir := filepath.Join(projectRoot, filepath.Join(prefix...))
			perm := os.FileMode(0o755)
			var srcModTime time.Time
			if srcInfo, statErr := os.Stat(srcDir); statErr == nil && srcInfo.IsDir() {
				perm = srcInfo.Mode().Perm()
				srcModTime = srcInfo.ModTime()
			}
			if mkErr := os.Mkdir(next, perm); mkErr != nil {
				if errors.Is(mkErr, os.ErrExist) {
					// Another writer created the path between lstat and mkdir;
					// re-inspect it instead of assuming it is safe.
					continue
				}
				return "", fmt.Errorf("create seed directory %s: %w", next, mkErr)
			}
			if !srcModTime.IsZero() {
				*created = append(*created, seededDir{path: next, modTime: srcModTime, perm: perm})
			}
			current = next
			break
		}
	}
	return current, nil
}

// checkSeedParent re-verifies the destination ancestry immediately before a
// leaf is written. It catches races that could replace a verified parent with a
// symlink pointing elsewhere.
func checkSeedParent(root, parent string) error {
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve seed destination parent %s: %w", parent, err)
	}
	if !pathWithin(root, resolved) {
		return fmt.Errorf("seed destination parent %s resolves outside the workspace: %s", parent, resolved)
	}
	return nil
}

// pathWithin reports whether p is root or lies below root.
func pathWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// seedSourceKind distinguishes the source kinds Seed can recreate.
type seedSourceKind int

const (
	seedSourceRegular seedSourceKind = iota
	seedSourceSymlink
	seedSourceSpecial
)

// classifySeedSource decides how a source path is seeded. Symlinks are
// recreated as symlinks and regular files are copied; anything else (FIFOs,
// sockets, device nodes) is skipped with a warning.
func classifySeedSource(mode os.FileMode) seedSourceKind {
	switch {
	case mode&os.ModeSymlink != 0:
		return seedSourceSymlink
	case mode.IsRegular():
		return seedSourceRegular
	default:
		return seedSourceSpecial
	}
}

// copySymlink recreates src as a symlink at dst with the same target text. It
// never resolves or follows the link.
func copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	return os.Symlink(target, dst)
}

// reflinkDisableEnv forces the byte-copy path in tests.
const reflinkDisableEnv = "BOXT_TEST_DISABLE_REFLINK"

// seedCopier is the injectable copy engine for seeding. clone attempts a
// copy-on-write reflink; copy is the byte-copy fallback. Both must refuse to
// overwrite an existing destination and report os.ErrExist when it exists.
type seedCopier struct {
	clone func(src, dst string, perm os.FileMode) error
	copy  func(src, dst string, perm os.FileMode) error
}

// seedCopierFor returns the copy engine for one Seed call. Tests may inject a
// copier on the Manager; BOXT_TEST_DISABLE_REFLINK=1 disables reflinks.
func (m *Manager) seedCopierFor() seedCopier {
	copier := seedCopier{clone: cloneFile, copy: copyFile}
	if m.copier != nil {
		copier = *m.copier
	}
	if os.Getenv(reflinkDisableEnv) == "1" {
		copier.clone = nil
	}
	return copier
}

// copyRegularFile copies one regular file, preferring a reflink. Clone failures
// other than an existing destination fall back to a byte copy (unsupported
// filesystems, EXDEV, EPERM, EOPNOTSUPP, EINVAL and similar). The source
// permission bits and modification time are preserved on both paths.
func (c seedCopier) copyRegularFile(src, dst string, info os.FileInfo) error {
	perm := info.Mode().Perm()
	if c.clone != nil {
		err := c.clone(src, dst, perm)
		if err == nil {
			return preserveFileMetadata(dst, perm, info.ModTime())
		}
		if errors.Is(err, os.ErrExist) {
			return err
		}
	}
	copyFn := c.copy
	if copyFn == nil {
		copyFn = copyFile
	}
	if err := copyFn(src, dst, perm); err != nil {
		return err
	}
	return preserveFileMetadata(dst, perm, info.ModTime())
}

// preserveFileMetadata applies the source mode and modification time.
func preserveFileMetadata(dst string, perm os.FileMode, modTime time.Time) error {
	if err := os.Chmod(dst, perm); err != nil {
		return err
	}
	return os.Chtimes(dst, modTime, modTime)
}

// copyFile byte-copies src to dst without overwriting. It reports os.ErrExist
// when dst already exists; callers remove dst on partial failure.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Chmod(dst, perm)
}
