package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// Ports upstream tests/test_config.py: config get/set/unset/list/path must
// round-trip through the locked SettingsStore and never hand-edit JSON.
func TestConfigCRUD(t *testing.T) {
	h := newHarness(t)

	if code := h.run(t, "", "config", "set", "use_public_api", "true"); code != 0 {
		t.Fatalf("set exit=%d stderr=%q", code, h.stderr.String())
	}
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.UsePublicAPI {
		t.Fatalf("use_public_api not persisted: %+v", settings)
	}

	if code := h.run(t, "", "config", "get", "use_public_api"); code != 0 {
		t.Fatalf("get exit=%d stderr=%q", code, h.stderr.String())
	}
	if got := strings.TrimSpace(h.stdout.String()); got != "true" {
		t.Fatalf("get = %q, want true", got)
	}

	if code := h.run(t, "", "config", "set", "enable_update_check", "false"); code != 0 {
		t.Fatalf("set exit=%d stderr=%q", code, h.stderr.String())
	}
	if code := h.run(t, "", "config", "unset", "enable_update_check"); code != 0 {
		t.Fatalf("unset exit=%d stderr=%q", code, h.stderr.String())
	}
	settings, err = h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	// unset must restore the Python default (true), not merely store false.
	if !settings.EnableUpdateCheck {
		t.Fatalf("unset did not restore default: %+v", settings)
	}

	if code := h.run(t, "", "config", "list"); code != 0 {
		t.Fatalf("list exit=%d stderr=%q", code, h.stderr.String())
	}
	for _, key := range []string{"use_public_api", "enable_update_check", "update_url", "public_api_supported", "tunnel_api_supported", "default_session"} {
		if !strings.Contains(h.stdout.String(), key) {
			t.Fatalf("list lacks %q: %q", key, h.stdout.String())
		}
	}

	if code := h.run(t, "", "config", "path"); code != 0 {
		t.Fatalf("path exit=%d stderr=%q", code, h.stderr.String())
	}
	want := filepath.Join(h.dir, "settings.json")
	if got := strings.TrimSpace(h.stdout.String()); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// Config values are free-form; a token-shaped value in update_url must never
// reach stdout from `get` or `list`.
func TestConfigPrintRedactsSecretMaterial(t *testing.T) {
	h := newHarness(t)
	secret := "supersecrettokenvalue"
	if code := h.run(t, "", "config", "set", "update_url", "https://example.invalid/json?token="+secret); code != 0 {
		t.Fatalf("set exit=%d stderr=%q", code, h.stderr.String())
	}
	for _, args := range [][]string{
		{"config", "get", "update_url"},
		{"config", "list"},
	} {
		if code := h.run(t, "", args...); code != 0 {
			t.Fatalf("%v exit=%d stderr=%q", args, code, h.stderr.String())
		}
		if strings.Contains(h.stdout.String(), secret) {
			t.Fatalf("%v leaked secret: %q", args, h.stdout.String())
		}
		if !strings.Contains(h.stdout.String(), "token=REDACTED") {
			t.Fatalf("%v did not redact: %q", args, h.stdout.String())
		}
	}
}

func TestConfigProbesTriState(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "config", "get", "public_api_supported"); code != 0 {
		t.Fatalf("get exit=%d stderr=%q", code, h.stderr.String())
	}
	if got := strings.TrimSpace(h.stdout.String()); got != "unset" {
		t.Fatalf("unset probe = %q", got)
	}
	if code := h.run(t, "", "config", "set", "public_api_supported", "true"); code != 0 {
		t.Fatalf("set exit=%d stderr=%q", code, h.stderr.String())
	}
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.PublicAPISupported == nil || !*settings.PublicAPISupported {
		t.Fatalf("probe not set: %+v", settings)
	}
	if code := h.run(t, "", "config", "unset", "public_api_supported"); code != 0 {
		t.Fatalf("unset exit=%d stderr=%q", code, h.stderr.String())
	}
	settings, err = h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.PublicAPISupported != nil {
		t.Fatalf("probe not cleared: %+v", settings)
	}
}

func TestConfigRejectsUnknownKeyAndBadValues(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"config", "get", "not_a_key"},
		{"config", "set", "use_public_api", "maybe"},
		{"config", "set", "not_a_key", "1"},
	} {
		if code := h.run(t, "", args...); code != 2 {
			t.Fatalf("%v exit=%d, want 2; stderr=%q", args, code, h.stderr.String())
		}
	}
}
