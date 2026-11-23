package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/doctor"
	"github.com/20Youssef10/golab/internal/logx"
)

// NewDoctorCommand builds the GET-only connectivity probe. It is registered
// separately (see RegisterDoctorCommands) so the root owner controls when the
// new command family appears in the command tree.
func NewDoctorCommand() *cobra.Command {
	var live, asJSON bool
	var section, fixtures string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe Colab connectivity with GET-only, redacting checks",
		Long: "Doctor reports per-plane reachability without allocating a VM. " +
			"Live probes are opt-in (--live plus GOLAB_LIVE_PROBES=1) and are disabled " +
			"when LIVE_E2E_ENABLED=false. It is not a TLS/MITM capture tool.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if section != "" && !validDoctorSection(section) {
				return usagef("[colab] doctor: unknown --section %q (config, auth, api, update, wire)", section)
			}
			d := depsOf(cmd)
			if live && !doctor.LiveEnabled(os.Getenv) {
				return usagef("[colab] doctor: %v", doctor.ErrLiveDisabled)
			}
			// Auth must not be touched in offline mode: token refresh is a
			// network operation and the default probe promises zero requests.
			var client doctor.Client
			if live {
				c, err := d.ColabClient(cmd.Context())
				if err != nil {
					return err
				}
				client = c
			}
			rep, err := doctor.Probe(cmd.Context(), doctor.Config{
				Live: live, Timeout: timeout, FixtureDir: fixtures,
			}, client)
			if err != nil {
				if errors.Is(err, doctor.ErrLiveDisabled) {
					return usagef("[colab] doctor: kill switch: %v", err)
				}
				return exitf(1, "[colab] doctor: %s", logx.Redact(err.Error()))
			}
			rep.Checks = filterDoctorChecks(rep.Checks, section)
			if asJSON {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
			} else {
				for _, c := range rep.Checks {
					fmt.Fprintf(d.Stdout, "[doctor] %s: %s — %s\n", c.Name, c.Status, c.Detail)
					if c.Remediation != "" && c.Status != "pass" {
						fmt.Fprintf(d.Stdout, "         -> %s\n", c.Remediation)
					}
				}
			}
			for _, c := range rep.Checks {
				if c.Status == "fail" {
					return exitf(1, "")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&live, "live", false, "Perform GET-only API probes (requires GOLAB_LIVE_PROBES=1)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the report as a single JSON document")
	cmd.Flags().StringVar(&section, "section", "", "Only report one section: config, auth, api, update, wire")
	cmd.Flags().StringVar(&fixtures, "fixtures", "", "Directory of captured wire fixtures to inventory")
	cmd.Flags().DurationVar(&timeout, "timeout", doctor.DefaultTimeout, "Hard deadline for the whole live probe (0 uses the 10s default)")
	return cmd
}

// RegisterDoctorCommands adds `doctor` to the root command tree.
func RegisterDoctorCommands(root *cobra.Command) { root.AddCommand(NewDoctorCommand()) }

func validDoctorSection(s string) bool {
	switch s {
	case "config", "auth", "api", "update", "wire":
		return true
	default:
		return false
	}
}

func filterDoctorChecks(checks []doctor.Check, section string) []doctor.Check {
	if section == "" {
		return checks
	}
	var out []doctor.Check
	for _, c := range checks {
		matched := false
		switch section {
		case "config":
			matched = c.Name == "environment"
		case "auth":
			matched = strings.HasPrefix(c.Name, "auth.")
		case "update":
			matched = strings.HasPrefix(c.Name, "update.")
		case "wire":
			matched = strings.HasPrefix(c.Name, "wire.")
		case "api":
			matched = c.Name == "control-plane.list" || strings.HasPrefix(c.Name, "api.")
		}
		if matched {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []doctor.Check{}
	}
	return out
}
