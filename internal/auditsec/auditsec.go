// Package auditsec produces a local-only, secret-free security report.
//
// It inspects resolved paths and file modes, the token-store tier, cached API
// capability flags and updater verification status. Findings never contain a
// credential value: state files are parsed with state.Store (whose tokens are
// logx.Secret values) and every free-form detail goes through logx.Redact.
package auditsec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	goruntime "runtime"
	"strings"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/update"
)

const schema = "golab/audit/1"

// Capabilities are the cached probe flags from settings.json. Nil means the
// value has never been probed and must be reported as unknown.
type Capabilities struct {
	PublicAPISupported *bool
	TunnelAPISupported *bool
}

// Config carries only resolved values and paths; it performs no I/O of its own.
type Config struct {
	ConfigDir        string
	SessionsPath     string
	SettingsPath     string
	LegacyTokenPath  string
	OAuthTokenPath   string
	HistoryDir       string
	AuthProvider     string
	Owner, Repo      string
	LastVerification error
	Capabilities     Capabilities
}

// Finding is one line of the human-readable report.
type Finding struct {
	Severity    string `json:"severity"` // info | warning | error
	ID          string `json:"id"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// TokenStore describes where credentials live. The OS keyring availability
// probe is deliberately not performed here, so its result is "unknown".
type TokenStore struct {
	Tier  string `json:"tier"`
	Probe string `json:"probe"`
}

// CapabilityInfo mirrors the cached flags without exposing settings values.
type CapabilityInfo struct {
	PublicAPI string `json:"public_api"`
	TunnelAPI string `json:"tunnel_api"`
}

// UpdateInfo is the last verification outcome from the settings cache.
type UpdateInfo struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// PathInfo is a resolved path and its mode (or "n/a" on Windows).
type PathInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// Report is the complete audit result with a stable field order.
type Report struct {
	Schema       string         `json:"schema"`
	TokenStore   TokenStore     `json:"token_store"`
	Paths        []PathInfo     `json:"paths"`
	Capabilities CapabilityInfo `json:"capabilities"`
	Update       UpdateInfo     `json:"update"`
	Findings     []Finding      `json:"findings"`
}

var sensitiveKeyRE = regexp.MustCompile(`(?i)"(client_secret|refresh_token|access_token|authorization|cookie|password|code_verifier)"\s*:`)

// Audit inspects the local configuration. It never contacts Google, never
// probes the keyring and never reads a token value into a finding.
func Audit(ctx context.Context, cfg Config) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	rep := Report{
		Schema:       schema,
		TokenStore:   tokenStoreInfo(cfg),
		Paths:        []PathInfo{},
		Capabilities: capabilityInfo(cfg.Capabilities),
		Update:       updateInfo(cfg),
		Findings:     []Finding{},
	}
	for _, p := range []struct{ name, path string }{
		{"config_dir", cfg.ConfigDir},
		{"sessions", cfg.SessionsPath},
		{"settings", cfg.SettingsPath},
		{"oauth_token", cfg.OAuthTokenPath},
		{"legacy_token", cfg.LegacyTokenPath},
		{"history", cfg.HistoryDir},
	} {
		if p.path == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		info, err := os.Stat(p.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			rep.Findings = append(rep.Findings, Finding{
				Severity: "warning", ID: "path.stat-failed",
				Detail:      fmt.Sprintf("cannot stat %s: %s", p.name, logx.Redact(err.Error())),
				Remediation: "check filesystem permissions",
			})
			continue
		}
		mode := modeString(info)
		rep.Paths = append(rep.Paths, PathInfo{Name: p.name, Path: p.path, Mode: mode})
		checkMode(&rep, p.name, info)
	}
	checkState(&rep, ctx, cfg)
	checkUpdate(&rep, cfg)
	checkCapabilities(&rep, cfg)
	return rep, nil
}

func modeString(info os.FileInfo) string {
	if goruntime.GOOS == "windows" {
		return "n/a (Windows ACLs)"
	}
	return fmt.Sprintf("%04o", info.Mode().Perm())
}

func checkMode(rep *Report, name string, info os.FileInfo) {
	if goruntime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0 {
		return
	}
	id := "path.mode"
	switch name {
	case "config_dir":
		id = "config.dir.mode"
	case "sessions":
		id = "state.sessions.mode"
	case "settings":
		id = "state.settings.mode"
	case "oauth_token":
		id = "auth.token-file.mode"
	case "legacy_token":
		id = "auth.legacy-token.mode"
	case "history":
		id = "history.dir.mode"
	}
	rep.Findings = append(rep.Findings, Finding{
		Severity: "warning", ID: id,
		Detail:      fmt.Sprintf("%s is readable/writable by group or others (mode %s)", name, modeString(info)),
		Remediation: "chmod 700 (directories) or chmod 600 (files)",
	})
}

func checkState(rep *Report, ctx context.Context, cfg Config) {
	if cfg.LegacyTokenPath != "" {
		if _, err := os.Stat(cfg.LegacyTokenPath); err == nil {
			rep.Findings = append(rep.Findings, Finding{
				Severity: "warning", ID: "auth.legacy-token",
				Detail:      "a legacy Python token.json is still present",
				Remediation: "remove it after confirming the Go token store has the refresh token",
			})
		}
	}
	if cfg.SessionsPath == "" {
		return
	}
	raw, err := os.ReadFile(cfg.SessionsPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		rep.Findings = append(rep.Findings, Finding{
			Severity: "warning", ID: "state.read-failed",
			Detail:      "cannot read sessions file: " + logx.Redact(err.Error()),
			Remediation: "check filesystem permissions",
		})
		return
	}
	if keys := sensitiveKeyRE.FindAllStringSubmatch(string(raw), -1); len(keys) > 0 {
		seen := map[string]bool{}
		var names []string
		for _, m := range keys {
			if !seen[strings.ToLower(m[1])] {
				seen[strings.ToLower(m[1])] = true
				names = append(names, strings.ToLower(m[1]))
			}
		}
		rep.Findings = append(rep.Findings, Finding{
			Severity: "warning", ID: "state.sensitive-key",
			Detail:      "sessions file contains credential-shaped key(s): " + strings.Join(names, ", "),
			Remediation: "move refresh tokens to the OS keyring or an owner-only token file",
		})
	}
	if err := ctx.Err(); err != nil {
		return
	}
	store, err := state.NewStore(cfg.SessionsPath)
	if err != nil {
		return
	}
	sessions, err := store.List()
	if err != nil {
		rep.Findings = append(rep.Findings, Finding{
			Severity: "error", ID: "state.corrupt",
			Detail:      logx.Redact(err.Error()),
			Remediation: "restore sessions.json from its .bak file",
		})
		return
	}
	for name, s := range sessions {
		if err := state.ValidateName(name); err != nil {
			rep.Findings = append(rep.Findings, Finding{
				Severity: "error", ID: "state.session-name",
				Detail:      logx.Redact(err.Error()),
				Remediation: "rename the session with golab sessions",
			})
		}
		if s.Token.Reveal() != "" {
			rep.Findings = append(rep.Findings, Finding{
				Severity: "info", ID: "state.token-present",
				Detail: fmt.Sprintf("session %q stores a runtime proxy token (value redacted)", name),
			})
		}
	}
}

func tokenStoreInfo(cfg Config) TokenStore {
	switch strings.ToLower(strings.TrimSpace(cfg.AuthProvider)) {
	case "adc":
		return TokenStore{Tier: "none (Application Default Credentials)", Probe: "not-applicable"}
	case "oauth2":
		if _, err := os.Stat(cfg.OAuthTokenPath); err == nil {
			return TokenStore{Tier: "owner-only file (oauth_token.json)", Probe: "unknown (keyring not probed)"}
		}
		return TokenStore{Tier: "keyring with owner-only file fallback", Probe: "unknown (keyring not probed)"}
	default:
		return TokenStore{Tier: "unknown", Probe: "unknown"}
	}
}

func capabilityInfo(c Capabilities) CapabilityInfo {
	return CapabilityInfo{
		PublicAPI: boolLabel(c.PublicAPISupported),
		TunnelAPI: boolLabel(c.TunnelAPISupported),
	}
}

func boolLabel(v *bool) string {
	if v == nil {
		return "unknown"
	}
	if *v {
		return "supported"
	}
	return "unsupported"
}

func updateInfo(cfg Config) UpdateInfo {
	if cfg.LastVerification == nil {
		return UpdateInfo{Status: "unknown", Detail: "no cached verification result"}
	}
	for _, tc := range []struct {
		err  error
		id   string
		text string
	}{
		{update.ErrMissingSBOM, "update.missing-sbom", "required signed SBOM attestation is missing"},
		{update.ErrInvalidSignature, "update.invalid-signature", "signature verification failed"},
		{update.ErrIdentityMismatch, "update.identity-mismatch", "signer identity does not match the pinned identity"},
		{update.ErrChecksumMismatch, "update.checksum-mismatch", "release checksum does not match"},
		{update.ErrTrustedRoot, "update.trusted-root", "sigstore trusted root unavailable"},
	} {
		if errors.Is(cfg.LastVerification, tc.err) {
			return UpdateInfo{Status: "fail", Detail: tc.id + ": " + tc.text}
		}
	}
	return UpdateInfo{Status: "unknown", Detail: "unrecognised verification error: " + logx.Redact(cfg.LastVerification.Error())}
}

func checkUpdate(rep *Report, cfg Config) {
	if rep.Update.Status == "fail" {
		id := "update.verification-failed"
		if i := strings.IndexByte(rep.Update.Detail, ':'); i > 0 {
			id = rep.Update.Detail[:i]
		}
		rep.Findings = append(rep.Findings, Finding{
			Severity: "error", ID: id, Detail: rep.Update.Detail,
			Remediation: "do not install this release; re-run golab update after the publisher fixes it",
		})
	}
	if strings.TrimSpace(cfg.Owner) == "" || strings.TrimSpace(cfg.Repo) == "" {
		rep.Findings = append(rep.Findings, Finding{
			Severity: "warning", ID: "update.unpinned-source",
			Detail:      "update owner/repo is not pinned",
			Remediation: "use a pinned owner and repository for self-update",
		})
	}
}

func checkCapabilities(rep *Report, cfg Config) {
	if cfg.Capabilities.PublicAPISupported == nil {
		rep.Findings = append(rep.Findings, Finding{
			Severity: "info", ID: "capability.public-unknown",
			Detail:      "Public API support has not been probed",
			Remediation: "run doctor --live or golab test-public-api",
		})
	}
	if cfg.Capabilities.TunnelAPISupported == nil {
		rep.Findings = append(rep.Findings, Finding{
			Severity: "info", ID: "capability.tunnel-unknown",
			Detail:      "Tunnel API support has not been probed",
			Remediation: "run doctor --live or golab test-public-api",
		})
	}
}

// Marshal is a small convenience for callers that need a stable encoding.
func (r Report) Marshal() ([]byte, error) { return json.Marshal(r) }
