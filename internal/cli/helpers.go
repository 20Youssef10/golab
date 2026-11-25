package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/render"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

// These seams default to the real os/nb/state calls and let CLI-level tests
// inject I/O failures (they mirror budgetCheckFn/attachSSHForceKernel). Keep
// production behaviour byte-identical to the direct calls they replace.
var (
	marshalNotebook   = nb.Marshal
	writeNotebookFile = os.WriteFile
	storePut          = func(d *app.Deps, s state.Session) error { return d.Store.Put(s) }
)

// hardwareLabel mirrors upstream's `_hardware_label`: NONE is shown as CPU.
func hardwareLabel(accelerator string) string {
	if accelerator == "NONE" || accelerator == "" {
		return "CPU"
	}
	return accelerator
}

func shapeLabel(s colabapi.Shape) string { return shapeLabelString(s.String()) }

func shapeLabelString(s string) string {
	if s == "HIGH_RAM" || s == "1" {
		return "High-RAM"
	}
	return "Standard"
}

// formatSessionLine is the single source of truth for session/status lines.
func formatSessionLine(name, endpoint, accelerator, variant string, status *string, machineShape string) string {
	parts := []string{
		fmt.Sprintf("[%s] %s", name, endpoint),
		"Hardware: " + hardwareLabel(accelerator),
		"Shape: " + shapeLabelString(machineShape),
		"Variant: " + variant,
	}
	if status != nil {
		parts = append(parts, "Status: "+*status)
	}
	return strings.Join(parts, " | ")
}

// pyRepr returns a safe, round-trippable Python string literal. JSON string
// escaping is a subset of Python's: every produced escape is understood by a
// Python string literal, so user text is never spliced into generated source.
func pyRepr(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

type envVar struct{ key, value string }

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnvVars implements upstream's strict --env validation before any
// session work or allocation happens.
func parseEnvVars(items []string) ([]envVar, error) {
	out := make([]envVar, 0, len(items))
	for _, item := range items {
		i := strings.IndexByte(item, '=')
		if i < 0 {
			return nil, usagef("[colab] Invalid --env value %q. Expected KEY=VALUE.", item)
		}
		key, value := item[:i], item[i+1:]
		if !envKeyRE.MatchString(key) {
			return nil, usagef("[colab] Invalid --env key %q. Expected a valid environment variable name.", key)
		}
		out = append(out, envVar{key: key, value: value})
	}
	return out, nil
}

func envPrelude(vars []envVar) string {
	if len(vars) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("import os\n")
	for _, v := range vars {
		fmt.Fprintf(&b, "os.environ[%s] = %s\n", pyRepr(v.key), pyRepr(v.value))
	}
	return b.String()
}

// Argument validators that map misuse to exit code 2 (cobra's own validators
// would otherwise be mapped to the generic exit code 1).
func noArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usagef("[colab] Unexpected argument %q.", args[0])
	}
	return nil
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return usagef("[colab] Expected %d argument(s), got %d.", n, len(args))
		}
		return nil
	}
}

func maxArgs(n int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > n {
			return usagef("[colab] Expected at most %d argument(s), got %d.", n, len(args))
		}
		return nil
	}
}

// resolveSessionName implements the shared rule when only a name is needed.
func resolveSessionName(ctx context.Context, d *app.Deps, name string) (string, error) {
	if name != "" {
		return name, nil
	}
	s, err := d.ResolveSession(ctx, "")
	if err != nil {
		return "", err
	}
	return s.Name, nil
}

// reportStateWriteError keeps a non-critical session-record write failure
// visible at debug level. Unlike the Pending handle, Running/LastExecution
// are progress fields: aborting a live execution over one would be worse than
// losing the field. Debug is off by default, matching docs/03 §8.
func reportStateWriteError(d *app.Deps, field string, err error) {
	if err == nil || d == nil || d.Log == nil {
		return
	}
	d.Log.Debug("session record write failed", "field", field, "error", err)
}

func writeEvent(d *app.Deps, name, eventType string, payload map[string]any) {
	logger, err := d.History(name)
	if err != nil {
		return
	}
	reportStateWriteError(d, "history event", logger.Append(eventType, payload))
}

func running(value string) *string { return &value }

func setRunning(d *app.Deps, s *state.Session, value *string) {
	s.Running = value
	reportStateWriteError(d, "running", storePut(d, *s))
}

func setLastExecution(d *app.Deps, s *state.Session, kind string, cell *string) {
	now := time.Now().Format("2006-01-02 15:04:05")
	s.LastExecution = &state.LastExecution{Kind: kind, Value: cell, At: now}
	reportStateWriteError(d, "last_execution", storePut(d, *s))
}

