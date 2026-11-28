package cli

// attach is deliberately a local-state operation: the only control-plane call
// is the read-only List. No POST/assign or Public API create is ever issued,
// so attaching a browser-created runtime cannot allocate a billable VM.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
)

// attachDefaultName derives a valid local session name from a Public API
// runtime name ("runtimes/r-…") or a tunnel endpoint. It keeps the runtime id
// suffix when possible, replaces characters outside state.ValidateName's
// alphabet and truncates to the 64-byte maximum. The fallback stem keeps empty
// and Windows-reserved results valid too.
func attachDefaultName(runtime string) string {
	base := runtime
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.TrimLeft(b.String(), "._-")
	if name == "" {
		return "r-attached"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	if state.ValidateName(name) == nil {
		return name
	}
	// A Windows reserved device name (e.g. CON) is the only remaining
	// failure; a fixed stem makes it addressable without changing the id.
	name = "r-" + name
	if len(name) > 64 {
		name = name[:64]
	}
	if state.ValidateName(name) == nil {
		return name
	}
	return "r-attached"
}

// findAttachRuntime resolves a full resource name first, then a bare id or
// endpoint (the values `golab sessions` prints).
func findAttachRuntime(want string, assignments []colabapi.ListedAssignment) (colabapi.ListedAssignment, bool) {
	want = strings.TrimSpace(want)
	if want == "" {
		return colabapi.ListedAssignment{}, false
	}
	for _, a := range assignments {
		if a.RuntimeName == want {
			return a, true
		}
	}
	for _, a := range assignments {
		id := strings.TrimPrefix(a.RuntimeName, "runtimes/")
		if id == want || a.Endpoint == want {
			return a, true
		}
	}
	return colabapi.ListedAssignment{}, false
}

// planeLabel names the control plane in diagnostics. A failure to list must
// say which plane was tried instead of reporting a fake empty success.
func planeLabel(d *app.Deps) string {
	if d.Config.UsePublicAPI != nil && *d.Config.UsePublicAPI {
		return "Public API"
	}
	return "legacy tunnel"
}

func runtimeLabel(a colabapi.ListedAssignment) string {
	if a.RuntimeName != "" {
		return a.RuntimeName
	}
	return a.Endpoint
}

func findAssignmentForSession(s state.Session, assignments []colabapi.ListedAssignment) (colabapi.ListedAssignment, bool) {
	for _, a := range assignments {
		if state.MatchAssignment(s, a.Endpoint, a.RuntimeName) {
			return a, true
		}
	}
	return colabapi.ListedAssignment{}, false
}

func assignmentsMatch(a, b colabapi.ListedAssignment) bool {
	return (a.Endpoint != "" && a.Endpoint == b.Endpoint) ||
		(a.RuntimeName != "" && a.RuntimeName == b.RuntimeName)
}

// sessionFromAssignment converts one listed runtime into a local record. The
// proxy token is a logx.Secret, so it cannot leak through formatting.
func sessionFromAssignment(a colabapi.ListedAssignment, name string) state.Session {
	expiry := a.RuntimeProxyInfo.ExpiresAt(nowUTC())
	return state.Session{
		Name:           name,
		Token:          a.RuntimeProxyInfo.Token,
		URL:            a.RuntimeProxyInfo.URL,
		Endpoint:       a.Endpoint,
		RuntimeName:    a.RuntimeName,
		TokenExpiresAt: &expiry,
		Variant:        a.Variant.String(),
		Accelerator:    string(a.Accelerator),
		MachineShape:   a.MachineShape.String(),
	}
}

// uniqueAttachName avoids overwriting a local session with the same derived
// name when two different runtimes share a name prefix.
func uniqueAttachName(local map[string]state.Session, base string) string {
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, exists := local[candidate]; !exists {
			return candidate
		}
		tail := "-" + strconv.Itoa(suffix)
		stem := base
		if len(stem)+len(tail) > 64 {
			stem = stem[:64-len(tail)]
		}
		candidate = stem + tail
	}
}

func attachOne(d *app.Deps, a colabapi.ListedAssignment, name string) error {
	s := sessionFromAssignment(a, name)
	if err := d.Store.Put(s); err != nil {
		return err
	}
	fmt.Fprintf(d.Stderr, "[colab] Attached session '%s' to runtime %s.\n", name, logx.Redact(runtimeLabel(a)))
	return nil
}

// attachAll imports every listed runtime not already represented locally.
// Matching is by endpoint or Public API runtime name, so a session attached
// earlier under any local name is never duplicated.
func attachAll(cmd *cobra.Command, d *app.Deps, assignments []colabapi.ListedAssignment) error {
	local, err := d.Store.List()
	if err != nil {
		return err
	}
	imported := 0
	for _, a := range assignments {
		known := false
		for _, s := range local {
			if state.MatchAssignment(s, a.Endpoint, a.RuntimeName) {
				known = true
				break
			}
		}
		if known {
			continue
		}
		base := a.RuntimeName
		if base == "" {
			base = a.Endpoint
		}
		name := uniqueAttachName(local, attachDefaultName(base))
		if err := attachOne(d, a, name); err != nil {
			return err
		}
		local[name] = sessionFromAssignment(a, name)
		imported++
	}
	if imported == 0 {
		fmt.Fprintf(d.Stderr, "[colab] No Web UI runtimes found on the %s plane that are not already attached.\n", planeLabel(d))
		return nil
	}
	fmt.Fprintf(d.Stderr, "[colab] Attached %d Web UI runtime(s) from the %s plane.\n", imported, planeLabel(d))
	return nil
}

