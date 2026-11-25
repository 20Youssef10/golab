// Package doctor implements a provably read-only Colab connectivity probe.
//
// It exposes only the two GET-backed control-plane verbs (List and
// CheckAPIAccess) through a narrow interface, so a caller cannot allocate a
// billable VM through this package even by mistake. Live mode is additionally
// gated by GOLAB_LIVE_PROBES=1 and LIVE_E2E_ENABLED!=false. Reports contain
// counts and statuses only: no token, header or response body is ever copied
// into a check detail.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
)

var (
	// ErrLiveDisabled means the caller asked for live probes without both
	// opting in and leaving CI's LIVE_E2E_ENABLED kill switch alone.
	ErrLiveDisabled = errors.New("doctor: live probes blocked by kill switch (set GOLAB_LIVE_PROBES=1 and do not set LIVE_E2E_ENABLED=false)")
	// ErrNoClient means live mode was requested without a control-plane client.
	ErrNoClient = errors.New("doctor: live probe requires a control-plane client")
)

// DefaultTimeout is the hard per-probe deadline. colabapi's client default is
// 60 s; a diagnostic must never inherit an unbounded-looking wait.
const DefaultTimeout = 10 * time.Second

// Client is the read-only subset of colabapi.Client used by the probe. Both
// methods map to GET requests; Assign/Unassign are deliberately absent.
type Client interface {
	List(context.Context) ([]colabapi.ListedAssignment, error)
	CheckAPIAccess(context.Context) (publicOK, tunnelOK bool, err error)
}

// Config configures Probe. Getenv is injectable for the kill-switch tests.
type Config struct {
	Live       bool
	Timeout    time.Duration
	FixtureDir string
	Getenv     func(string) string
}

// Check is one stable, machine-readable result.
type Check struct {
	Name        string `json:"name"`
	Status      string `json:"status"` // pass | fail | skip
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// Report is the complete doctor result. Field order is part of the schema.
type Report struct {
	Schema string  `json:"schema"`
	Live   bool    `json:"live"`
	Checks []Check `json:"checks"`
}

const schema = "golab/doctor/1"

// LiveEnabled reports whether both live-probe kill switches allow network I/O.
func LiveEnabled(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	return getenv("GOLAB_LIVE_PROBES") == "1" &&
		!strings.EqualFold(strings.TrimSpace(getenv("LIVE_E2E_ENABLED")), "false")
}

// Probe gathers offline checks and, only when explicitly enabled, GET-only
// API checks. It never writes state and never calls a mutating endpoint.
func Probe(ctx context.Context, cfg Config, client Client) (Report, error) {
	if cfg.Live && !LiveEnabled(cfg.Getenv) {
		return Report{}, ErrLiveDisabled
	}
	rep := Report{Schema: schema, Live: cfg.Live, Checks: []Check{}}
	rep.Checks = append(rep.Checks, environmentCheck())
	rep.Checks = append(rep.Checks, fixtureCheck(cfg.FixtureDir))

	// Authentication and update verification are audited locally by `audit`;
	// doctor stays read-only and secret-free, so it reports them as skipped.
	rep.Checks = append(rep.Checks, Check{
		Name: "auth.store", Status: "skip",
		Detail:      "credential storage is inspected by 'golab audit'",
		Remediation: "run 'golab audit' for token-store tier and file modes",
	})
	rep.Checks = append(rep.Checks, Check{
		Name: "update.verification", Status: "skip",
		Detail:      "update signature state is inspected by 'golab audit'",
		Remediation: "run 'golab audit' for updater verification status",
	})

	if !cfg.Live {
		for _, name := range []string{"control-plane.list", "api.public", "api.tunnel"} {
			rep.Checks = append(rep.Checks, Check{
				Name: name, Status: "skip", Detail: "offline mode: no requests made",
				Remediation: "re-run with --live and GOLAB_LIVE_PROBES=1 for a GET-only probe",
			})
		}
		return rep, nil
	}
	if client == nil {
		return rep, ErrNoClient
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	listed, err := client.List(probeCtx)
	check := Check{Name: "control-plane.list", Status: "pass"}
	if err != nil {
		check.Status = "fail"
		check.Detail = "unreachable: " + logx.Redact(err.Error())
		check.Remediation = "check credentials and network access to the Colab control plane"
	} else {
		check.Detail = fmt.Sprintf("%d assignment(s) visible; tokens not reported", len(listed))
	}
	rep.Checks = append(rep.Checks, check)
	if err := probeCtx.Err(); err != nil {
		return rep, err
	}

	publicOK, tunnelOK, accessErr := client.CheckAPIAccess(probeCtx)
	for _, item := range []struct {
		name string
		ok   bool
	}{
		{"api.public", publicOK}, {"api.tunnel", tunnelOK},
	} {
		check := Check{Name: item.name, Status: "fail", Detail: "not reachable"}
		if accessErr != nil {
			check.Detail = "probe failed: " + logx.Redact(accessErr.Error())
		} else if item.ok {
			check.Status = "pass"
			check.Detail = "GET probe succeeded"
		}
		if check.Status == "fail" {
			check.Remediation = "verify this API path with 'golab test-public-api'"
		}
		rep.Checks = append(rep.Checks, check)
	}
	if err := probeCtx.Err(); err != nil {
		return rep, err
	}
	return rep, nil
}

func environmentCheck() Check {
	detail := fmt.Sprintf("GOOS=%s GOARCH=%s termux=%t", goruntime.GOOS, goruntime.GOARCH, platform.IsTermux())
	return Check{Name: "environment", Status: "pass", Detail: detail}
}

func fixtureCheck(dir string) Check {
	if dir == "" {
		return Check{
			Name: "wire.fixtures", Status: "skip",
			Detail:      "no fixture directory configured; fakes are not live proof",
			Remediation: "point --fixtures at testdata/wire after a human capture",
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Check{
			Name: "wire.fixtures", Status: "fail",
			Detail:      "cannot list fixtures: " + logx.Redact(err.Error()),
			Remediation: "check --fixtures path",
		}
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			n++
		}
	}
	if n == 0 {
		// An empty inventory (e.g. testdata/wire holding only a README) gives
		// no differential proof. Use "skip" rather than "pass": the report
		// schema is pass|fail|skip, and the CLI fails only on "fail", so a
		// skipped check stays non-fatal while its remediation is still shown.
		return Check{
			Name: "wire.fixtures", Status: "skip",
			Detail:      "0 captured wire fixture(s); no differential proof available; fakes are not live proof",
			Remediation: "capture redacted wire fixtures from the official Python CLI (scripts/live-capture.sh, GET-only) before treating this section as verified",
		}
	}
	return Check{
		Name: "wire.fixtures", Status: "pass",
		Detail: fmt.Sprintf("%d captured wire fixture(s); fakes are not live proof", n),
	}
}
