package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
)

// attachDefaultName derives a valid local session name from a Public API
// runtime name ("runtimes/r-…"). It keeps the runtime id suffix when possible,
// replaces characters outside state.ValidateName's alphabet and truncates to
// the 64-byte maximum. The fallback stem keeps empty and Windows-reserved
// results valid too.
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

// attachCommand turns a runtime created in the browser into a local named
// session. It is a local-state operation: the only control-plane call is the
// read-only List, and no POST /assign (or Public API create) is ever issued.
func attachCommand() *cobra.Command {
	var runtime, name string
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Attach a browser-created runtime as a local session",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			// Never silently switch planes: the effective value already
			// reflects --public-api/--no-public-api and COLAB_CLI_PUBLIC_API.
			if d.Config.UsePublicAPI == nil || !*d.Config.UsePublicAPI {
				return usagef("[colab] attach requires the Colab Public API. " +
					"Re-run with --public-api (or set COLAB_CLI_PUBLIC_API=1).")
			}
			runtime = strings.TrimSpace(runtime)
			if runtime == "" {
				return usagef("[colab] --runtime is required: pass the Public API " +
					"runtime name (runtimes/r-…) or its id/endpoint.")
			}
			if err := validateSessionName(name); err != nil {
				return err
			}
			client, err := d.ColabClient(cmd.Context())
			if err != nil {
				return err
			}
			assignments, err := client.List(cmd.Context())
			if err != nil {
				return err
			}
			a, ok := findAttachRuntime(runtime, assignments)
			if !ok {
				return usagef("[colab] Runtime %q not found. Run `golab sessions` to list active runtimes.", runtime)
			}
			if name == "" {
				source := a.RuntimeName
				if source == "" {
					source = a.Endpoint
				}
				if source == "" {
					source = runtime
				}
				name = attachDefaultName(source)
			}
			local, err := d.Store.List()
			if err != nil {
				return err
			}
			if _, exists := local[name]; exists {
				return usagef("[colab] Session %q already exists locally. Choose another --session name.", name)
			}
			expiry := a.RuntimeProxyInfo.ExpiresAt(nowUTC())
			s := state.Session{
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
			if err := d.Store.Put(s); err != nil {
				return err
			}
			fmt.Fprintf(d.Stderr, "[colab] Attached session '%s' to runtime %s.\n",
				name, logx.Redact(a.RuntimeName))
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "Public API runtime name (runtimes/r-…) or its id/endpoint")
	cmd.Flags().StringVarP(&name, "session", "s", "", "Local session name (default: runtime id)")
	return cmd
}

func registerAttachCommand(root *cobra.Command) {
	root.AddCommand(attachCommand())
}
