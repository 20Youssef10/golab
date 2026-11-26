package cli

import (
	"strings"
	"testing"
)

// `golab use NAME` is the user-facing writer for the default_session settings
// key consumed by Deps.ResolveSession.
func TestUseSetsDefaultSession(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "use", "my-session"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultSession != "my-session" {
		t.Fatalf("default_session = %q", settings.DefaultSession)
	}
	if code := h.run(t, "", "config", "get", "default_session"); code != 0 {
		t.Fatalf("config get exit=%d", code)
	}
	if got := strings.TrimSpace(h.stdout.String()); got != "my-session" {
		t.Fatalf("config get = %q", got)
	}
}

func TestUseClearRemovesDefault(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "use", "my-session"); code != 0 {
		t.Fatalf("set exit=%d stderr=%q", code, h.stderr.String())
	}
	if code := h.run(t, "", "use", "--clear"); code != 0 {
		t.Fatalf("clear exit=%d stderr=%q", code, h.stderr.String())
	}
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultSession != "" {
		t.Fatalf("default_session not cleared: %q", settings.DefaultSession)
	}
}

func TestUseRejectsInvalidName(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "use", "bad/name"); code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, h.stderr.String())
	}
}