// attachRefresh re-reads a fresh List and updates only transport credentials,
// hardware labels and expiry. Kernel/session ids and TTL metadata are kept.
func attachRefresh(cmd *cobra.Command, d *app.Deps, assignments []colabapi.ListedAssignment, want string) error {
	local, err := d.Store.List()
	if err != nil {
		return err
	}
	var target *colabapi.ListedAssignment
	if want != "" {
		a, ok := findAttachRuntime(want, assignments)
		if !ok {
			return usagef("[colab] Runtime %q not found. Run `golab sessions --web` to list active runtimes.", want)
		}
		target = &a
	}
	if len(local) == 0 {
		fmt.Fprintln(d.Stderr, "[colab] No attached sessions to refresh.")
		return nil
	}
	updated := 0
	missing := []string{}
	for name, s := range local {
		a, ok := findAssignmentForSession(s, assignments)
		if !ok {
			missing = append(missing, name)
			continue
		}
		if target != nil && !assignmentsMatch(*target, a) {
			continue
		}
		fresh := sessionFromAssignment(a, name)
		s.Token = fresh.Token
		s.URL = fresh.URL
		s.Endpoint = fresh.Endpoint
		s.RuntimeName = fresh.RuntimeName
		s.TokenExpiresAt = fresh.TokenExpiresAt
		s.Variant = fresh.Variant
		s.Accelerator = fresh.Accelerator
		s.MachineShape = fresh.MachineShape
		if err := d.Store.Put(s); err != nil {
			return err
		}
		updated++
	}
	if target != nil && updated == 0 {
		return usagef("[colab] Runtime %q is not attached locally. Run `golab attach --all` first.", want)
	}
	if updated == 0 {
		fmt.Fprintf(d.Stderr, "[colab] No attached sessions matched runtimes on the %s plane.\n", planeLabel(d))
	} else {
		fmt.Fprintf(d.Stderr, "[colab] Refreshed %d attached session(s) from the %s plane.\n", updated, planeLabel(d))
	}
	for _, n := range missing {
		fmt.Fprintf(d.Stderr, "[colab] Session '%s' has no matching runtime on the %s plane; left unchanged.\n", n, planeLabel(d))
	}
	return nil
}

// attachCommand turns a runtime created in the browser into a local named
// session. It works on either control plane and never allocates.
func attachCommand() *cobra.Command {
	var runtime, name string
	var all, refresh bool
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Attach browser-created runtimes as local sessions",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			runtime = strings.TrimSpace(runtime)
			switch {
			case all && (runtime != "" || refresh):
				return usagef("[colab] attach: --all cannot be combined with --runtime or --refresh.")
			case refresh && name != "":
				return usagef("[colab] attach: --session is only valid with --runtime.")
			case !all && !refresh && runtime == "":
				return usagef("[colab] --runtime is required: pass the runtime name (runtimes/r-…) or its id/endpoint, or use --all/--refresh.")
			case name != "":
				if err := validateSessionName(name); err != nil {
					return err
				}
			}
			// The effective plane already reflects --public-api/--no-public-api
			// and COLAB_CLI_PUBLIC_API; List works on both planes.
			client, err := d.ColabClient(cmd.Context())
			if err != nil {
				return err
			}
			assignments, err := client.List(cmd.Context())
			if err != nil {
				return fmt.Errorf("list runtimes on the %s plane: %w", planeLabel(d), err)
			}
			if all {
				return attachAll(cmd, d, assignments)
			}
			if refresh {
				return attachRefresh(cmd, d, assignments, runtime)
			}
			a, ok := findAttachRuntime(runtime, assignments)
			if !ok {
				return usagef("[colab] Runtime %q not found. Run `golab sessions --web` to list active runtimes.", runtime)
			}
			if name == "" {
				source := runtimeLabel(a)
				name = attachDefaultName(source)
			}
			local, err := d.Store.List()
			if err != nil {
				return err
			}
			if _, exists := local[name]; exists {
				return usagef("[colab] Session %q already exists locally. Choose another --session name.", name)
			}
			return attachOne(d, a, name)
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "Runtime name (runtimes/r-…), id or endpoint on either plane")
	cmd.Flags().StringVarP(&name, "session", "s", "", "Local session name (default: runtime id)")
	cmd.Flags().BoolVar(&all, "all", false, "Import every discovered Web UI runtime not already attached")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Refresh proxy token, URL and expiry of attached runtimes")
	return cmd
}

func registerAttachCommand(root *cobra.Command) {
	root.AddCommand(attachCommand())
}