// saveNotebookResult writes executed cell outputs back to disk. Success is
// reported only after the write succeeds; a marshal or write failure is
// returned so exec/run can never exit 0 after losing the only copy of the
// outputs (gap-report §3.10).
func saveNotebookResult(d *app.Deps, notebook *nb.Notebook, out string) error {
	data, err := marshalNotebook(notebook)
	if err != nil {
		return fmt.Errorf("could not marshal notebook outputs for %q: %w", out, err)
	}
	if err := writeNotebookFile(out, data, 0o600); err != nil {
		return fmt.Errorf("could not save notebook outputs to %q: %w", out, err)
	}
	fmt.Fprintf(d.Stderr, "[colab] Saved notebook with outputs to '%s'.\n", out)
	return nil
}

// renderOutputs converts the runtime's output type into render's protocol-
// agnostic type and streams it through internal/render (sanitised, images on
// TTY or to --output-image).
func renderOutputs(d *app.Deps, outs []runtime.Output, outputImage string) error {
	cfg := d.RenderConfig
	cfg.Image.TargetPath = outputImage
	converted := make([]render.Output, 0, len(outs))
	for _, o := range outs {
		converted = append(converted, render.Output{
			Type: o.OutputType, Name: o.Name, Text: o.Text,
			Data: o.Data, Metadata: o.Metadata,
			Ename: o.EName, Evalue: o.EValue, Traceback: o.Traceback,
		})
	}
	return render.RenderAll(d.Stdout, d.Stderr, converted, cfg)
}

func renderOne(d *app.Deps, o runtime.Output, outputImage string) error {
	return renderOutputs(d, []runtime.Output{o}, outputImage)
}

// notebookOutputs maps executed outputs back into notebook cell outputs.
func notebookOutputs(outs []runtime.Output) []nb.Output {
	result := make([]nb.Output, 0, len(outs))
	for _, o := range outs {
		switch {
		case o.OutputType == "stream":
			name := o.Name
			if name == "" {
				name = "stdout"
			}
			result = append(result, nb.NewStreamOutput(name, o.Text))
		case o.Data != nil:
			kind := o.OutputType
			if kind != "display_data" && kind != "execute_result" {
				kind = "display_data"
			}
			out := nb.NewDisplayDataOutput(o.Data)
			out.OutputType = kind
			out.ExecutionCount = o.ExecutionCount
			result = append(result, out)
		case o.OutputType == "error":
			ename, evalue := o.EName, o.EValue
			if ename == "" {
				ename = "Error"
			}
			result = append(result, nb.NewErrorOutput(ename, evalue, o.Traceback))
		}
	}
	return result
}

// notebookCellResult copies one kernel result into a notebook code cell. The
// cell-level execution_count comes from the kernel's execute_reply (captured in
// runtime.Result.ExecutionCount); notebookOutputs still maps only output-level
// counts. Unexecuted cells keep whatever count their source notebook had.
func notebookCellResult(cell *nb.Cell, outs []runtime.Output, executionCount *int) {
	cell.Outputs = notebookOutputs(outs)
	cell.ExecutionCount = executionCount
}

func logExecution(d *app.Deps, name, code string, outs []runtime.Output, extra map[string]any) {
	payload := map[string]any{"code": code, "outputs": runtimeOutputsAsMaps(outs)}
	for k, v := range extra {
		payload[k] = v
	}
	writeEvent(d, name, "execution", payload)
}

func runtimeOutputsAsMaps(outs []runtime.Output) []any {
	result := make([]any, 0, len(outs))
	for _, o := range outs {
		m := map[string]any{"output_type": o.OutputType, "text": o.Text}
		if o.Name != "" {
			m["name"] = o.Name
		}
		if o.Data != nil {
			m["data"] = o.Data
			m["metadata"] = o.Metadata
		}
		if o.OutputType == "error" {
			m["ename"] = o.EName
			m["evalue"] = o.EValue
			m["traceback"] = o.Traceback
		}
		result = append(result, m)
	}
	return result
}

// pruneLost prunes a session after a terminal 401/404 and returns the
// user-facing error. The caller must propagate the returned error.
func pruneLost(d *app.Deps, name string, err error) error {
	if !errors.Is(err, runtime.ErrSessionLost) {
		return err
	}
	_ = d.PruneSession(name)
	return exitf(1, "[colab] Session '%s' appears to be lost (404/401). Cleaning up.", name)
}
