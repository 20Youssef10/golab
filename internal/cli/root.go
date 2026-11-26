package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/agentout"
	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/platform"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/update"
)

type depsContextKey struct{}
type outputWriterKey struct{}
type updateJoinKey struct{}

// WithDeps injects a pre-built dependency graph. It is intended for tests and
// for embedding golab's CLI inside another program.
func WithDeps(ctx context.Context, d *app.Deps) context.Context {
	return context.WithValue(ctx, depsContextKey{}, d)
}

func depsFromContext(ctx context.Context) (*app.Deps, bool) {
	d, ok := ctx.Value(depsContextKey{}).(*app.Deps)
	return d, ok
}

// depsOf is called from RunE only after PersistentPreRunE has installed deps.
func depsOf(cmd *cobra.Command) *app.Deps {
	d, ok := depsFromContext(cmd.Context())
	if !ok || d == nil {
		panic("cli: dependency graph missing from command context")
	}
	return d
}

func nowUTC() time.Time { return time.Now().UTC() }

// Test seams. Production uses the real constructors; tests swap them so the
// auto-update notice's network traffic and dependency construction stay local.
var newAppDeps = app.New
var maybeCheckFn = update.MaybeCheck

type rootOptions struct {
	clientOAuthConfig string
	config            string
	logToStderr       bool
	authProvider      string
	scopes            string
	publicAPI         bool
	noPublicAPI       bool
	debug             bool
	outputFormat      string
	explain           bool
	assumeYes         bool
	budgetProfile     string
}

// NewRootCommand builds the command tree with the global flags of upstream's
// cli.py callback. All I/O goes through the supplied writers.
func NewRootCommand(in io.Reader, out, errOut io.Writer) *cobra.Command {
	opts := &rootOptions{
		clientOAuthConfig: defaultOAuthConfigPath(),
		authProvider:      envOr("COLAB_AUTH", string(auth.DefaultAuthProvider)),
		scopes:            envOr("COLAB_SCOPES", string(auth.DefaultScopeMode)),
		publicAPI:         parseBoolEnv(os.Getenv("COLAB_CLI_PUBLIC_API")),
		debug:             parseBoolEnv(os.Getenv("COLAB_DEBUG")),
		outputFormat:      envOr("COLAB_OUTPUT", "text"),
		assumeYes:         parseBoolEnv(os.Getenv("COLAB_ASSUME_YES")),
		budgetProfile:     envOr("GOLAB_BUDGET_PROFILE", "default"),
	}
	root := &cobra.Command{
		Use:           "golab",
		Short:         "Colab CLI",
		Long:          "golab — unofficial Go implementation of the Google Colab CLI",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Validate the scope mode even with injected deps so a bad --scopes
			// value is always a flag error, never a silent fallback to full.
			scopeMode, err := auth.ParseScopeMode(opts.scopes)
			if err != nil {
				return flagError(err)
			}
			d, injected := depsFromContext(cmd.Context())
			if !injected {
				provider, err := auth.ParseAuthProvider(opts.authProvider)
				if err != nil {
					return flagError(err)
				}
				usePublic := resolvePublicAPI(cmd, opts)
				cfg := app.Config{
					ClientOAuthConfigPath: opts.clientOAuthConfig,
					SessionsFile:          opts.config,
					LogToStderr:           opts.logToStderr,
					Debug:                 opts.debug,
					AuthProvider:          provider,
					Scopes:                scopeMode,
					UsePublicAPI:          &usePublic,
				}
				d, err = newAppDeps(cmd.Context(), cfg)
				if err != nil {
					return mapError(err)
				}
			}
			// Cobra's writers win so help output and command output share the
			// same streams, including in golden tests. In structured mode,
			// stdout is wrapped in the agent-safe event writer; diagnostics
			// stay on stderr.
			outWriter, err := agentout.NewWriter(cmd.OutOrStdout(), opts.outputFormat)
			if err != nil {
				return flagError(err)
			}
			d.SetStreams(cmd.InOrStdin(), outWriter, cmd.ErrOrStderr())
			ctx := context.WithValue(cmd.Context(), depsContextKey{}, d)
			if outWriter != cmd.OutOrStdout() {
				ctx = context.WithValue(ctx, outputWriterKey{}, outWriter)
			}
			// Start the throttled notice only after the command's writers are
			// installed, so its callback never sees a half-configured Deps.
			// Its join handle travels on the context: execute() waits on it
			// before returning so no banner can outlive the command.
			if !injected {
				if join := autoUpdateNotice(ctx, d, cmd); join != nil {
					ctx = context.WithValue(ctx, updateJoinKey{}, join)
				}
			}
			// RunE handlers read the dependency graph from the command context.
			// Without this, a real (non-injected) invocation panics. Store it on
			// the root too so execute() can finalise the structured writer.
			cmd.SetContext(ctx)
			cmd.Root().SetContext(ctx)
			// Tier 1 item 2: local-only session-expiry check on every
			// invocation. --explain is a no-network dry run, so skip it there.
			if !explainRequested(cmd) {
				checkExpiredSessions(ctx, d)
			}
			return nil
		},
	}
	// We provide our own `help` command (docs/05); hide cobra's built-in one
	// so `golab --help` does not list it twice.
	root.SetHelpCommand(&cobra.Command{Hidden: true})
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return flagError(err) })

	pf := root.PersistentFlags()
	pf.StringVarP(&opts.clientOAuthConfig, "client-oauth-config", "c", opts.clientOAuthConfig,
		"Path to client OAuth config JSON file")
	pf.StringVar(&opts.config, "config", "", "Path to session state file")
	pf.BoolVar(&opts.logToStderr, "logtostderr", false, "Log all output to stderr")
	pf.StringVar(&opts.authProvider, "auth", opts.authProvider,
		"Authentication strategy: oauth2 or adc")
	pf.StringVar(&opts.scopes, "scopes", opts.scopes,
		"OAuth scope set: full (default) or minimal (minimal cannot mount Drive)")
	pf.BoolVar(&opts.publicAPI, "public-api", opts.publicAPI,
		"Use the Colab Public API instead of the legacy tunnel API")
	pf.BoolVar(&opts.noPublicAPI, "no-public-api", !opts.publicAPI,
		"Use the legacy Colab tunnel API instead of the Public API")
	pf.StringVar(&opts.outputFormat, "output", opts.outputFormat,
		"Program output format: text, json or ndjson (diagnostics stay on stderr)")
	pf.BoolVar(&opts.explain, "explain", false,
		"Print a redacted dry-run plan for billable commands and exit")
	pf.BoolVar(&opts.assumeYes, "yes", opts.assumeYes,
		"Assume yes for non-destructive confirmations (never bypasses budget enforcement)")
	pf.StringVar(&opts.budgetProfile, "budget-profile", opts.budgetProfile,
		"Named golab.yaml budget profile for the guard (default: default; env GOLAB_BUDGET_PROFILE)")
	// Debug logging is enabled through COLAB_DEBUG (or --logtostderr); there is
	// deliberately no --debug flag in the upstream CLI surface.

	registerCommands(root)
	return root
}

