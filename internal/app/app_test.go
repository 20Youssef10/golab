package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

func testDeps(t *testing.T, srv *fakecolab.Server) (*Deps, colabapi.Client) {
	t.Helper()
	dir := t.TempDir()
	store, err := state.NewStore(filepath.Join(dir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := state.NewSettingsStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := colabapi.New(colabapi.Config{
		Domain:      srv.TunURL(),
		HTTPClient:  srv.Client(),
		ClientAgent: "github.com/20Youssef10/golab/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	return &Deps{
		Config:        Config{ColabDomain: srv.TunURL()},
		Store:         store,
		SettingsStore: settings,
		HistoryDir:    dir,
		Stdout:        &bytes.Buffer{},
		Stderr:        &stderr,
		Log:           logx.Logger(),
		colab:         client,
	}, client
}

func assign(t *testing.T, client colabapi.Client) *colabapi.AssignmentResponse {
	t.Helper()
	nbh, err := colabapi.RandomNotebookHash()
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: nbh,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Ports upstream tests/test_resolution_logic.py::test_resolve_unique_session.
func TestResolveUniqueSession(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	a := assign(t, client)
	expiry := time.Now().Add(time.Hour)
	if err := d.Store.Put(state.Session{
		Name: "s1", Endpoint: a.Endpoint, URL: a.RuntimeProxyInfo.URL,
		Token: a.RuntimeProxyInfo.Token, TokenExpiresAt: &expiry,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.ResolveSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "s1" {
		t.Fatalf("resolved %q", got.Name)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "Using unique session 's1'") {
		t.Fatalf("stderr = %q", d.Stderr.(*bytes.Buffer).String())
	}
}

// Ports upstream test_client.py token-refresh intent (docs/01 §5).
func TestGetSessionRefreshesNearExpiry(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	a := assign(t, client)
	expired := time.Now().Add(time.Minute) // inside the 5-minute margin
	if err := d.Store.Put(state.Session{
		Name: "s1", Endpoint: a.Endpoint, URL: "https://stale",
		Token: logx.NewSecret("stale"), TokenExpiresAt: &expired,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != a.RuntimeProxyInfo.URL || got.Token.Reveal() != a.RuntimeProxyInfo.Token.Reveal() {
		t.Fatalf("not refreshed: %+v", got)
	}
	persisted, err := d.Store.Get("s1")
	if err != nil || persisted.Token.Reveal() != a.RuntimeProxyInfo.Token.Reveal() {
		t.Fatalf("not persisted: %+v, %v", persisted, err)
	}
}

// Ports upstream tests/test_resolution_logic.py::test_missing_session_pruned.
func TestGetSessionPrunesGoneAssignment(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, _ := testDeps(t, srv)
	expired := time.Now().Add(-time.Hour)
	if err := d.Store.Put(state.Session{
		Name: "gone", Endpoint: "no-such-endpoint", Token: logx.NewSecret("t"),
		TokenExpiresAt: &expired,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetSession(context.Background(), "gone"); err == nil {
		t.Fatal("expected an error for a pruned session")
	}
	if _, err := d.Store.Get("gone"); err != state.ErrNotFound {
		t.Fatalf("session still present: %v", err)
	}
}

func TestResolveMultipleSessionsRequiresName(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	for _, name := range []string{"a", "b"} {
		a := assign(t, client)
		expiry := time.Now().Add(time.Hour)
		if err := d.Store.Put(state.Session{
			Name: name, Endpoint: a.Endpoint, Token: a.RuntimeProxyInfo.Token,
			URL: a.RuntimeProxyInfo.URL, TokenExpiresAt: &expiry,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := d.ResolveSession(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "Multiple active sessions") {
		t.Fatalf("unexpected error: %v", err)
	}
}
