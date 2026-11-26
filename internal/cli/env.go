package cli

// TIER4: environment lockfiles and fresh-VM bundle verification live here so
// the shared command files need only one-line flag/hook additions.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/bundle"
	"github.com/20Youssef10/golab/internal/contents"
	"github.com/20Youssef10/golab/internal/envlock"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/version"
)

const lockRequirementsRemote = "content/golab-requirements.txt"

// RegisterEnvCommands adds `env` to the root command tree.
func RegisterEnvCommands(root *cobra.Command) { root.AddCommand(envCommand()) }

func envCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Inspect and freeze a session's Python environment",
		Args:  noArgs,
	}
	cmd.AddCommand(envFreezeCommand())
	return cmd
}

func envFreezeCommand() *cobra.Command {
	var name, out string
	cmd := &cobra.Command{
		Use:   "freeze",
		Short: "Freeze Python version, pip freeze and GPU into a deterministic lockfile",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if out == "" {
				return usagef("[colab] env freeze: --out is required.")
			}
			if err := validateSessionName(name); err != nil {
				return err
			}
			s, err := d.ResolveSession(cmd.Context(), name)
			if err != nil {
				return sessionErr(name, err)
			}
			kernel, err := d.RuntimeFor(cmd.Context(), s)
			if err != nil {
				return pruneLost(d, s.Name, err)
			}
			defer func() {
				setRunning(d, s, nil)
				_ = kernel.Close()
			}()
			setRunning(d, s, running("env freeze"))
			lock, err := probeEnvironment(cmd.Context(), d, kernel, s.Accelerator)
			if err != nil {
				return err
			}
			data, err := envlock.Marshal(lock)
			if err != nil {
				return err
			}
			if err := writeLockfile(cmd.Context(), out, data); err != nil {
				return err
			}
			fmt.Fprintf(d.Stderr, "[colab] Froze environment to %s (Python %s, %d requirement(s)).\n",
				filepath.Base(out), lock.Python, len(lock.Requirements))
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVarP(&out, "out", "o", "", "Lockfile destination")
	return cmd
}

// writeLockfile persists a lockfile atomically with owner-only permissions.
func writeLockfile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".golab-envlock-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseProbeOutputs(outs []runtime.Output) (*envlock.Lockfile, error) {
	var b strings.Builder
	for _, o := range outs {
		if o.OutputType == "stream" {
			b.WriteString(o.Text)
		}
	}
	return envlock.ParseProbe(b.String())
}

