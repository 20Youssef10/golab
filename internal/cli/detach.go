package cli

// TIER 3 ADDITION (detached runs). This file is new; it wraps the existing
// `run` command with --detach/--ttl and adds `jobs` and `logs`. It deliberately
// does not edit run.go. ADR-001 records the decision and the no-daemon non-goal.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/detach"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

// Test seams with production defaults. Tests override these so run ids are
// deterministic and any local process launch is loud. Detached runs must never
// start a process locally: the child is started on the VM by the launcher cell.
var (
	detachNewID            = detach.NewID
	detachLocalLaunchGuard = func(spawnedLocal bool) {}
	detachNow              = func() time.Time { return time.Now().UTC() }
)

// runDetached implements `golab run --detach --ttl D SCRIPT`. Allocation and
// validation mirror `run`; the difference is that we submit ONE launcher cell,
// write a local manifest and keep the VM allocated. No local process, socket or
// daemon survives this function (ADR-001 non-goal: no local keep-alive).
func runDetached(cmd *cobra.Command, args []string, ttl time.Duration) error {
	d := depsOf(cmd)
	if ttl <= 0 {
		return usagef("[colab] run --detach requires --ttl DURATION (>0) so a forgotten job cannot bill forever (see ADR-001).")
	}
	// Invariant hook: a detached run NEVER spawns a local process. The child is
	// created on the VM by the launcher cell with start_new_session=True. Tests
	// replace this hook and fail if spawnedLocal is ever true.
	detachLocalLaunchGuard(false)

	script := args[0]
	// A notebook is not a plain Python script; embedding its JSON as source
	// would run nothing useful on the VM. Refuse it before any allocation.
	if strings.HasSuffix(strings.ToLower(script), ".ipynb") {
		return usagef("[colab] run --detach supports Python scripts only; use 'golab run' for notebooks.")
	}
	forwarded, inlineEnv := extractInlineEnv(args[1:])
	envVars, err := parseEnvVars(append(append([]string(nil), inlineEnv...), inlineEnvFromFlags(cmd)...))
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("session")
	gpu, _ := cmd.Flags().GetString("gpu")
	tpu, _ := cmd.Flags().GetString("tpu")
	highMem, _ := cmd.Flags().GetBool("high-mem")
	if err := validateSessionName(name); err != nil {
		return err
	}
	info, err := os.Stat(script)
	if err != nil || info.IsDir() {
		return usagef("[colab] Script not found: %s", script)
	}
	opts, err := resolveRuntimeOptions(gpu, tpu, highMem)
	if err != nil {
		return err
	}
	if name == "" {
		name = randomName("detach-")
	}
	// Read the script up front: an unreadable file must never reach allocation.
	body, err := os.ReadFile(script)
	if err != nil {
		return usagef("[colab] Could not read script '%s': %v", script, err)
	}
	id := detachNewID()
	if !detach.ValidID(id) {
		return fmt.Errorf("detach: generated unsafe run id %q", id)
	}
	if explainRequested(cmd) {
		fmt.Fprintln(d.Stdout, ExplainPlan(cmd, PlanFields{
			Operation: "run-detach", Method: "POST", API: apiLabel(d),
			Variant: string(opts.variant), Accelerator: string(opts.accelerator),
			Shape: opts.shape.String(), Session: name, Cleanup: "keep",
			CreatesSession: true, Billable: true,
		}))
		return nil
	}
	if err := guardBudget(cmd.Context(), d, cmd); err != nil {
		return err
	}

	started := detachNow()
	rec := detach.Record{
		ID: id, Session: name, Script: script, StartedAt: started, TTL: ttlString(ttl),
		// Fixed at submission: the remote watchdog enforces this absolute
		// deadline without any local timer or daemon (ADR-001).
		DeadlineAt: started.Add(ttl).UTC(),
	}
	launcher, err := detach.BuildLauncher(rec, body, forwarded, envMap(envVars))
	if err != nil {
		return err
	}

	fmt.Fprintf(d.Stderr, "[colab] Creating detached session '%s' (ttl=%s)...\n", name, ttl)
	ctx, stop := context.WithCancel(cmd.Context())
	defer stop()
	s, err := createSession(ctx, d, name, opts)
	if err != nil {
		return mapAssignFailure(err, opts)
	}
	kernel, err := d.RuntimeFor(ctx, &s)
	if err != nil {
		teardown(context.Background(), d, name, s, "detach_failed")
		return pruneLost(d, name, err)
	}
	// The launcher cell is short-lived: it starts the detached remote child and
	// returns. We DO wait for this cell (not the child) so a submission failure
	// is surfaced, then close the local socket. The VM stays allocated.
	_, err = kernel.Execute(ctx, launcher, runtime.ExecOptions{IdleTimeout: 30 * time.Second})
	closeErr := kernel.Close()
	if err != nil || closeErr != nil {
		teardown(context.Background(), d, name, s, "detach_failed")
		if err != nil {
			return err
		}
		return closeErr
	}

	// Write the local manifest AFTER the cell is accepted: a failed submission
	// must not leave a run id that points at nothing.
	if err := saveDetachRecord(d, rec); err != nil {
		return err
	}
	writeEvent(d, name, "detached_run_started", map[string]any{
		"run_id": id, "ttl": rec.TTL, "deadline": rec.DeadlineAt.Format(time.RFC3339),
	})
	fmt.Fprintf(d.Stdout, "%s\n", id)
	fmt.Fprintf(d.Stderr, "[colab] Detached run %s started. VM '%s' stays allocated until you stop it; deadline=%s enforced by a remote watchdog (VM must stay alive; see ADR-001).\n", id, name, rec.DeadlineAt.Format(time.RFC3339))
	fmt.Fprintf(d.Stderr, "[colab] Follow with: golab logs %s -f   |   golab jobs\n", id)
	return nil
}

