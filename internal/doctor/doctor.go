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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// Config configures Probe. Getenv is injectable for the kill-switch tests and
// also feeds the local-only prerequisite signals when Prerequisites.Getenv is
// unset. Prerequisites defaults to real, read-only local lookups.
type Config struct {
	Live          bool
	Timeout       time.Duration
	FixtureDir    string
	Getenv        func(string) string
	Prerequisites PrerequisiteConfig
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

// Public API prerequisites (issue #289). The new Public API can require a GCP
// quota project and a fresh OAuth consent. Everything here is local and
// read-only: golab reports the signals, it never creates or sets a quota
// project, changes billing, or refreshes consent on its own. No value read
// from the environment, an ADC file or gcloud is ever copied into a detail -
// only fixed source labels are reported, so credential material cannot leak.
const (
	quotaProjectEnv   = "GOOGLE_CLOUD_QUOTA_PROJECT"
	adcCredentialsEnv = "GOOGLE_APPLICATION_CREDENTIALS"
	scopesEnv         = "COLAB_SCOPES"
	authEnv           = "COLAB_AUTH"

	quotaSourcesChecked = "GOOGLE_CLOUD_QUOTA_PROJECT, ADC quota_project_id, gcloud config quota/project"
	quotaMissingDetail  = "no local GCP quota-project signal detected; the new Public API may require one"
	quotaMissingFix     = "have a maintainer create/set a quota project (GOOGLE_CLOUD_QUOTA_PROJECT, ADC quota_project_id, or gcloud config quota/project); golab only diagnoses and never mutates GCP"
)

// gcloudProbeTimeout bounds the optional `gcloud config get-value` probe so a
// diagnostic can never inherit a long/hanging CLI wait.
const gcloudProbeTimeout = 1200 * time.Millisecond

// GCloudRunner is the read-only command seam used by the quota-project probe.
// Callers pass the fixed args below; DefaultGCloudRunner deliberately ignores
// them and runs only `config get-value quota/project` so no mutation verb can
// reach gcloud even if a caller supplies one.
type GCloudRunner func(ctx context.Context, args ...string) (string, error)

// DefaultGCloudRunner performs the short-bounded, read-only gcloud config
// probe. A missing gcloud binary is a normal "no signal" outcome, not an error
// worth surfacing as a failed check.
func DefaultGCloudRunner(ctx context.Context, _ ...string) (string, error) {
	path, err := exec.LookPath("gcloud")
	if err != nil {
		return "", err
	}
	probeCtx, cancel := context.WithTimeout(ctx, gcloudProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, path, "config", "get-value", "quota/project").CombinedOutput()
	return string(out), err
}

// PrerequisiteConfig holds injectable, local-only inputs for the Public API
// prerequisite diagnosis. Nil functions get read-only production defaults.
type PrerequisiteConfig struct {
	Getenv       func(string) string
	ReadFile     func(string) ([]byte, error)
	UserHomeDir  func() (string, error)
	RunGCloud    GCloudRunner
	AuthProvider string // when empty, falls back to COLAB_AUTH then oauth2
	ScopeMode    string // when empty, falls back to COLAB_SCOPES then full
}

// Prerequisites is the local-only diagnosis. QuotaSource is a fixed label
// ("GOOGLE_CLOUD_QUOTA_PROJECT", "adc:quota_project_id",
// "gcloud:config/quota/project"); the observed value is never stored.
type Prerequisites struct {
	QuotaProjectDetected bool
	QuotaSource          string
	ReconsentHint        string
}

// InspectPrerequisites reads only local indicators: the quota-project env
// var, ADC JSON files, and (last) a short-bounded read-only gcloud config
// probe. It performs no network I/O and no GCP mutation.
func InspectPrerequisites(ctx context.Context, cfg PrerequisiteConfig) Prerequisites {
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	readFile := cfg.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	homeDir := cfg.UserHomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	runGCloud := cfg.RunGCloud
	if runGCloud == nil {
		runGCloud = DefaultGCloudRunner
	}

	var p Prerequisites

	if strings.TrimSpace(getenv(quotaProjectEnv)) != "" {
		p.QuotaProjectDetected = true
		p.QuotaSource = quotaProjectEnv
	}

	if !p.QuotaProjectDetected {
		var paths []string
		if cred := strings.TrimSpace(getenv(adcCredentialsEnv)); cred != "" {
			paths = append(paths, cred)
		}
		if home, err := homeDir(); err == nil && home != "" {
			paths = append(paths, filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"))
		}
		for _, path := range paths {
			data, err := readFile(path)
			if err != nil || len(data) == 0 {
				continue
			}
			// Decode only the one field we need. Any token, refresh token or
			// client secret in the same file is never read into a variable.
			var adc struct {
				QuotaProjectID string `json:"quota_project_id"`
			}
			if err := json.Unmarshal(data, &adc); err != nil {
				continue
			}
			if strings.TrimSpace(adc.QuotaProjectID) != "" {
				p.QuotaProjectDetected = true
				p.QuotaSource = "adc:quota_project_id"
				break
			}
		}
	}

	if !p.QuotaProjectDetected {
		out, err := runGCloud(ctx, "config", "get-value", "quota/project")
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				value := strings.TrimSpace(line)
				if value == "" || strings.EqualFold(value, "(unset)") {
					continue
				}
				// A project ID is one token; skip any multi-word warning line.
				if len(strings.Fields(value)) != 1 {
					continue
				}
				p.QuotaProjectDetected = true
				p.QuotaSource = "gcloud:config/quota/project"
				break
			}
		}
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.AuthProvider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(getenv(authEnv)))
	}
	if provider == "" {
		provider = "oauth2" // auth.DefaultAuthProvider
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ScopeMode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(getenv(scopesEnv)))
	}
	if provider == "oauth2" && mode == "minimal" {
		p.ReconsentHint = "OAuth2 scope mode is minimal (changed from the default full); re-consent may be required for the Public API — re-run 'golab auth' if calls return 401/403"
	}
	return p
}

