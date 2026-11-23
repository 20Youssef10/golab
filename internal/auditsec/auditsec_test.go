package auditsec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/update"
)

const plantedToken = "super-secret-planted-token-1234567890"

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	sub := filepath.Join(dir, "config")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{
		ConfigDir:       sub,
		SessionsPath:    filepath.Join(sub, "sessions.json"),
		SettingsPath:    filepath.Join(sub, "settings.json"),
		LegacyTokenPath: filepath.Join(sub, "token.json"),
		OAuthTokenPath:  filepath.Join(sub, "oauth_token.json"),
		HistoryDir:      filepath.Join(sub, "history"),
		AuthProvider:    "oauth2",
		Owner:           "golab-cli",
		Repo:            "golab",
	}, dir
}

// TestAuditDetectsSeededDefects checks the audit's core promise: world-readable
// state, a 0644 token file, an invalid session name, a legacy token file and a
// sensitive key in state must all produce findings, and no secret value may
// appear anywhere in the report.
func TestAuditDetectsSeededDefects(t *testing.T) {
	cfg, _ := fixture(t)
	if err := os.Chmod(cfg.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg.SessionsPath, `{"bad name":{"name":"bad name","token":"`+plantedToken+`","url":"","client_secret":"leaked"}}`+"\n", 0o644)
	writeFile(t, cfg.SettingsPath, `{"enable_update_check":true}`, 0o644)
	writeFile(t, cfg.LegacyTokenPath, `{"refresh_token":"`+plantedToken+`"}`, 0o600)
	writeFile(t, cfg.OAuthTokenPath, `{"refresh_token":"`+plantedToken+`"}`, 0o644)

	rep, err := Audit(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	ids := map[string]bool{}
	for _, f := range rep.Findings {
		ids[f.ID] = true
	}
	for _, want := range []string{
		"config.dir.mode", "state.sessions.mode", "state.settings.mode",
		"state.session-name", "state.token-present", "state.sensitive-key",
		"auth.legacy-token", "auth.token-file.mode",
	} {
		if !ids[want] {
			t.Errorf("missing finding %q (got %v)", want, ids)
		}
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), plantedToken) || strings.Contains(string(b), "leaked") {
		t.Fatalf("audit report leaked a secret value: %s", b)
	}
}

// TestAuditRedactRegression plants both a bearer credential and a JSON token
// field in state and proves the report is clean after logx.Redact. This guards
// against a future change that echoes raw state bytes into a finding.
func TestAuditRedactRegression(t *testing.T) {
	cfg, _ := fixture(t)
	raw := `{"s1":{"name":"s1","token":"` + plantedToken + `","url":"https://x/?colab-runtime-proxy-token=abcdefghijklmnop"}}`
	writeFile(t, cfg.SessionsPath, raw, 0o600)
	if strings.Contains(logx.Redact(raw), plantedToken) {
		t.Fatal("test fixture is not a redaction case")
	}
	rep, err := Audit(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rep)
	for _, secret := range []string{plantedToken, "abcdefghijklmnop"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("report leaked %q: %s", secret, b)
		}
	}
}

// TestAuditUnknownForUnverifiableChecks follows A5: an unavailable backend is
// 'unknown', never a green 'pass'.
func TestAuditUnknownForUnverifiableChecks(t *testing.T) {
	cfg, _ := fixture(t)
	rep, err := Audit(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Update.Status != "unknown" {
		t.Errorf("update status = %q, want unknown", rep.Update.Status)
	}
	if rep.Capabilities.PublicAPI != "unknown" || rep.Capabilities.TunnelAPI != "unknown" {
		t.Errorf("capabilities = %+v, want unknown", rep.Capabilities)
	}
}

// TestAuditUpdateVerificationTypedErrors proves verification failures are
// identified by typed errors and surfaced with stable IDs and statuses.
func TestAuditUpdateVerificationTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		id   string
		want string
	}{
		{update.ErrMissingSBOM, "update.missing-sbom", "fail"},
		{update.ErrInvalidSignature, "update.invalid-signature", "fail"},
		{update.ErrIdentityMismatch, "update.identity-mismatch", "fail"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			cfg, _ := fixture(t)
			cfg.LastVerification = tc.err
			rep, err := Audit(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Update.Status != tc.want {
				t.Errorf("status = %q, want %q", rep.Update.Status, tc.want)
			}
			found := false
			for _, f := range rep.Findings {
				if f.ID == tc.id {
					found = true
				}
			}
			if !found {
				t.Errorf("missing finding %q: %+v", tc.id, rep.Findings)
			}
			if !errors.Is(tc.err, update.ErrMissingSBOM) && strings.Contains(string(mustJSON(t, rep)), "SBOM") {
				t.Errorf("unrelated finding mentions SBOM")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
