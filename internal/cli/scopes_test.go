package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/auth"
)

// scopeRecordingProvider captures the scopes handed to the control-plane
// provider and returns a harmless HTTP client (no network, no credentials).
type scopeRecordingProvider struct {
	calls  int
	scopes []string
}

func (p *scopeRecordingProvider) HTTPClient(_ context.Context, scopes []string) (*http.Client, error) {
	p.calls++
	p.scopes = append([]string(nil), scopes...)
	return &http.Client{}, nil
}

func depWithScopes(t *testing.T, mode auth.ScopeMode) (*app.Deps, *scopeRecordingProvider) {
	t.Helper()
	p := &scopeRecordingProvider{}
	usePublic := false
	d := &app.Deps{
		Config: app.Config{Scopes: mode, UsePublicAPI: &usePublic},
		Auth:   p,
	}
	return d, p
}

// Issue #71 (a): app.Config.Scopes must propagate the reduced set to the
// provider's HTTPClient instead of the hard-coded auth.Scopes.
func TestConfigScopesPropagatesMinimalToProvider(t *testing.T) {
	d, p := depWithScopes(t, auth.ScopeModeMinimal)
	if _, err := d.ColabClient(context.Background()); err != nil {
		t.Fatalf("ColabClient: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("provider HTTPClient calls = %d, want 1", p.calls)
	}
	if got := strings.Join(p.scopes, "|"); got != strings.Join(auth.MinimalScopes, "|") {
		t.Fatalf("provider scopes = %v; want %v", p.scopes, auth.MinimalScopes)
	}
}

// Issue #71 (b): the compatibility default stays the original six scopes.
func TestConfigScopesDefaultRemainsFull(t *testing.T) {
	d, p := depWithScopes(t, "")
	if _, err := d.ColabClient(context.Background()); err != nil {
		t.Fatalf("ColabClient: %v", err)
	}
	if got := strings.Join(p.scopes, "|"); got != strings.Join(auth.Scopes, "|") {
		t.Fatalf("provider scopes = %v; want the full set %v", p.scopes, auth.Scopes)
	}
}

// Issue #71: --scopes is a persistent (global) flag defaulting to full.
func TestScopesFlagDefaultsToFull(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), io.Discard, io.Discard)
	f := root.PersistentFlags().Lookup("scopes")
	if f == nil {
		t.Fatal("missing persistent --scopes flag")
	}
	if f.DefValue != string(auth.DefaultScopeMode) {
		t.Fatalf("--scopes default = %q, want %q", f.DefValue, auth.DefaultScopeMode)
	}
	if !strings.Contains(f.Usage, "Drive") {
		t.Fatalf("--scopes usage should warn that minimal cannot mount Drive: %q", f.Usage)
	}
}

// An unknown mode must be rejected as a flag error, before any app setup.
func TestScopesFlagRejectsUnknownMode(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), io.Discard, io.Discard)
	root.SetArgs([]string{"--scopes=broad", "version"})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown scope mode") {
		t.Fatalf("unexpected error for --scopes=broad: %v", err)
	}
}