// Execute runs the root command with the process's standard streams and maps
// errors to the exit-code contract (docs/01 §7).
func Execute(ctx context.Context, args []string) int {
	return execute(ctx, args, os.Stdin, os.Stdout, os.Stderr)
}

func execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	root := NewRootCommand(in, out, errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	// tier6 hunk: emit any accumulated WebSocket frame summary so a redacted
	// wire trace is complete when the command returns.
	if d, ok := depsFromContext(root.Context()); ok && d != nil && d.Trace != nil {
		_ = d.Trace.FlushWS()
	}
	// The notice runs asynchronously; join it before returning so its stderr
	// write completes while the command's writers are still alive.
	if join, ok := root.Context().Value(updateJoinKey{}).(func()); ok && join != nil {
		join()
	}
	if w, ok := root.Context().Value(outputWriterKey{}).(io.Writer); ok {
		if closeErr := agentout.Close(w); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if err == nil {
		return 0
	}
	mapped := mapError(err)
	var ee *exitError
	if !asExitError(mapped, &ee) {
		fmt.Fprintln(errOut, mapped)
		return 1
	}
	if ee.msg != "" {
		fmt.Fprintln(errOut, ee.msg)
	}
	return ee.code
}

func asExitError(err error, target **exitError) bool {
	return errors.As(err, target)
}

// autoUpdateNotice runs upstream's throttled release check off the critical
// path. It never writes to stdout and is skipped for injected test deps so a
// golden test can never make a network call. A --explain plan performs no
// billable work, so it must not make a network call either.
//
// The returned function joins the background check; nil means no asynchronous
// work was started.
func autoUpdateNotice(ctx context.Context, d *app.Deps, cmd *cobra.Command) func() {
	if explainRequested(cmd) {
		return nil
	}
	invoked := ""
	if cmd != nil {
		invoked = cmd.Name()
	}
	// A piped `ssh` (ProxyCommand or `-- CMD`) must keep stdout byte-exact and
	// must not pay for an unrelated network check.
	if suppressed(invoked) || (invoked == "ssh" && !platform.IsStdoutTTY()) || d.SettingsStore == nil {
		return nil
	}
	cfg, err := d.UpdateConfig(ctx)
	if err != nil || !cfg.Enabled {
		return nil
	}
	// Capture the writer before launching the goroutine so a later SetStreams
	// can never race with the callback's read of d.Stderr.
	stderr := d.Stderr
	return maybeCheckFn(ctx, cfg, func(rel *update.Release) {
		fmt.Fprintf(stderr, "[colab] Update available: %s. Run 'golab update --install' to upgrade.\n", rel.Version)
		settings, err := d.SettingsStore.Load()
		if err != nil {
			return
		}
		now := time.Now()
		settings.LastCheck = &now
		settings.LatestVersion = rel.Version
		_ = d.SettingsStore.Save(settings)
	})
}

func suppressed(command string) bool {
	switch command {
	case "update", "version", "log", "pay", "help", "url", "whoami",
		"test-public-api", "readme", "README", "skill", "SKILL",
		"doctor", "audit", "budget", "trace", "compat", "state":
		return true
	default:
		return false
	}
}

// resolvePublicAPI honours an explicit flag over the COLAB_CLI_PUBLIC_API
// environment default. --no-public-api and --public-api are separate pflag
// variables so binding one never overwrites the other's default.
func resolvePublicAPI(cmd *cobra.Command, opts *rootOptions) bool {
	usePublic := opts.publicAPI
	if cmd.Flags().Changed("no-public-api") {
		usePublic = false
	}
	if cmd.Flags().Changed("public-api") {
		usePublic = true
	}
	return usePublic
}

func defaultOAuthConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".colab-cli-oauth-config.json"
	}
	return home + string(os.PathSeparator) + ".colab-cli-oauth-config.json"
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseBoolEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// validateSessionName rejects invalid -s values before any network call.
func validateSessionName(name string) error {
	if name == "" {
		return nil
	}
	if err := state.ValidateName(name); err != nil {
		return usagef("[colab] %v", err)
	}
	return nil
}
