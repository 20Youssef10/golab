package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/contents"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/vmstat"
)

// Tier 2 ("see inside the VM") commands. Every kernel-bound snippet is built
// by internal/vmstat (JSON-decoded data, shell-quoted, bounded); cat streams
// raw bytes via the Contents API and needs no kernel execution at all.

func psCommand() *cobra.Command {
	var name string
	var timeout float64
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:     "ps",
		Aliases: []string{"top"},
		Short:   "Show processes, GPU, memory and disk usage on the VM",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			r, err := newVMRunner(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			defer r.Close()
			out, err := r.Exec(cmd.Context(), vmstat.BuildPSCode(), timeoutOption(timeout))
			if err != nil {
				return err
			}
			snap, err := vmstat.ParseSnapshot(out)
			if err != nil {
				return exitf(1, "[colab] ps: %s", logx.Redact(err.Error()))
			}
			writeEvent(d, r.s.Name, "vm_inspect", map[string]any{"op": "ps"})
			if jsonOutput {
				b, err := json.Marshal(snap)
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
				return nil
			}
			fmt.Fprintf(d.Stdout, "%-8s %-6s %-6s %-10s CMD\n", "PID", "%CPU", "%MEM", "ELAPSED")
			for _, p := range snap.Processes {
				fmt.Fprintf(d.Stdout, "%-8s %-6s %-6s %-10s %s\n", p.PID, p.PCPU, p.PMEM, p.ETime, p.Cmd)
			}
			if snap.GPU.Available {
				fmt.Fprintf(d.Stdout, "\nGPU:\n%s\n", snap.GPU.Text)
			} else {
				fmt.Fprintf(d.Stdout, "\nGPU: unavailable (%s)\n", snap.GPU.Reason)
			}
			if snap.Memory != "" {
				fmt.Fprintf(d.Stdout, "\nMemory:\n%s\n", snap.Memory)
			}
			if snap.Disk != "" {
				fmt.Fprintf(d.Stdout, "\nDisk:\n%s\n", snap.Disk)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().Float64Var(&timeout, "timeout", 20, "Seconds without output before failing (0 for no limit)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the snapshot as one JSON document on stdout")
	return cmd
}

func catCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "cat REMOTE",
		Short: "Print a remote file to stdout (binary-safe)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			c, session, err := contentsForSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			// ReadStream decodes (base64 when needed) and writes raw bytes
			// straight through: no added newline, no interpretation, pipe-safe.
			_, err = c.ReadStream(cmd.Context(), args[0], d.Stdout)
			if err != nil {
				switch {
				case errors.Is(err, contents.ErrNotFound):
					return exitf(1, "[colab] cat: %s: not found", args[0])
				case errors.Is(err, contents.ErrIsDirectory):
					return exitf(1, "[colab] cat: %s is a directory", args[0])
				default:
					return exitf(1, "[colab] cat: %s", logx.Redact(err.Error()))
				}
			}
			writeEvent(d, session, "file_operation", map[string]any{"op": "cat", "path": logx.Redact(args[0])})
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	return cmd
}

func tailCommand() *cobra.Command {
	var name string
	var follow bool
	var lines int
	var interval time.Duration
	var timeout float64
	cmd := &cobra.Command{
		Use:   "tail REMOTE",
		Short: "Print the tail of a remote file (optionally follow it)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			d := depsOf(cmd)
			// A canceled context is the normal way a user stops `tail -f`, so
			// it is a clean exit, not a 130.
			defer func() {
				if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
					runErr = nil
				}
			}()
			if err := validateSessionName(name); err != nil {
				return err
			}
			if lines < 0 {
				return usagef("[colab] tail: -n must be >= 0")
			}
			if interval <= 0 {
				return usagef("[colab] tail: --interval must be positive")
			}
			path := args[0]
			ctx := cmd.Context()
			r, err := newVMRunner(ctx, d, name)
			if err != nil {
				return err
			}
			defer r.Close()

			// Initial read: last N lines, and the current size as the offset.
			offset := int64(0)
			chunk, next, err := tailPoll(ctx, r, path, lines, offset, true, timeout)
			if err != nil {
				return err
			}
			fmt.Fprint(d.Stdout, chunk)
			offset = next
			if !follow {
				writeEvent(d, r.s.Name, "vm_inspect", map[string]any{"op": "tail", "path": logx.Redact(path)})
				return nil
			}
			// One cheap kernel execution per interval, bounded by context.
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					if ctx.Err() != nil {
						return nil
					}
					chunk, next, err := tailPoll(ctx, r, path, lines, offset, false, timeout)
					if err != nil {
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return nil
						}
						return err
					}
					if chunk != "" {
						fmt.Fprint(d.Stdout, chunk)
					}
					offset = next
				}
			}
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the file, polling once per interval")
	cmd.Flags().IntVarP(&lines, "lines", "n", 10, "Number of lines for the initial read")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "Poll interval when following")
	cmd.Flags().Float64Var(&timeout, "timeout", 15, "Seconds without output before failing (0 for no limit)")
	return cmd
}

