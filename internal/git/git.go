// Package git wraps Git commands behind a testable interface.
package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daveagill/boxt/internal/command"
)

// ErrNotRepository means the directory is not inside a Git repository.
var ErrNotRepository = errors.New("not a Git repository")

// Client runs Git commands. All arguments are passed separately.
type Client struct {
	Runner command.Runner
	Bin    string
}

// New returns a Git client using the provided process runner.
func New(r command.Runner) *Client {
	return &Client{Runner: r, Bin: "git"}
}

// Output runs git -C dir with args and returns trimmed stdout.
func (c *Client) Output(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := command.Output(ctx, c.Runner, command.Command{Name: c.Bin, Args: full})
	if err != nil {
		return "", err
	}
	return out, nil
}

// TopLevel returns the canonical top-level directory of the repository
// containing dir, or ErrNotRepository.
func (c *Client) TopLevel(ctx context.Context, dir string) (string, error) {
	out, err := c.Output(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		var cmdErr *command.Error
		if errors.As(err, &cmdErr) && cmdErr.ExitCode == 128 &&
			strings.Contains(strings.ToLower(cmdErr.Stderr), "not a git repository") {
			return "", ErrNotRepository
		}
		var exitCoder command.ExitCoder
		if errors.As(err, &exitCoder) && exitCoder.ExitStatus() == 128 {
			return "", ErrNotRepository
		}
		return "", fmt.Errorf("detect Git repository: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", ErrNotRepository
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve Git top level: %w", err)
	}
	return filepath.Clean(abs), nil
}

// IsRepository reports whether dir is inside a Git repository.
func (c *Client) IsRepository(ctx context.Context, dir string) (bool, error) {
	_, err := c.TopLevel(ctx, dir)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotRepository) {
		return false, nil
	}
	return false, err
}
