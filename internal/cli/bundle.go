package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/bundle"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/version"
)

// NewBundleCommand builds the local-only provenance command family. Neither
// subcommand authenticates, allocates a VM or opens a network connection.
func NewBundleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Record and verify reproducible execution provenance",
		Args:  noArgs,
	}
	cmd.AddCommand(newBundleRecordCommand(), newBundleVerifyCommand())
	return cmd
}

// RegisterBundleCommands adds `bundle` to the root command tree.
func RegisterBundleCommands(root *cobra.Command) { root.AddCommand(NewBundleCommand()) }

func newBundleRecordCommand() *cobra.Command {
	var out, root string
	var files, env []string
	var accelerator, variant, shape string
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Write a deterministic provenance manifest for local inputs",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return usagef("[colab] bundle record: --out is required")
			}
			d := depsOf(cmd)
			vars, err := parseEnvVars(env)
			if err != nil {
				return err
			}
			inputs := make([]bundle.File, 0, len(files))
			for _, p := range files {
				if err := cmd.Context().Err(); err != nil {
					return err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				rel, err := bundleInputPath(root, p)
				if err != nil {
					return mapBundleError(err)
				}
				inputs = append(inputs, bundle.File{Path: rel, Content: b})
			}
			envMap := make(map[string]string, len(vars))
			for _, v := range vars {
				envMap[v.key] = v.value
			}
			m, err := bundle.New(bundle.Input{
				Files: inputs, Env: envMap, Accelerator: accelerator,
				Variant: variant, Shape: shape,
				UpstreamVersion: version.Upstream, GolabVersion: version.Number,
			})
			if err != nil {
				return mapBundleError(err)
			}
			if err := bundle.Write(cmd.Context(), out, m); err != nil {
				return err
			}
			fmt.Fprintf(d.Stdout, "Recorded bundle %s -> %s\n", m.ID, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Destination manifest path")
	cmd.Flags().StringVar(&root, "root", "", "Local root used to store relative input paths")
	cmd.Flags().StringArrayVar(&files, "file", nil, "Local input file (repeatable)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Environment input KEY=VALUE (repeatable)")
	cmd.Flags().StringVar(&accelerator, "accelerator", "NONE", "Recorded accelerator")
	cmd.Flags().StringVar(&variant, "variant", "DEFAULT", "Recorded variant")
	cmd.Flags().StringVar(&shape, "shape", "STANDARD", "Recorded machine shape")
	return cmd
}

func newBundleVerifyCommand() *cobra.Command {
	var filesRoot string
	cmd := &cobra.Command{
		Use:   "verify MANIFEST",
		Short: "Verify a bundle manifest and optionally re-hash local inputs",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			m, err := bundle.Read(cmd.Context(), args[0])
			if err != nil {
				return mapBundleError(err)
			}
			if filesRoot != "" {
				if err := m.VerifyFiles(cmd.Context(), filesRoot); err != nil {
					return mapBundleError(err)
				}
			}
			fmt.Fprintf(d.Stdout, "Bundle %s verified%s\n", m.ID,
				map[bool]string{true: " (inputs re-hashed)", false: ""}[filesRoot != ""])
			return nil
		},
	}
	cmd.Flags().StringVar(&filesRoot, "files-root", "", "Local root for re-hashing recorded input files")
	return cmd
}

// bundleInputPath stores relative paths so a bundle is portable and cannot
// embed a host-specific absolute location.
func bundleInputPath(root, p string) (string, error) {
	base := root
	if base == "" && filepath.IsAbs(p) {
		if cwd, err := os.Getwd(); err == nil {
			base = cwd
		}
	}
	if base == "" {
		return p, nil
	}
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q is outside --root", bundle.ErrUnsafePath, p)
	}
	return rel, nil
}

func mapBundleError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, bundle.ErrSecretKey):
		return usagef("[colab] bundle: %s", logx.Redact(err.Error()))
	case errors.Is(err, bundle.ErrUnsafePath):
		return usagef("[colab] bundle: %s", logx.Redact(err.Error()))
	default:
		return exitf(1, "[colab] bundle: %s", logx.Redact(err.Error()))
	}
}