// tailPoll runs a single kernel execution and decodes one tail envelope.
func tailPoll(ctx context.Context, r *vmRunner, path string, n int, offset int64, initial bool, timeout float64) (string, int64, error) {
	out, err := r.Exec(ctx, vmstat.BuildTailCode(path, n, offset, initial), timeoutOption(timeout))
	if err != nil {
		return "", 0, err
	}
	res, err := vmstat.ParseTail(out)
	if err != nil {
		return "", 0, exitf(1, "[colab] tail: %s", logx.Redact(err.Error()))
	}
	return res.Chunk, res.Offset, nil
}

func grepCommand() *cobra.Command {
	var name, glob string
	var ignoreCase bool
	var maxMatches int
	cmd := &cobra.Command{
		Use:   "grep PATTERN PATH",
		Short: "Search remote files on the VM",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			if maxMatches <= 0 {
				return usagef("[colab] grep: --max-matches must be positive")
			}
			spec := vmstat.GrepSpec{
				Pattern: args[0], Path: args[1], Glob: glob,
				IgnoreCase: ignoreCase, MaxMatches: maxMatches,
			}
			r, err := newVMRunner(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			defer r.Close()
			out, err := r.Exec(cmd.Context(), vmstat.BuildGrepCode(spec), timeoutOption(20))
			if err != nil {
				return err
			}
			res, err := vmstat.ParseMatches(out)
			if err != nil {
				return exitf(1, "[colab] grep: %s", logx.Redact(err.Error()))
			}
			fmt.Fprint(d.Stdout, res.Matches)
			writeEvent(d, r.s.Name, "vm_inspect", map[string]any{"op": "grep", "path": logx.Redact(args[1])})
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().BoolVarP(&ignoreCase, "ignore-case", "i", false, "Case-insensitive search")
	cmd.Flags().StringVar(&glob, "glob", "", "Only search files matching this glob (e.g. '*.py')")
	cmd.Flags().IntVar(&maxMatches, "max-matches", 200, "Maximum number of matching lines to return")
	return cmd
}

func duCommand() *cobra.Command {
	var name string
	var maxEntries int
	cmd := &cobra.Command{
		Use:   "du PATH",
		Short: "Show disk usage per entry under a remote path",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			if maxEntries <= 0 {
				return usagef("[colab] du: bound must be positive")
			}
			r, err := newVMRunner(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			defer r.Close()
			out, err := r.Exec(cmd.Context(), vmstat.BuildDuCode(args[0], maxEntries), timeoutOption(30))
			if err != nil {
				return err
			}
			res, err := vmstat.ParseLines(out)
			if err != nil {
				return exitf(1, "[colab] du: %s", logx.Redact(err.Error()))
			}
			for _, line := range strings.Split(strings.TrimSuffix(res.Lines, "\n"), "\n") {
				if line != "" {
					fmt.Fprintln(d.Stdout, line)
				}
			}
			writeEvent(d, r.s.Name, "vm_inspect", map[string]any{"op": "du", "path": logx.Redact(args[0])})
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().IntVar(&maxEntries, "max-entries", 200, "Maximum number of entries to report")
	return cmd
}

func findCommand() *cobra.Command {
	var name string
	var maxResults int
	cmd := &cobra.Command{
		Use:   "find NAME",
		Short: "Find files by name under the remote /content directory",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			if maxResults <= 0 {
				return usagef("[colab] find: bound must be positive")
			}
			r, err := newVMRunner(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			defer r.Close()
			out, err := r.Exec(cmd.Context(), vmstat.BuildFindCode("/content", args[0], maxResults), timeoutOption(30))
			if err != nil {
				return err
			}
			res, err := vmstat.ParseLines(out)
			if err != nil {
				return exitf(1, "[colab] find: %s", logx.Redact(err.Error()))
			}
			fmt.Fprint(d.Stdout, res.Lines)
			writeEvent(d, r.s.Name, "vm_inspect", map[string]any{"op": "find", "name": logx.Redact(args[0])})
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().IntVar(&maxResults, "max-results", 200, "Maximum number of paths to return")
	return cmd
}

func fetchCommand() *cobra.Command {
	var name, sha256 string
	var timeout float64
	cmd := &cobra.Command{
		Use:   "fetch URL [DEST]",
		Short: "Download a URL into the VM (wget with curl fallback)",
		Args:  rangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			// Validate before any allocation or kernel call: a bad URL or
			// digest must never reach the VM.
			if err := vmstat.ValidateFetchURL(args[0]); err != nil {
				return usagef("[colab] %s", err)
			}
			if err := vmstat.ValidateSHA256(sha256); err != nil {
				return usagef("[colab] %s", err)
			}
			dest := "/content/" + strings.TrimSpace(strings.TrimSuffix(args[0], "/"))
			if i := strings.LastIndexAny(dest, "/"); i >= 0 {
				dest = "/content/" + dest[i+1:]
			}
			if len(args) == 2 {
				dest = args[1]
			}
			if timeout <= 0 {
				return usagef("[colab] fetch: --timeout must be positive")
			}
			fmt.Fprintf(d.Stderr, "[colab] Fetching %s -> %s...\n", logx.Redact(args[0]), logx.Redact(dest))
			ctx := cmd.Context()
			r, err := newVMRunner(ctx, d, name)
			if err != nil {
				return err
			}
			defer r.Close()
			out, err := r.Exec(ctx, vmstat.BuildFetchCode(args[0], dest, sha256, int(timeout)),
				timeoutOption(timeout)+30*time.Second)
			if err != nil {
				return err
			}
			res, err := vmstat.ParseFetch(out)
			if err != nil {
				return exitf(1, "[colab] fetch: %s", logx.Redact(err.Error()))
			}
			if !res.OK {
				return exitf(1, "[colab] fetch failed: %s", logx.Redact(res.Error))
			}
			fmt.Fprintln(d.Stdout, res.Dest)
			if res.SHA256 != "" {
				fmt.Fprintf(d.Stdout, "sha256: %s\n", res.SHA256)
			}
			writeEvent(d, r.s.Name, "vm_inspect", map[string]any{"op": "fetch", "dest": logx.Redact(dest)})
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVar(&sha256, "sha256", "", "Expected SHA-256 hex digest of the download")
	cmd.Flags().Float64Var(&timeout, "timeout", 600, "Download timeout in seconds")
	return cmd
}

// registerVMCommands is called from registerExtraUtilityCommands in
// utility_extra.go; keeping registration here avoids touching register.go,
// which other agents own.
func registerVMCommands(root *cobra.Command) {
	root.AddCommand(psCommand())
	root.AddCommand(catCommand())
	root.AddCommand(tailCommand())
	root.AddCommand(grepCommand())
	root.AddCommand(duCommand())
	root.AddCommand(findCommand())
	root.AddCommand(fetchCommand())
}

// rangeArgs mirrors exactArgs/maxArgs for fetch's optional DEST.
func rangeArgs(lo, hi int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < lo || len(args) > hi {
			return usagef("accepts between %d and %d arg(s), received %d", lo, hi, len(args))
		}
		return nil
	}
}
