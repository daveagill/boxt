// Package cli implements the boxt command-line interface. It contains
// presentation and orchestration only; external tools live behind adapters.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/boxt/boxt/internal/command"
	"github.com/boxt/boxt/internal/git"
	"github.com/boxt/boxt/internal/lima"
	"github.com/boxt/boxt/internal/platform"
	"github.com/boxt/boxt/internal/project"
	"github.com/boxt/boxt/internal/toolchain"
)

// Version is overridden at build time with -ldflags.
var Version = "dev"

// App holds the process dependencies used by commands. Tests replace these.
type App struct {
	In        io.Reader
	Out       io.Writer
	Err       io.Writer
	Getwd     func() (string, error)
	Runner    command.Runner
	Git       *git.Client
	Lima      *lima.Client
	DataDir   func() (string, error)
	LookPath  platform.LookPath
	Confirm   func(prompt string) (bool, error)
	Logger    *slog.Logger
	Verbose   bool
	Toolchain ToolManager

	// Provision is installed by the toolchain layer. It runs after the VM is
	// running and before boxt reports the environment ready.
	Provision func(ctx context.Context, p *project.Project) (*toolchain.Report, error)

	// PrepareCommand wraps guest argv with the internal toolchain environment.
	PrepareCommand func(tools map[string]string, argv []string) []string
}

// New returns the production application wiring.
func New() *App {
	app := &App{
		In:       os.Stdin,
		Out:      os.Stdout,
		Err:      os.Stderr,
		Getwd:    os.Getwd,
		LookPath: exec.LookPath,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	runner := &loggingRunner{
		next: command.ExecRunner{},
		log:  func() *slog.Logger { return app.Logger },
	}
	app.Runner = runner
	app.Git = git.New(runner)
	app.Lima = lima.New(runner)
	app.DataDir = platform.DataDir
	app.Confirm = defaultConfirm(app.In, app.Out)
	app.Toolchain = toolchain.New(app.Lima)
	app.Provision = func(ctx context.Context, p *project.Project) (*toolchain.Report, error) {
		if app.Toolchain == nil {
			return nil, nil
		}
		return app.Toolchain.Provision(ctx, p)
	}
	app.PrepareCommand = toolchain.WrapArgv
	return app
}

// loggingRunner logs external commands at debug level without changing their
// behavior. It never logs environment variables.
type loggingRunner struct {
	next command.Runner
	log  func() *slog.Logger
}

func (l *loggingRunner) Run(ctx context.Context, c command.Command) error {
	logger := l.log()
	if logger != nil {
		logger.Debug("exec", "command", c.Name, "args", c.Args, "dir", c.Dir)
	}
	err := l.next.Run(ctx, c)
	if err != nil && logger != nil {
		logger.Debug("exec failed", "command", c.Name, "error", err)
	}
	return err
}

func defaultConfirm(in io.Reader, out io.Writer) func(string) (bool, error) {
	return func(prompt string) (bool, error) {
		if f, ok := in.(*os.File); ok {
			info, err := f.Stat()
			if err == nil && info.Mode()&os.ModeCharDevice == 0 {
				return false, errors.New("confirmation requires a terminal; re-run with --yes to confirm non-interactively")
			}
		}
		fmt.Fprint(out, prompt)
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("read confirmation: %w", err)
		}
		return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
	}
}

// NewRoot builds the Cobra command tree.
func NewRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "boxt [target] [-- command [args...]]",
		Short:         "Persistent Linux development environments for projects",
		Long:          "boxt gives each project a persistent Linux VM, shared toolchain and independent Git workspaces.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.run(cmd.Context(), args, cmd.ArgsLenAtDash(), false)
		},
	}
	root.SetIn(app.In)
	root.SetOut(app.Out)
	root.SetErr(app.Err)
	root.PersistentFlags().BoolVarP(&app.Verbose, "verbose", "v", false, "enable debug logging")
	root.CompletionOptions.DisableDefaultCmd = true

	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		level := slog.LevelInfo
		if app.Verbose {
			level = slog.LevelDebug
		}
		app.Logger = slog.New(slog.NewTextHandler(app.Err, &slog.HandlerOptions{Level: level}))
	}

	root.AddCommand(newInitCmd(app))
	root.AddCommand(newUpCmd(app))
	root.AddCommand(newDownCmd(app))
	root.AddCommand(newStatusCmd(app))
	root.AddCommand(newDestroyCmd(app))
	root.AddCommand(newRunCmd(app))
	root.AddCommand(newShellCmd(app))
	root.AddCommand(newToolCmd(app))
	root.AddCommand(newProvisionCmd(app))
	root.AddCommand(newWorkspaceCmd(app))
	root.AddCommand(newVersionCmd(app))
	return root
}

// Run executes the CLI with the provided arguments and returns a process exit
// status. It never panics on expected errors.
func Run(ctx context.Context, app *App, args []string) int {
	root := NewRoot(app)
	root.SetArgs(args)
	root.SetIn(app.In)
	root.SetOut(app.Out)
	root.SetErr(app.Err)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	PrintError(app.Err, err)
	var exitCoder interface{ ExitStatus() int }
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitStatus()
	}
	return 1
}

// PrintError renders an actionable error. Errors may implement Hint() string.
func PrintError(w io.Writer, err error) {
	var notInit *project.ErrNotInitialized
	if errors.As(err, &notInit) {
		fmt.Fprintf(w, "Project not initialized.\n\nRun `boxt init` to initialize this directory.\n")
		return
	}
	fmt.Fprintf(w, "Error: %v\n", err)
	var hinter interface{ Hint() string }
	if errors.As(err, &hinter) {
		if hint := hinter.Hint(); hint != "" {
			fmt.Fprintf(w, "\n%s\n", hint)
		}
	}
}

func (a *App) loadProject() (*project.Project, error) {
	cwd, err := a.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}
	return project.Lookup(cwd)
}

func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the boxt version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(app.Out, "boxt %s\n", Version)
			return err
		},
	}
}