// inlineEnvFromFlags reads --env entries declared on `run`. It is separate so
// detach can reuse run's flag surface without importing its closure variables.
func inlineEnvFromFlags(cmd *cobra.Command) []string {
	if cmd.Flags().Lookup("env") == nil {
		return nil
	}
	values, err := cmd.Flags().GetStringArray("env")
	if err != nil {
		return nil
	}
	return values
}

// ttlString renders a whole-unit duration compactly ("1h", "30m", "45s") for
// the manifest and diagnostics; sub-second values fall back to String().
func ttlString(d time.Duration) string {
	switch {
	case d <= 0:
		return d.String()
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	default:
		return d.String()
	}
}

func envMap(vars []envVar) map[string]string {
	out := make(map[string]string, len(vars))
	for _, v := range vars {
		out[v.key] = v.value
	}
	return out
}

func manifestFor(d *app.Deps) (detach.Manifest, string, error) {
	p := detach.ManifestPath(d.HistoryDir)
	m, err := detach.LoadManifest(p)
	return m, p, err
}

func saveDetachRecord(d *app.Deps, rec detach.Record) error {
	m, p, err := manifestFor(d)
	if err != nil {
		return err
	}
	m.Runs[rec.ID] = rec
	return detach.SaveManifest(p, m)
}

