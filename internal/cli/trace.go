package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/trace"
)

// NewTraceCommand wraps any other golab command with the redacted wire
// recorder. It is deliberately a thin adapter: all redaction lives in
// internal/trace so a different front end cannot accidentally weaken it.
func NewTraceCommand() *cobra.Command {
	var output string
	var unsafe bool
	cmd := &cobra.Command{
		Use:   "trace -- <command> [args]",
		Short: "Run a command and append a redacted wire trace to an NDJSON file",
		Long: "trace wraps a command with GOLAB_TRACE-style recording. Headers and " +
			"query values are never written; bodies are reduced to a shape and hash. " +
			"--unsafe-bodies is UNSAFE: it adds redacted JSON bodies and must be " +
			"reviewed before the file is shared.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usagef("[colab] trace: expected a command after --")
			}
			if output == "" {
				output = os.Getenv(trace.EnvPath)
			}
			if output == "" {
				output = "wire.ndjson"
			}
			cfg := trace.Config{Path: output, UnsafeBodies: unsafe}
			rec, err := trace.New(cfg)
			if err != nil {
				return usagef("[colab] trace: %v", err)
			}
			d := depsOf(cmd)
			d.Trace = rec
			ctx := trace.WithConfig(cmd.Context(), cfg)
			code := execute(ctx, args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			// A kernel socket may have counted frames; emit the summary before
			// the wrapper returns so the trace is complete for this invocation.
			_ = rec.FlushWS()
			if code != 0 {
				return exitf(code, "")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Trace file (default: $GOLAB_TRACE or wire.ndjson)")
	cmd.Flags().BoolVar(&unsafe, "unsafe-bodies", false,
		"UNSAFE: include redacted JSON bodies (verify before sharing the trace)")
	return cmd
}

// RegisterTraceCommands adds `trace` to the root command tree.
func RegisterTraceCommands(root *cobra.Command) { root.AddCommand(NewTraceCommand()) }
