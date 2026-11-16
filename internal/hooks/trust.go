package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/fsutil"
)

// Hash returns a stable SHA-256 of the normalized hook configuration.
func Hash(cfg *config.Config) (string, error) {
	var h config.HooksConfig
	if cfg != nil && cfg.Hooks != nil {
		h = *cfg.Hooks
	}
	h = normalize(h)
	data, err := json.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("encode hook configuration: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func normalize(h config.HooksConfig) config.HooksConfig {
	out := config.HooksConfig{
		WorkspaceCreate: normalizeSet(h.WorkspaceCreate),
		WorkspaceMerge:  normalizeSet(h.WorkspaceMerge),
		WorkspaceRemove: normalizeSet(h.WorkspaceRemove),
	}
	return out
}

func normalizeSet(set config.HookSet) config.HookSet {
	out := config.HookSet{
		Pre:  append([]config.Hook(nil), set.Pre...),
		Post: append([]config.Hook(nil), set.Post...),
	}
	for i := range out.Pre {
		if out.Pre[i].Where == "" {
			out.Pre[i].Where = config.HookWhereGuest
		}
	}
	for i := range out.Post {
		if out.Post[i].Where == "" {
			out.Post[i].Where = config.HookWhereGuest
		}
	}
	return out
}

// TrustPath is the per-project approval file in the boxt data directory.
func TrustPath(dataDir, projectID string) string {
	return filepath.Join(dataDir, "hooks", projectID+".sha256")
}

// ReadTrusted returns the approved hash, or "" when no approval exists.
func ReadTrusted(dataDir, projectID string) (string, error) {
	data, err := os.ReadFile(TrustPath(dataDir, projectID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read hook approval: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// Trust records approval of the current hook configuration.
func Trust(dataDir, projectID, hash string) error {
	path := TrustPath(dataDir, projectID)
	return fsutil.WithLock(path, func() error {
		return fsutil.WriteFileAtomic(path, []byte(hash+"\n"), 0o600)
	})
}

// Clear removes the stored approval. It is not an error when none exists.
func Clear(dataDir, projectID string) error {
	path := TrustPath(dataDir, projectID)
	return fsutil.WithLock(path, func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clear hook approval: %w", err)
		}
		return nil
	})
}

// EnsureTrusted checks approval before an operation mutates workspace state.
func EnsureTrusted(dataDir, projectID string, cfg *config.Config, event string) error {
	set := cfg.HooksFor(event)
	if len(set.Pre) == 0 && len(set.Post) == 0 {
		return nil
	}
	current, err := Hash(cfg)
	if err != nil {
		return err
	}
	trusted, err := ReadTrusted(dataDir, projectID)
	if err != nil {
		return err
	}
	if trusted == current {
		return nil
	}
	return &ErrUntrusted{
		Event:    event,
		Commands: Commands(set),
		Trusted:  trusted,
		Current:  current,
	}
}

// ErrUntrusted refuses to execute project-controlled hooks without explicit
// approval.
type ErrUntrusted struct {
	Event    string
	Commands []string
	Trusted  string
	Current  string
}

func (e *ErrUntrusted) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s hooks are not trusted", e.Event)
	if len(e.Commands) > 0 {
		b.WriteString(":\n")
		for _, cmd := range e.Commands {
			fmt.Fprintf(&b, "  - %s\n", cmd)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (e *ErrUntrusted) Hint() string {
	return "Review the commands in .boxt/config.yaml, then run `boxt hooks trust`.\n" +
		"Changing the hook configuration invalidates the approval. Hooks run arbitrary project-controlled code."
}