// Notes renders the diagnosis as short, fixed-text diagnostic lines. Values
// are never interpolated; only source labels are included.
func (p Prerequisites) Notes() []string {
	var notes []string
	if p.QuotaProjectDetected {
		notes = append(notes, "Quota project signal detected via "+p.QuotaSource+" (value not printed).")
	} else {
		notes = append(notes, quotaMissingDetail+"; checked "+quotaSourcesChecked+". "+quotaMissingFix)
	}
	if p.ReconsentHint != "" {
		notes = append(notes, p.ReconsentHint)
	}
	return notes
}

// PrerequisiteChecks converts the diagnosis into stable, machine-readable
// report checks. Missing quota project is a non-fatal "skip" (it may be
// configured by a path golab cannot see); a detected signal is a "pass".
func PrerequisiteChecks(p Prerequisites) []Check {
	check := Check{Name: "api.quota-project", Status: "skip"}
	if p.QuotaProjectDetected {
		check.Status = "pass"
		check.Detail = "quota project signal detected via " + p.QuotaSource + "; value not printed"
	} else {
		check.Detail = quotaMissingDetail + "; checked " + quotaSourcesChecked
		check.Remediation = quotaMissingFix
	}
	checks := []Check{check}
	if p.ReconsentHint != "" {
		checks = append(checks, Check{Name: "api.reconsent", Status: "skip", Detail: p.ReconsentHint})
	}
	return checks
}

// Probe gathers offline checks and, only when explicitly enabled, GET-only
// API checks. It never writes state and never calls a mutating endpoint.
func Probe(ctx context.Context, cfg Config, client Client) (Report, error) {
	if cfg.Live && !LiveEnabled(cfg.Getenv) {
		return Report{}, ErrLiveDisabled
	}
	// Keep prerequisite inspection lazy so a hard API deadline (or an early
	// error) is never spent on the optional read-only gcloud probe.
	prereqCfg := cfg.Prerequisites
	if prereqCfg.Getenv == nil {
		prereqCfg.Getenv = cfg.Getenv
	}
	rep := Report{Schema: schema, Live: cfg.Live, Checks: []Check{}}
	appendPrerequisites := func() {
		rep.Checks = append(rep.Checks, PrerequisiteChecks(InspectPrerequisites(ctx, prereqCfg))...)
	}

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
		appendPrerequisites()
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
	appendPrerequisites()
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
