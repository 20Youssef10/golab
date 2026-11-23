package cli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/agentout"
	"github.com/20Youssef10/golab/internal/logx"
)

// NewAgentOutCommand emits a typed event through the shared structured-output
// writer. It is also a compact way to test the schema without a VM.
func NewAgentOutCommand() *cobra.Command {
	var format, typ string
	cmd := &cobra.Command{
		Use:   "agentout [text ...]",
		Short: "Emit a typed structured-output event (text, json or ndjson)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			w := d.Stdout
			closeIt := false
			if !agentout.IsWrapped(w) {
				var err error
				w, err = agentout.NewWriter(w, format)
				if err != nil {
					return mapAgentOutError(err)
				}
				closeIt = true
			}
			if err := agentout.Emit(w, typ, strings.Join(args, " ")); err != nil {
				return mapAgentOutError(err)
			}
			if closeIt {
				return mapAgentOutError(agentout.Close(w))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json or ndjson")
	cmd.Flags().StringVar(&typ, "type", "info", "Event type: info, output or error")
	return cmd
}

// RegisterAgentOutCommands adds `agentout` to the root command tree.
func RegisterAgentOutCommands(root *cobra.Command) { root.AddCommand(NewAgentOutCommand()) }

func mapAgentOutError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentout.ErrUnsupportedFormat) || errors.Is(err, agentout.ErrInvalidType) {
		return usagef("[colab] agentout: %s", logx.Redact(err.Error()))
	}
	return exitf(1, "[colab] agentout: %s", logx.Redact(err.Error()))
}
