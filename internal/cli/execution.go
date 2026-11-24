package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/platform"
	"github.com/20Youssef10/golab/internal/render"
	"github.com/20Youssef10/golab/internal/repl"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

const contentPrelude = "import os; os.makedirs('/content', exist_ok=True); os.chdir('/content')"

// attachSession resolves a session, attaches its kernel and performs the
// mandatory /content chdir. Terminal 401/404 errors prune the local session.
func attachSession(ctx context.Context, d *app.Deps, name string) (*state.Session, runtime.Kernel, error) {
	s, err := d.ResolveSession(ctx, name)
	if err != nil {
		return nil, nil, sessionErr(name, err)
	}
	kernel, err := d.RuntimeFor(ctx, s)
	if err != nil {
		return nil, nil, pruneLost(d, s.Name, err)
	}
	if _, err := kernel.Execute(ctx, contentPrelude, runtime.ExecOptions{IdleTimeout: 30 * time.Second}); err != nil {
		_ = kernel.Close()
		return nil, nil, pruneLost(d, s.Name, err)
	}
	return s, kernel, nil
}

func timeoutOption(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

type codeBlock struct {
	code    string
	id      string
	cellIdx int
}

func readCodeBlocks(d *app.Deps, file string) ([]codeBlock, *nb.Notebook, error) {
	if file == "" {
		if platform.IsStdinTTY() {
			return nil, nil, exitf(1, "[colab] Error: No input provided. Pipe code or provide a file.")
		}
		data, err := io.ReadAll(d.Stdin)
		if err != nil {
			return nil, nil, err
		}
		return []codeBlock{{code: string(data)}}, nil, nil
	}
	if strings.HasSuffix(strings.ToLower(file), ".ipynb") {
		fmt.Fprintf(d.Stderr, "[colab] Parsing notebook '%s'...\n", file)
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, err
		}
		notebook, err := nb.Parse(data)
		if err != nil {
			return nil, nil, err
		}
		blocks := make([]codeBlock, 0)
		for i := range notebook.Cells {
			cell := &notebook.Cells[i]
			if cell.CellType != "code" {
				continue
			}
			if cell.ID == "" {
				cell.ID = uuid.NewString()
			}
			blocks = append(blocks, codeBlock{code: string(cell.Source), id: cell.ID, cellIdx: i})
		}
		return blocks, notebook, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	return []codeBlock{{code: string(data)}}, nil, nil
}

// execOutputHook renders a cell's outputs. IPython keeps the kernel alive by
// reporting sys.exit as an error output, so SystemExit is translated back to
// CPython's UX: empty/None/integer exits are silent and only a non-integer
// message reaches stderr (without the "%tb" banner). Every other output is
// rendered exactly as before. Exit codes come from exitCodeFromOutputs.
func execOutputHook(d *app.Deps, outputImage string) func(runtime.Output) error {
	return func(o runtime.Output) error {
		if o.OutputType == "error" && o.EName == "SystemExit" {
			value := strings.TrimSpace(o.EValue)
			if value == "" || value == "None" {
				return nil
			}
			if _, err := strconv.Atoi(strings.Trim(value, `"'`)); err == nil {
				return nil
			}
			_, err := fmt.Fprintln(d.Stderr, render.Sanitize(value))
			return err
		}
		return renderOne(d, o, outputImage)
	}
}

func execCommand() *cobra.Command {
	var name, file, outputImage string
	var timeout float64
	var env []string
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Execute code in a session",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			envVars, err := parseEnvVars(env)
			if err != nil {
				return err
			}
			blocks, notebook, err := readCodeBlocks(d, file)
			if err != nil {
				return err
			}
			hasCode := false
			for _, b := range blocks {
				if strings.TrimSpace(b.code) != "" {
					hasCode = true
					break
				}
			}
			if !hasCode {
				return nil
			}
			s, kernel, err := attachSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			prelude := envPrelude(envVars)
			isNB := strings.HasSuffix(strings.ToLower(file), ".ipynb")
			defer func() {
				setRunning(d, s, nil)
				_ = kernel.Close()
				if isNB && notebook != nil {
					out := strings.TrimSuffix(file, filepath.Ext(file)) + "_output.ipynb"
					fmt.Fprintf(d.Stderr, "[colab] Saving notebook with outputs to '%s'...\n", out)
					if data, err := nb.Marshal(notebook); err == nil {
						_ = os.WriteFile(out, data, 0o600)
					}
				}
			}()
			setRunning(d, s, running("exec("+sourceLabel(file)+")"))
			outputHook := execOutputHook(d, outputImage)
			for i, block := range blocks {
				code := prelude + block.code
				var cellID *string
				if isNB {
					id := nb.Title(code)
					if id == "" {
						id = block.id
					}
					if id != "" {
						cellID = &id
					}
					suffix := ""
					if id != "" {
						suffix = " - " + id
					}
					fmt.Fprintf(d.Stderr, "[colab] Executing cell %d/%d%s...\n", i+1, len(blocks), suffix)
				}
				setLastExecution(d, s, sourceLabel(file), cellID)
				var renderErr error
				res, err := kernel.Execute(cmd.Context(), code, runtime.ExecOptions{
					IdleTimeout: timeoutOption(timeout),
					OnOutput: func(o runtime.Output) {
						if e := outputHook(o); e != nil && renderErr == nil {
							renderErr = e
						}
					},
				})
				if err == nil && renderErr != nil {
					err = renderErr
				}
				if err != nil {
					return pruneLost(d, s.Name, err)
				}
				if notebook != nil {
					notebook.Cells[block.cellIdx].Outputs = notebookOutputs(res.Outputs)
				}
				extra := map[string]any{}
				if isNB {
					extra["cell_index"] = i
					extra["cell_id"] = block.id
				}
				logExecution(d, s.Name, code, res.Outputs, extra)
				// A persistent IPython kernel keeps running after sys.exit, so
				// the kernel's error outputs are the only signal of a failed
				// cell. Mirror run's CPython mapping and stop at the first
				// non-zero cell; the deferred notebook write still runs.
				if exitCode := exitCodeFromOutputs(res.Outputs); exitCode != 0 {
					return exitf(exitCode, "")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVarP(&file, "file", "f", "", "File to execute")
	cmd.Flags().StringVar(&outputImage, "output-image", "", "Path to save a displayed image")
	cmd.Flags().Float64Var(&timeout, "timeout", 30, "Seconds without output before failing (0 for no limit)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Set KEY=VALUE in the remote kernel (repeatable)")
	return cmd
}

func sourceLabel(file string) string {
	if file == "" {
		return "stdin"
	}
	return file
}

// replKernel adapts runtime.Kernel to repl.Kernel and records execution events.
type replKernel struct {
	kernel      runtime.Kernel
	d           *app.Deps
	name        string
	outputImage string
}

func (k *replKernel) Execute(ctx context.Context, code string, opts repl.ExecOptions) (repl.Result, error) {
	res, err := k.kernel.Execute(ctx, code, runtime.ExecOptions{
		IdleTimeout: opts.Timeout,
		OnOutput: func(o runtime.Output) {
			if opts.OutputHook == nil {
				return
			}
			converted := repl.Output{
				OutputType: o.OutputType, Name: o.Name, Text: o.Text,
				Ename: o.EName, Evalue: o.EValue, Traceback: o.Traceback,
				Data: map[string]string{},
			}
			for mime, value := range o.Data {
				if s, ok := value.(string); ok {
					converted.Data[mime] = s
				}
			}
			opts.OutputHook(converted)
		},
	})
	if err != nil {
		return repl.Result{}, err
	}
	writeEvent(k.d, k.name, "execution", map[string]any{"code": code, "outputs": runtimeOutputsAsMaps(res.Outputs)})
	out := repl.Result{Outputs: make([]repl.Output, 0, len(res.Outputs))}
	for _, o := range res.Outputs {
		converted := repl.Output{
			OutputType: o.OutputType, Name: o.Name, Text: o.Text,
			Ename: o.EName, Evalue: o.EValue, Traceback: o.Traceback,
			Data: map[string]string{},
		}
		for mime, value := range o.Data {
			if s, ok := value.(string); ok {
				converted.Data[mime] = s
			}
		}
		out.Outputs = append(out.Outputs, converted)
	}
	return out, err
}

func replRenderer(kind, data string) string {
	switch kind {
	case "text/html":
		if md, err := render.HTMLToMarkdown(data); err == nil {
			return md
		}
	case "text/markdown":
		if out, err := render.RenderMarkdown(data); err == nil {
			return out
		}
	}
	return data
}

func replCommand() *cobra.Command {
	var name, outputImage string
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Start an interactive REPL",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			s, kernel, err := attachSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			defer func() { _ = kernel.Close() }()
			opts := []repl.Option{
				repl.WithRenderer(replRenderer),
				repl.WithOutputImage(outputImage),
				repl.WithLogger(d.Log),
			}
			rk := &replKernel{kernel: kernel, d: d, name: s.Name, outputImage: outputImage}
			if !platform.IsStdinTTY() {
				setRunning(d, s, running("repl(stdin)"))
				setLastExecution(d, s, "stdin", nil)
				defer setRunning(d, s, nil)
				frontend := repl.NewPiped(d.Stdin, d.Stdout, d.Stderr, opts...)
				if err := frontend.Run(cmd.Context(), rk); err != nil {
					return pruneLost(d, s.Name, err)
				}
				return nil
			}
			setRunning(d, s, running("repl"))
			defer setRunning(d, s, nil)
			writeEvent(d, s.Name, "repl_started", map[string]any{})
			frontend, err := repl.NewTTY(d.Stdin, d.Stdout, d.Stderr, opts...)
			if err != nil {
				return err
			}
			if err := frontend.Run(cmd.Context(), rk); err != nil {
				return pruneLost(d, s.Name, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVar(&outputImage, "output-image", "", "Path to save a displayed image")
	return cmd
}

func registerExecutionCommands(root *cobra.Command) {
	root.AddCommand(execCommand())
	root.AddCommand(replCommand())
}