// jobsCommand polls the Contents API on demand for each locally indexed run.
// It starts nothing and holds nothing open (ADR-001: no local daemon).
func jobsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List detached runs with status, exit code and duration",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := depsOf(cmd)
			m, _, err := manifestFor(d)
			if err != nil {
				return exitf(1, "[colab] jobs: %s", logx.Redact(err.Error()))
			}
			if len(m.Runs) == 0 {
				fmt.Fprintln(d.Stderr, "[colab] No detached runs.")
				return nil
			}
			ids := make([]string, 0, len(m.Runs))
			for id := range m.Runs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				rec := m.Runs[id]
				status, exitCode, ttlState := "unknown", "-", "unknown"
				deadline, hasDeadline := rec.Deadline()
				reason := ""
				s, err := d.Store.Get(rec.Session)
				if err == nil {
					if client, cErr := d.ContentsFor(s); cErr == nil {
						metaBody, metaErr := client.Read(cmd.Context(), detach.RemoteFile(id, detach.MetaFile))
						exitBody, exitErr := client.Read(cmd.Context(), detach.RemoteFile(id, detach.ExitFile))
						reasonBody, reasonErr := client.Read(cmd.Context(), detach.RemoteFile(id, detach.TTLReasonFile))
						if reasonErr == nil {
							reason = strings.TrimSpace(string(reasonBody.Content))
						}
						// meta.json holds the deadline the remote watchdog
						// enforces; fall back to the local manifest if absent.
						if metaErr == nil && len(metaBody.Content) > 0 {
							var remote detach.Record
							if json.Unmarshal(metaBody.Content, &remote) == nil {
								if rd, ok := remote.Deadline(); ok {
									deadline, hasDeadline = rd, true
								}
							}
						}
						switch {
						case exitErr == nil:
							status = "exited"
							exitCode = strings.TrimSpace(string(exitBody.Content))
							if exitCode == "" {
								exitCode = "-"
							}
						case metaErr == nil:
							status = "running"
						}
					}
				} else if errors.Is(err, state.ErrNotFound) {
					status = "stale"
				}
				// A missing remote file is never a success: the reason marker
				// and the absolute deadline are the only TTL evidence we have.
				now := time.Now()
				switch {
				case reason == detach.TTLExceeded:
					status = "ttl_exceeded"
					ttlState = "exceeded"
				case hasDeadline && !deadline.After(now):
					ttlState = "exceeded"
				case hasDeadline:
					ttlState = "ok"
				}
				dur := time.Since(rec.StartedAt)
				if dur < 0 {
					dur = 0
				}
				fmt.Fprintf(d.Stdout, "%s\t%s\texit=%s\tttl=%s\tduration=%s\tsession=%s\tscript=%s\n",
					rec.ID, status, exitCode, ttlState, dur.Round(time.Second), rec.Session, rec.Script)
			}
			return nil
		},
	}
	return cmd
}

// logsCommand reads a run's remote log; -f follows with bounded polling.
func logsCommand() *cobra.Command {
	var follow bool
	var interval time.Duration
	var maxIterations int
	cmd := &cobra.Command{
		Use:   "logs ID",
		Short: "Show (and optionally follow) a detached run's log",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			id := args[0]
			m, _, err := manifestFor(d)
			if err != nil {
				return exitf(1, "[colab] logs: %s", logx.Redact(err.Error()))
			}
			rec, ok := m.Runs[id]
			if !ok {
				return exitf(1, "[colab] Unknown run id %q.", id)
			}
			s, err := d.Store.Get(rec.Session)
			if err != nil {
				return sessionErr(rec.Session, err)
			}
			client, err := d.ContentsFor(s)
			if err != nil {
				return err
			}
			logPath := detach.RemoteFile(id, detach.LogFile)
			exitPath := detach.RemoteFile(id, detach.ExitFile)
			readLog := func() []byte {
				model, rErr := client.Read(cmd.Context(), logPath)
				if rErr != nil {
					return nil
				}
				return model.Content
			}
			printed := 0
			content := readLog()
			if len(content) > 0 {
				fmt.Fprint(d.Stdout, string(content))
				printed = len(content)
			}
			if !follow {
				return nil
			}
			if interval <= 0 {
				interval = time.Second
			}
			if maxIterations <= 0 {
				maxIterations = 30
			}
			for i := 0; i < maxIterations; i++ {
				timer := time.NewTimer(interval)
				select {
				case <-cmd.Context().Done():
					timer.Stop()
					return cmd.Context().Err()
				case <-timer.C:
				}
				content = readLog()
				if len(content) > printed {
					fmt.Fprint(d.Stdout, string(content[printed:]))
					printed = len(content)
				}
				if _, eErr := client.Read(cmd.Context(), exitPath); eErr == nil {
					content = readLog()
					if len(content) > printed {
						fmt.Fprint(d.Stdout, string(content[printed:]))
					}
					return nil
				}
			}
			return nil // bounded: never tail forever
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the log with bounded polling")
	cmd.Flags().DurationVar(&interval, "interval", time.Second, "Poll interval when following")
	cmd.Flags().IntVar(&maxIterations, "max", 30, "Maximum follow iterations (bounded)")
	return cmd
}