// probeEnvironment executes the static probe on a kernel. No user string is
// interpolated into the code and no raw kernel output reaches the terminal.
func probeEnvironment(ctx context.Context, d *app.Deps, k runtime.Kernel, accelerator string) (*envlock.Lockfile, error) {
	res, err := k.Execute(ctx, envlock.ProbeCode(), runtime.ExecOptions{IdleTimeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	lock, err := parseProbeOutputs(res.Outputs)
	if err != nil {
		return nil, exitf(1, "[colab] Environment probe failed: %s", logx.Redact(err.Error()))
	}
	lock.Accelerator = accelerator
	return lock, nil
}

// applyLockfile checks hardware/interpreter before installing requirements
// through the existing `uv pip install --system -r` automation. It returns the
// observed probe so `run --bundle` can reuse it instead of probing twice.
func applyLockfile(ctx context.Context, d *app.Deps, s *state.Session, k runtime.Kernel, lock *envlock.Lockfile) (*envlock.Lockfile, error) {
	res, err := executeTracked(ctx, d, s, k, envlock.ProbeCode(), runtime.ExecOptions{IdleTimeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	current, err := parseProbeOutputs(res.Outputs)
	if err != nil {
		return nil, exitf(1, "[colab] Environment probe failed: %s", logx.Redact(err.Error()))
	}
	current.Accelerator = s.Accelerator
	if err := envlock.Compare(current, lock); err != nil {
		return current, exitCausef(1, err, "[colab] Environment mismatch: %s", err.Error())
	}
	reqs := envlock.RequirementsBytes(lock)
	if len(reqs) > 0 {
		client, err := d.ContentsFor(s)
		if err != nil {
			return current, err
		}
		// A fixed remote name keeps user input out of VM-side paths. The
		// requirement bodies are JSON-encoded by buildInstallCode, never
		// spliced into generated Python source.
		if err := client.WriteStream(ctx, lockRequirementsRemote, bytes.NewReader(reqs), contents.DefaultChunkSize); err != nil {
			return current, exitf(1, "[colab] Could not upload lockfile requirements: %s", logx.Redact(err.Error()))
		}
		code := buildInstallCode([]string{"-r", "/" + lockRequirementsRemote})
		res, err := executeTracked(ctx, d, s, k, code, runtime.ExecOptions{IdleTimeout: 5 * time.Minute})
		if err != nil {
			return current, err
		}
		if c := exitCodeFromOutputs(res.Outputs); c != 0 {
			return current, lockInstallFailure(c, res.Outputs)
		}
	}
	fmt.Fprintf(d.Stderr, "[colab] Environment matches lockfile; installed %d requirement(s).\n", len(lock.Requirements))
	return current, nil
}

func lockInstallFailure(code int, outs []runtime.Output) error {
	var detail string
	for _, o := range outs {
		if o.OutputType == "error" {
			detail = o.EName + ": " + o.EValue
			break
		}
	}
	if detail == "" {
		return exitf(code, "[colab] Lockfile installation failed.")
	}
	return exitf(code, "[colab] Lockfile installation failed: %s", logx.Redact(detail))
}

// recordRunBundle writes the provenance manifest for a completed run. It
// reuses a probe collected by --lockfile when available.
func recordRunBundle(ctx context.Context, d *app.Deps, s *state.Session, k runtime.Kernel, script string, code []byte, out string, probed *envlock.Lockfile) error {
	lock := probed
	if lock == nil {
		var err error
		lock, err = probeEnvironment(ctx, d, k, s.Accelerator)
		if err != nil {
			return err
		}
	}
	env := &bundle.Environment{
		PythonVersion: lock.Python,
		PipFreeze:     append([]string(nil), lock.Requirements...),
		CUDA:          lock.CUDA,
		GPU:           lock.GPU,
		Accelerator:   lock.Accelerator,
	}
	m, err := bundle.NewRun(bundle.Input{
		Files: []bundle.File{{Path: filepath.Base(script), Content: code}},
		Env: map[string]string{
			"python_version": lock.Python,
			"pip_freeze":     strings.Join(lock.Requirements, "\n"),
			"cuda":           lock.CUDA,
			"gpu":            lock.GPU,
		},
		Accelerator:     s.Accelerator,
		Variant:         s.Variant,
		Shape:           s.MachineShape,
		UpstreamVersion: version.Upstream,
		GolabVersion:    version.Number,
		Environment:     env,
	})
	if err != nil {
		return err
	}
	if err := bundle.Write(ctx, out, m); err != nil {
		return err
	}
	fmt.Fprintf(d.Stderr, "[colab] Recorded run bundle %s -> %s (recorded provenance, not a replay proof).\n",
		m.ID, filepath.Base(out))
	return nil
}

func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// runtimeOptionsFromManifest rebuilds allocation options from recorded
// hardware. A malformed value fails before any billable assignment.
func runtimeOptionsFromManifest(m *bundle.Manifest) (runtimeOptions, error) {
	accel := strings.ToUpper(m.Accelerator)
	gpu, tpu := "", ""
	switch {
	case accel == "" || accel == "NONE":
	case containsFold(tpuAccelerators, accel):
		tpu = accel
	case containsFold(gpuAccelerators, accel):
		gpu = accel
	default:
		return runtimeOptions{}, usagef("[colab] bundle: unsupported recorded accelerator %q", m.Accelerator)
	}
	highMem := strings.EqualFold(m.Shape, "HIGH_RAM") || strings.EqualFold(m.Shape, "High-RAM")
	return resolveRuntimeOptions(gpu, tpu, highMem)
}

// verifyBundleEnv rechecks the recorded environment on a genuinely fresh VM.
// The manifest is never rewritten: a match is reported here and only here.
func verifyBundleEnv(ctx context.Context, d *app.Deps, m *bundle.Manifest) error {
	if m.Environment == nil {
		return usagef("[colab] bundle verify --env: manifest has no recorded environment probe; use `golab run --bundle` to record one.")
	}
	opts, err := runtimeOptionsFromManifest(m)
	if err != nil {
		return err
	}
	name := randomName("bundle-verify-")
	fmt.Fprintf(d.Stderr, "[colab] Allocating a FRESH VM '%s' to re-check the recorded environment...\n", name)
	s, err := createSession(ctx, d, name, opts)
	if err != nil {
		return mapAssignFailure(err, opts)
	}
	kernel, err := d.RuntimeFor(ctx, &s)
	if err != nil {
		teardown(context.Background(), d, name, s, "bundle_verify_failed")
		return pruneLost(d, name, err)
	}
	current, probeErr := probeEnvironment(ctx, d, kernel, s.Accelerator)
	_ = kernel.Close()
	if probeErr != nil {
		teardown(context.Background(), d, name, s, "bundle_verify_failed")
		return probeErr
	}
	locked := &envlock.Lockfile{
		Schema:       envlock.Schema,
		Python:       m.Environment.PythonVersion,
		Requirements: append([]string(nil), m.Environment.PipFreeze...),
		CUDA:         m.Environment.CUDA,
		GPU:          m.Environment.GPU,
		Accelerator:  m.Environment.Accelerator,
	}
	if err := envlock.Compare(current, locked); err != nil {
		teardown(context.Background(), d, name, s, "bundle_verify_failed")
		return exitCausef(1, err, "[colab] Environment mismatch in fresh VM: %s", err.Error())
	}
	teardown(context.Background(), d, name, s, "bundle_verified")
	fmt.Fprintf(d.Stdout, "Bundle %s verified: environment matched in a FRESH VM. Manifest stays recorded provenance, not a verified replay.\n", m.ID)
	return nil
}
