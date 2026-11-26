package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

// Ports the Tier-5 ergonomics contract: explicit -s > GOLAB_SESSION >
// settings.json default_session > unique-session fallback. The first three
// levels are pure name resolution and need no network.
func TestConfiguredSessionNamePrecedence(t *testing.T) {
	dir := t.TempDir()
	settings, err := state.NewSettingsStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	def := state.DefaultSettings()
	def.DefaultSession = "from-settings"
	if err := settings.Save(def); err != nil {
		t.Fatal(err)
	}
	empty, err := state.NewSettingsStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		explicit string
		env      string
		store    *state.SettingsStore
		want     string
	}{
		{"explicit wins", "flag-session", "env-session", settings, "flag-session"},
		{"env beats settings", "", "env-session", settings, "env-session"},
		{"settings default", "", "", settings, "from-settings"},
		{"nothing configured", "", "", empty, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(SessionEnvVar, tc.env)
			got, err := ConfiguredSessionName(tc.explicit, tc.store)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

// The configured default must win even when more than one session exists, and
// must not print the unique-session notice.
func TestResolveSessionDefaultBeatsUniqueFallback(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	for _, name := range []string{"preferred", "other"} {
		a := assign(t, client)
		expiry := time.Now().Add(time.Hour)
		if err := d.Store.Put(state.Session{
			Name: name, Endpoint: a.Endpoint, URL: a.RuntimeProxyInfo.URL,
			Token: a.RuntimeProxyInfo.Token, TokenExpiresAt: &expiry,
		}); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := d.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.DefaultSession = "preferred"
	if err := d.SettingsStore.Save(settings); err != nil {
		t.Fatal(err)
	}

	got, err := d.ResolveSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "preferred" {
		t.Fatalf("resolved %q, want preferred", got.Name)
	}
	if strings.Contains(d.Stderr.(*bytes.Buffer).String(), "Using unique session") {
		t.Fatalf("unique fallback notice printed despite configured default: %q", d.Stderr.(*bytes.Buffer).String())
	}
}

func TestResolveSessionEnvBeatsSettingsDefault(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	for _, name := range []string{"env-session", "settings-session"} {
		a := assign(t, client)
		expiry := time.Now().Add(time.Hour)
		if err := d.Store.Put(state.Session{
			Name: name, Endpoint: a.Endpoint, URL: a.RuntimeProxyInfo.URL,
			Token: a.RuntimeProxyInfo.Token, TokenExpiresAt: &expiry,
		}); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := d.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.DefaultSession = "settings-session"
	if err := d.SettingsStore.Save(settings); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SessionEnvVar, "env-session")

	got, err := d.ResolveSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "env-session" {
		t.Fatalf("resolved %q, want env-session", got.Name)
	}
}

func TestResolveSessionExplicitBeatsEnvAndDefault(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	for _, name := range []string{"flag-session", "env-session", "settings-session"} {
		a := assign(t, client)
		expiry := time.Now().Add(time.Hour)
		if err := d.Store.Put(state.Session{
			Name: name, Endpoint: a.Endpoint, URL: a.RuntimeProxyInfo.URL,
			Token: a.RuntimeProxyInfo.Token, TokenExpiresAt: &expiry,
		}); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := d.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.DefaultSession = "settings-session"
	if err := d.SettingsStore.Save(settings); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SessionEnvVar, "env-session")

	got, err := d.ResolveSession(context.Background(), "flag-session")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "flag-session" {
		t.Fatalf("resolved %q, want flag-session", got.Name)
	}
}
