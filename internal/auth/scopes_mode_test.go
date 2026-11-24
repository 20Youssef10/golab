// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// TestScopeModeParsingAndDefaults pins the --scopes full|minimal contract:
// case-insensitive parsing (like --auth), full is the compatibility default,
// and only the minimal set omits drive.file (needed solely by drivemount).
func TestScopeModeParsingAndDefaults(t *testing.T) {
	if DefaultScopeMode != ScopeModeFull {
		t.Fatalf("DefaultScopeMode = %q, want %q", DefaultScopeMode, ScopeModeFull)
	}
	for _, tc := range []struct {
		in   string
		want ScopeMode
	}{
		{"full", ScopeModeFull}, {"FULL", ScopeModeFull}, {" Full ", ScopeModeFull},
		{"minimal", ScopeModeMinimal}, {"MINIMAL", ScopeModeMinimal}, {" minimal ", ScopeModeMinimal},
	} {
		got, err := ParseScopeMode(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseScopeMode(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := ParseScopeMode("broad"); err == nil {
		t.Fatal("ParseScopeMode accepted an unknown mode")
	}
	var m ScopeMode
	if err := m.Set("minimal"); err != nil || m != ScopeModeMinimal {
		t.Fatalf("Set(minimal) = %q, %v", m, err)
	}
	if m.Type() != "string" {
		t.Fatalf("Type = %q, want string", m.Type())
	}
	if strings.Join(ScopeMode("").Scopes(), "|") != strings.Join(Scopes, "|") {
		t.Fatal("empty mode must default to the full scope set")
	}
	if strings.Join(ScopeModeFull.Scopes(), "|") != strings.Join(Scopes, "|") {
		t.Fatal("full mode must map to the six-scope set")
	}
	if strings.Join(ScopeModeMinimal.Scopes(), "|") != strings.Join(MinimalScopes, "|") {
		t.Fatal("minimal mode must map to MinimalScopes")
	}
	if !contains(ScopeModeFull.Scopes(), DriveFileScope) || contains(ScopeModeMinimal.Scopes(), DriveFileScope) {
		t.Fatal("drive.file must be present only in full mode (drivemount requires it)")
	}
}

// TestScopeModeConfigDrivesNilScopes proves the mode stored in auth.Config is
// applied even when a caller passes no explicit scopes (HTTPClient(ctx, nil)).
func TestScopeModeConfigDrivesNilScopes(t *testing.T) {
	t.Run("adc", func(t *testing.T) {
		cfg := adcTestConfig(t, io.Discard)
		cfg.ScopeMode = ScopeModeMinimal
		p := mustProvider(t, cfg).(*adcProvider)
		var requested []string
		p.defaults = func(_ context.Context, scopes ...string) (oauth2.TokenSource, error) {
			requested = append([]string(nil), scopes...)
			return fakeTokenSource{tok: &oauth2.Token{
				AccessToken: "adc-access", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour),
			}}, nil
		}
		if _, err := p.HTTPClient(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if strings.Join(requested, "|") != strings.Join(MinimalScopes, "|") {
			t.Fatalf("ADC scopes = %v; want %v", requested, MinimalScopes)
		}
	})
	t.Run("oauth2", func(t *testing.T) {
		srv := fakeTokenServer(t, func(url.Values) (int, string) {
			return 200, tokenJSON("access", "refresh", 3600, MinimalScopes)
		})
		defer srv.Close()
		cfg, _, _, stderr := testConfig(t, srv.URL, "code\n")
		cfg.ScopeMode = ScopeModeMinimal
		if _, err := mustProvider(t, cfg).HTTPClient(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		q, err := url.Parse(extractAuthURL(t, stderr.String()))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Fields(q.Query().Get("scope")); strings.Join(got, "|") != strings.Join(MinimalScopes, "|") {
			t.Fatalf("auth scope = %v; want %v", got, MinimalScopes)
		}
	})
}

// TestRequestedScopesPersisted records the *requested* set separately from the
// granted set so a later mode switch is detectable without corrupting partial
// consent (the granted set stays authoritative for refresh).
func TestRequestedScopesPersisted(t *testing.T) {
	granted := []string{"openid", EmailScope, CollaboratoryScope}
	srv := fakeTokenServer(t, func(url.Values) (int, string) {
		return 200, tokenJSON("access", "refresh", 3600, granted)
	})
	defer srv.Close()

	cfg, kr, _, _ := testConfig(t, srv.URL, "code\n")
	cfg.ScopeMode = ScopeModeMinimal
	if _, err := mustProvider(t, cfg).HTTPClient(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var out storedToken
	if err := json.Unmarshal([]byte(kr.value()), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.Scopes, "|") != strings.Join(granted, "|") {
		t.Fatalf("granted scopes = %v; want the partial grant %v", out.Scopes, granted)
	}
	if strings.Join(out.RequestedScopes, "|") != strings.Join(MinimalScopes, "|") {
		t.Fatalf("requested scopes = %v; want %v", out.RequestedScopes, MinimalScopes)
	}
}
