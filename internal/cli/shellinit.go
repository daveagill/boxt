package cli

import (
	_ "embed"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

//go:embed shellinit/boxt.sh
var shShellInit string

//go:embed shellinit/boxt.fish
var fishShellInit string

func newShellInitCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "shell-init <shell>",
		Short: "Print shell integration for `boxt workspace switch`",
		Long: `Print the shell function that lets ` + "`boxt workspace switch <name>`" + ` change the
current shell's directory. Add one line to your shell startup file:

  zsh:  eval "$(boxt shell-init zsh)"
  bash: eval "$(boxt shell-init bash)"
  sh:   eval "$(boxt shell-init sh)"
  fish: boxt shell-init fish | source

Supported shells: sh, bash, zsh, fish.

boxt never edits your startup files and never runs this integration itself; it
only prints it. Without the integration, switch with:

  cd "$(boxt workspace path <name>)"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := shellInitScript(args[0])
			if err != nil {
				return err
			}
			_, err = io.WriteString(app.Out, script)
			return err
		},
	}
}

func shellInitScript(shell string) (string, error) {
	switch shell {
	case "sh", "bash", "zsh":
		return shShellInit, nil
	case "fish":
		return fishShellInit, nil
	}
	return "", fmt.Errorf("unsupported shell %q (supported: sh, bash, zsh, fish)", shell)
}
