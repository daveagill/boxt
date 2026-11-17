package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
)

func newConfigCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and edit project configuration",
		Long: `Inspect and edit .boxt/config.yaml.

Scalar settings are edited atomically while preserving comments and unrelated
YAML. Structured sections have typed owners: tools via boxt tool, run targets
via boxt target, ports via boxt port, mounts via boxt mount.`,
	}
	cmd.AddCommand(newConfigShowCmd(app))
	cmd.AddCommand(newConfigGetCmd(app))
	cmd.AddCommand(newConfigSetCmd(app))
	cmd.AddCommand(newConfigUnsetCmd(app))
	cmd.AddCommand(newConfigValidateCmd(app))
	return cmd
}

func newConfigShowCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print .boxt/config.yaml exactly as stored",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := app.loadProject()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p.ConfigPath)
			if err != nil {
				return fmt.Errorf("read configuration: %w", err)
			}
			_, err = app.Out.Write(data)
			return err
		},
	}
}

func newConfigGetCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get KEY",
		Short: "Print one effective configuration value",
		Long: `Print one effective configuration value after defaults are applied.

List values print one entry per line. Structured sections are read through their
own commands (boxt tool, boxt target, boxt port, boxt mount).`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeConfigKeys(false),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.configGet(args[0])
		},
	}
	return cmd
}

func (a *App) configGet(key string) error {
	spec, ok := config.LookupKey(key)
	if !ok {
		return unknownConfigKey(key)
	}
	if spec.Owner != "" {
		return fmt.Errorf("%s is managed by %s", key, spec.Owner)
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if spec.Kind == config.KeyList {
		for _, value := range spec.List(p.Config) {
			fmt.Fprintln(a.Out, value)
		}
		return nil
	}
	fmt.Fprintln(a.Out, spec.Scalar(p.Config))
	return nil
}

func newConfigSetCmd(app *App) *cobra.Command {
	var appendValue bool
	cmd := &cobra.Command{
		Use:   "set KEY VALUE",
		Short: "Set one scalar configuration key",
		Long: `Set one scalar configuration key.

seed.include and seed.exclude replace their contents unless --append is given.
Unknown values are validated before anything is written.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeConfigSet,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.configSet(args[0], args[1], appendValue)
		},
	}
	cmd.Flags().BoolVar(&appendValue, "append", false, "append to a list key instead of replacing it")
	return cmd
}

func (a *App) configSet(key, value string, appendValue bool) error {
	spec, ok := config.LookupKey(key)
	if !ok {
		return unknownConfigKey(key)
	}
	if spec.Kind != config.KeyList && appendValue {
		return fmt.Errorf("--append is only valid for list keys (seed.include, seed.exclude)")
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := config.SetKey(p.ConfigPath, key, value, appendValue); err != nil {
		return err
	}
	if appendValue {
		fmt.Fprintf(a.Out, "Appended %q to %s.\n", value, key)
	} else {
		fmt.Fprintf(a.Out, "Set %s = %s.\n", key, value)
	}
	if spec.Fingerprint {
		fmt.Fprintln(a.Out, "Note: this is a VM creation setting. Run `boxt recreate` to apply it.")
	}
	return nil
}

func newConfigUnsetCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unset KEY [VALUE]",
		Short: "Remove a scalar key or list entry",
		Long: `Remove a scalar key or list entry.

Without VALUE the whole key is removed and defaults apply again. With VALUE on
seed.include or seed.exclude only that entry is removed.`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeConfigUnset,
		RunE: func(cmd *cobra.Command, args []string) error {
			value := ""
			hasValue := len(args) == 2
			if hasValue {
				value = args[1]
			}
			return app.configUnset(args[0], value, hasValue)
		},
	}
	return cmd
}

func (a *App) configUnset(key, value string, hasValue bool) error {
	spec, ok := config.LookupKey(key)
	if !ok {
		return unknownConfigKey(key)
	}
	if hasValue && spec.Kind != config.KeyList {
		return fmt.Errorf("%s is not a list; omit the value to unset it", key)
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := config.UnsetKey(p.ConfigPath, key, value, hasValue); err != nil {
		return err
	}
	if hasValue {
		fmt.Fprintf(a.Out, "Removed %q from %s.\n", value, key)
	} else {
		fmt.Fprintf(a.Out, "Unset %s.\n", key)
	}
	return nil
}

func newConfigValidateCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate project configuration without side effects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := app.loadProject()
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "Configuration is valid: %s\n", p.ConfigPath)
			return nil
		},
	}
}

func unknownConfigKey(key string) error {
	return fmt.Errorf("unknown configuration key %q (supported: %s)", key, joinConfigKeys(config.ConfigKeys(false)))
}

func joinConfigKeys(keys []string) string {
	out := ""
	for i, key := range keys {
		if i > 0 {
			out += ", "
		}
		out += key
	}
	return out
}

func completeConfigKeys(writable bool) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return config.ConfigKeys(writable), cobra.ShellCompDirectiveNoFileComp
	}
}

func completeConfigSet(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return config.ConfigKeys(true), cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 1 {
		if values := config.KeyValues(args[0]); len(values) > 0 {
			return values, cobra.ShellCompDirectiveNoFileComp
		}
		if spec, ok := config.LookupKey(args[0]); ok && spec.Kind == config.KeyBool {
			return []string{"true", "false"}, cobra.ShellCompDirectiveNoFileComp
		}
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeConfigUnset(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return config.ConfigKeys(true), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
