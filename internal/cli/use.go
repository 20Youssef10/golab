package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
)

// useCommand writes the default_session settings key. Resolution precedence
// (-s > GOLAB_SESSION > this value > unique fallback) lives in
// app.Deps.ResolveSession; this command only persists the local preference.
func useCommand() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "use NAME",
		Short: "Set the default session used when -s is omitted",
		Args: func(cmd *cobra.Command, args []string) error {
			if clear {
				if len(args) != 0 {
					return usagef("[colab] use: --clear takes no NAME.")
				}
				return nil
			}
			return exactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			name := ""
			if !clear {
				name = strings.TrimSpace(args[0])
				if err := state.ValidateName(name); err != nil {
					return usagef("[colab] use: %v", err)
				}
			}
			settings, err := d.SettingsStore.Load()
			if err != nil {
				return exitf(1, "[colab] use: %s", logx.Redact(err.Error()))
			}
			settings.DefaultSession = name
			if err := d.SettingsStore.Save(settings); err != nil {
				return exitf(1, "[colab] use: could not save settings: %s", logx.Redact(err.Error()))
			}
			if clear {
				fmt.Fprintln(d.Stderr, "[colab] Default session cleared.")
			} else {
				fmt.Fprintf(d.Stderr, "[colab] Default session set to '%s'.\n", name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "Clear the default session")
	return cmd
}

// RegisterUseCommands is the integration hook for the shared register.go.
func RegisterUseCommands(root *cobra.Command) { root.AddCommand(useCommand()) }
