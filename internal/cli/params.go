package cli

// TIER4: papermill-style notebook parameters. The heavy lifting is in
// internal/nbparams; this file only bridges cobra/inline flags to the notebook.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/nbparams"
)

// extractInlineParams mirrors extractInlineEnv: `run` disables interspersed
// flags so anything after the script path is forwarded verbatim. Papermill
// options after a notebook path are still golab options, not sys.argv values.
func extractInlineParams(args []string) ([]string, []string, string) {
	var forwarded, params []string
	var paramsFile string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-p" || args[i] == "--param":
			if i+1 < len(args) {
				params = append(params, args[i+1])
				i++
			}
		case strings.HasPrefix(args[i], "--param="):
			params = append(params, strings.TrimPrefix(args[i], "--param="))
		case strings.HasPrefix(args[i], "-p="):
			params = append(params, strings.TrimPrefix(args[i], "-p="))
		case args[i] == "--params-file":
			if i+1 < len(args) {
				paramsFile = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], "--params-file="):
			paramsFile = strings.TrimPrefix(args[i], "--params-file=")
		default:
			forwarded = append(forwarded, args[i])
		}
	}
	return forwarded, params, paramsFile
}

// applyNotebookParams merges flag/file values, injects them into the notebook
// and rebuilds the code-block list with correct cell indices. A missing
// parameters cell is a usage error raised before any VM allocation.
func applyNotebookParams(notebook *nb.Notebook, blocks []codeBlock, flags []string, paramsFile string) ([]codeBlock, error) {
	flagAssignments, err := nbparams.ParseFlags(flags)
	if err != nil {
		return nil, err
	}
	var fileAssignments []nbparams.Assignment
	if paramsFile != "" {
		data, err := os.ReadFile(paramsFile)
		if err != nil {
			return nil, usagef("[colab] Could not read params file '%s'.", filepath.Base(paramsFile))
		}
		fileAssignments, err = nbparams.ParseFile(data, filepath.Ext(paramsFile))
		if err != nil {
			return nil, usagef("[colab] Invalid params file: %s", logx.Redact(err.Error()))
		}
	}
	if len(flagAssignments) == 0 && len(fileAssignments) == 0 {
		return blocks, nil
	}
	if err := nbparams.Inject(notebook, nbparams.Merge(fileAssignments, flagAssignments)); err != nil {
		if errors.Is(err, nbparams.ErrNoParametersCell) {
			return nil, usagef("[colab] run -p/--params-file requires a notebook cell tagged 'parameters'.")
		}
		return nil, err
	}
	out := make([]codeBlock, 0, len(notebook.Cells))
	for i := range notebook.Cells {
		cell := &notebook.Cells[i]
		if cell.CellType != "code" {
			continue
		}
		if cell.ID == "" {
			cell.ID = uuid.NewString()
		}
		out = append(out, codeBlock{code: string(cell.Source), id: cell.ID, cellIdx: i})
	}
	return out, nil
}
