package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

func testClient(t *testing.T, ctrl *fakecolab.Server) colabapi.Client {
	t.Helper()
	c, err := colabapi.New(colabapi.Config{
		Domain: ctrl.TunURL(), PublicAPI: ctrl.PublicURL(), HTTPClient: ctrl.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func liveEnv(key string) string {
	if key == "GOLAB_LIVE_PROBES" {
		return "1"
	}
	return ""
}

// TestDoctorLiveProbeIsReadOnly is the blocking GET-only gate for A4: the
// probe must issue only GET requests, even when the fake exposes allocation
// endpoints. A POST here would allocate a billable VM.
func TestDoctorLiveProbeIsReadOnly(t *testing.T) {
	ctrl := fakecolab.New(fakecolab.WithProxyTokenPrefix("planted-proxy-token-"))
	defer ctrl.Close()
	ctrl.SetRuntimes([]fakecolab.Runtime{{
		Name: "runtimes/r-1", Endpoint: "ep-1", URL: "https://kernel.example/",
		Token: "planted-proxy-token-SECRET", ExpireTime: time.Now().Add(time.Hour),
	}})
	rep, err := Probe(context.Background(), Config{
		Live: true, Getenv: liveEnv, Timeout: 2 * time.Second,
	}, testClient(t, ctrl))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !rep.Live {
		t.Fatal("report should say live")
	}
	requests := ctrl.Requests()
	if len(requests) == 0 {
		t.Fatal("live probe made no requests")
	}
	for _, r := range requests {
		if r.Method != "GET" {
			t.Errorf("non-GET method %s %s", r.Method, r.Path)
		}
	}
	var foundList, foundPublic, foundTunnel bool
	for _, c := range rep.Checks {
		apiCheck := false
		switch c.Name {
		case "control-plane.list":
			foundList, apiCheck = true, true
		case "api.public":
			foundPublic, apiCheck = true, true
		case "api.tunnel":
			foundTunnel, apiCheck = true, true
		}
		if apiCheck && c.Status != "pass" && c.Status != "fail" {
			t.Errorf("live check %q has status %q", c.Name, c.Status)
		}
	}
	if !foundList || !foundPublic || !foundTunnel {
		t.Errorf("missing checks: list=%v public=%v tunnel=%v", foundList, foundPublic, foundTunnel)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "planted-proxy-token-SECRET") {
		t.Fatalf("report leaked the planted proxy token: %s", b)
	}
}

// TestDoctorOfflineMakesNoRequests verifies the default mode never touches the
// network: without --live there must be zero HTTP calls.
func TestDoctorOfflineMakesNoRequests(t *testing.T) {
	ctrl := fakecolab.New()
	defer ctrl.Close()
	rep, err := Probe(context.Background(), Config{}, testClient(t, ctrl))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rep.Live {
		t.Fatal("offline report must not claim live mode")
	}
	if n := len(ctrl.Requests()); n != 0 {
		t.Fatalf("offline probe made %d requests", n)
	}
	for _, c := range rep.Checks {
		switch c.Name {
		case "control-plane.list", "api.public", "api.tunnel":
			if c.Status != "skip" {
				t.Errorf("offline API check %q status = %q, want skip", c.Name, c.Status)
			}
			if c.Remediation == "" {
				t.Errorf("skipped check %q has no remediation", c.Name)
			}
		}
	}
}

// TestDoctorKillSwitchBlocksLive verifies both parts of the A4 kill switch:
// GOLAB_LIVE_PROBES=1 is required and LIVE_E2E_ENABLED=false wins.
func TestDoctorKillSwitchBlocksLive(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(string) string
	}{
		{"no-opt-in", func(string) string { return "" }},
		{"ci-disabled", func(key string) string {
			if key == "GOLAB_LIVE_PROBES" {
				return "1"
			}
			if key == "LIVE_E2E_ENABLED" {
				return "false"
			}
			return ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := fakecolab.New()
			defer ctrl.Close()
			_, err := Probe(context.Background(), Config{Live: true, Getenv: tc.env}, testClient(t, ctrl))
			if !errors.Is(err, ErrLiveDisabled) {
				t.Fatalf("error = %v, want ErrLiveDisabled", err)
			}
			if n := len(ctrl.Requests()); n != 0 {
				t.Fatalf("blocked live probe made %d requests", n)
			}
		})
	}
}

// TestDoctorHardTimeout bounds a hanging control plane with a hard deadline
// instead of inheriting the client's 60 s default.
func TestDoctorHardTimeout(t *testing.T) {
	ctrl := fakecolab.New()
	defer ctrl.Close()
	ctrl.SetFault(fakecolab.Fault{Hang: true, PathContains: "/assignments", Times: -1})
	start := time.Now()
	_, err := Probe(context.Background(), Config{
		Live: true, Getenv: liveEnv, Timeout: 60 * time.Millisecond,
	}, testClient(t, ctrl))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("probe took %s despite 60ms timeout", d)
	}
}

// TestDoctorFixtureInventory is a golab-specific regression test for
// CONFIRMED DEFECT §4.21 (no upstream Python equivalent): a fixture directory
// holding only a README is not proof of anything, so the check must say skip
// (or warning) and explain that no differential proof is available. It must
// not report a healthy pass on an empty inventory.
func TestDoctorFixtureInventory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# no captures yet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Probe(context.Background(), Config{FixtureDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := findFixtureCheck(t, rep)
	if check.Status != "skip" && check.Status != "warning" {
		t.Errorf("empty fixture inventory status = %q, detail = %q; want skip or warning", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "no differential proof") {
		t.Errorf("empty fixture inventory detail = %q; want an explicit no-differential-proof note", check.Detail)
	}
	if check.Remediation == "" {
		t.Error("skipped fixture check has no remediation")
	}
}

// TestDoctorFixtureInventoryWithFixturePasses pins the other half of the
// contract: a non-empty inventory still earns a pass. golab-specific (no
// upstream Python equivalent).
func TestDoctorFixtureInventoryWithFixturePasses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "capture.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Probe(context.Background(), Config{FixtureDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := findFixtureCheck(t, rep)
	if check.Status != "pass" {
		t.Errorf("one captured fixture: status = %q, detail = %q; want pass", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "1 captured wire fixture(s)") {
		t.Errorf("one captured fixture: detail = %q; want the inventory count", check.Detail)
	}
}

func findFixtureCheck(t *testing.T, rep Report) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == "wire.fixtures" {
			return c
		}
	}
	t.Fatalf("no wire.fixtures check in report: %+v", rep.Checks)
	return Check{}
}

// --- Public API prerequisite diagnostics (issue #289) ---------------------
// These tests fake env/filesystem/gcloud: no real gcloud is ever spawned and
// no GCP resource can be mutated. Upstream issue #289 is a human-in-the-loop
// story (re-auth + quota project), so there is no upstream Python unit test to
// port; these are golab-specific diagnostic regressions.

type fakeGCloudRunner struct {
	calls [][]string
	out   string
	err   error
}

func (f *fakeGCloudRunner) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	return f.out, f.err
}

func assertReadOnlyGCloudCalls(t *testing.T, f *fakeGCloudRunner, want int) {
	t.Helper()
	if len(f.calls) != want {
		t.Fatalf("gcloud calls = %d, want %d: %v", len(f.calls), want, f.calls)
	}
	for _, args := range f.calls {
		if got := strings.Join(args, " "); got != "config get-value quota/project" {
			t.Fatalf("gcloud args = %q, want only the read-only quota/project probe", got)
		}
		for _, a := range args {
			switch strings.ToLower(a) {
			case "set", "unset", "auth", "login", "billing", "projects", "update":
				t.Fatalf("gcloud mutation verb %q in args %v", a, args)
			}
		}
	}
}

func prereqEnv(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

func findNamedCheck(t *testing.T, rep Report, name string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in report: %+v", name, rep.Checks)
	return Check{}
}

func hasNamedCheck(rep Report, name string) bool {
	for _, c := range rep.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func noADCFile(string) ([]byte, error) { return nil, os.ErrNotExist }

func prereqConfig(env map[string]string, home string, read func(string) ([]byte, error), gc *fakeGCloudRunner) PrerequisiteConfig {
	return PrerequisiteConfig{
		Getenv:       prereqEnv(env),
		ReadFile:     read,
		UserHomeDir:  func() (string, error) { return home, nil },
		RunGCloud:    gc.run,
		AuthProvider: "oauth2",
		ScopeMode:    "full",
	}
}

// TestDoctorQuotaProjectMissingIsActionable pins requirement (a): with no
// local signal, the api section must carry a non-fatal, actionable hint.
func TestDoctorQuotaProjectMissingIsActionable(t *testing.T) {
	gc := &fakeGCloudRunner{out: "(unset)\n"}
	rep, err := Probe(context.Background(), Config{
		Prerequisites: prereqConfig(map[string]string{}, t.TempDir(), noADCFile, gc),
	}, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	c := findNamedCheck(t, rep, "api.quota-project")
	if c.Status != "skip" {
		t.Fatalf("status = %q, detail = %q; want non-fatal skip", c.Status, c.Detail)
	}
	text := c.Detail + " " + c.Remediation
	for _, want := range []string{"quota", "GOOGLE_CLOUD_QUOTA_PROJECT", "maintainer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in actionable hint: %q", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "golab creates") ||
		strings.Contains(strings.ToLower(text), "golab sets") {
		t.Fatalf("hint must not claim golab provisions GCP: %q", text)
	}
	assertReadOnlyGCloudCalls(t, gc, 1)
}

// TestDoctorQuotaProjectFromEnv pins requirement (b) for the env signal: it
// is reported as detected, the value is never echoed, and gcloud is skipped.
func TestDoctorQuotaProjectFromEnv(t *testing.T) {
	const canary = "env-quota-project-CANARY"
	gc := &fakeGCloudRunner{}
	rep, err := Probe(context.Background(), Config{
		Prerequisites: prereqConfig(map[string]string{"GOOGLE_CLOUD_QUOTA_PROJECT": canary}, t.TempDir(), noADCFile, gc),
	}, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	c := findNamedCheck(t, rep, "api.quota-project")
	if c.Status != "pass" || !strings.Contains(c.Detail, "GOOGLE_CLOUD_QUOTA_PROJECT") {
		t.Fatalf("env signal not reported as detected: %+v", c)
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), canary) {
		t.Fatalf("quota project value leaked: %s", b)
	}
	assertReadOnlyGCloudCalls(t, gc, 0)
}

// TestDoctorQuotaProjectFromADC covers the ADC file signal and asserts that
// credential material in the same JSON file is never copied into the report.
func TestDoctorQuotaProjectFromADC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application_default_credentials.json")
	read := func(p string) ([]byte, error) {
		if p != path {
			return nil, os.ErrNotExist
		}
		return []byte(`{"quota_project_id":"adc-project","refresh_token":"PLANTED_REFRESH_TOKEN","client_secret":"PLANTED_CLIENT_SECRET"}`), nil
	}
	gc := &fakeGCloudRunner{}
	rep, err := Probe(context.Background(), Config{
		Prerequisites: prereqConfig(map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": path}, t.TempDir(), read, gc),
	}, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	c := findNamedCheck(t, rep, "api.quota-project")
	if c.Status != "pass" || !strings.Contains(c.Detail, "adc:quota_project_id") {
		t.Fatalf("ADC signal not reported as detected: %+v", c)
	}
	b, _ := json.Marshal(rep)
	for _, secret := range []string{"PLANTED_REFRESH_TOKEN", "PLANTED_CLIENT_SECRET", "adc-project"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("ADC material leaked (%s): %s", secret, b)
		}
	}
	assertReadOnlyGCloudCalls(t, gc, 0)
}

// TestDoctorQuotaProjectFromGCloud covers the third local signal and pins
// requirement (c): only a read-only `config get-value` probe is permitted.
func TestDoctorQuotaProjectFromGCloud(t *testing.T) {
	gc := &fakeGCloudRunner{out: "gcloud-project-CANARY\n"}
	rep, err := Probe(context.Background(), Config{
		Prerequisites: prereqConfig(map[string]string{}, t.TempDir(), noADCFile, gc),
	}, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	c := findNamedCheck(t, rep, "api.quota-project")
	if c.Status != "pass" || !strings.Contains(c.Detail, "gcloud") {
		t.Fatalf("gcloud signal not reported as detected: %+v", c)
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), "gcloud-project-CANARY") {
		t.Fatalf("gcloud output leaked into report: %s", b)
	}
	assertReadOnlyGCloudCalls(t, gc, 1)
}

// TestDoctorReconsentHintOnlyForOAuth2ScopeChange pins the re-consent note:
// OAuth2 + minimal scope mode produces it; ADC and the default full mode do not.
func TestDoctorReconsentHintOnlyForOAuth2ScopeChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		scope    string
		env      map[string]string
		wantHint bool
	}{
		{"oauth2-minimal", "oauth2", "minimal", nil, true},
		{"oauth2-full", "oauth2", "full", nil, false},
		{"adc-minimal", "adc", "minimal", nil, false},
		{"env-scope-fallback", "", "", map[string]string{"COLAB_AUTH": "oauth2", "COLAB_SCOPES": "minimal"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env
			if env == nil {
				env = map[string]string{}
			}
			cfg := prereqConfig(env, t.TempDir(), noADCFile, &fakeGCloudRunner{out: "proj\n"})
			cfg.AuthProvider, cfg.ScopeMode = tc.provider, tc.scope
			rep, err := Probe(context.Background(), Config{Prerequisites: cfg}, nil)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			got := hasNamedCheck(rep, "api.reconsent")
			if got != tc.wantHint {
				t.Fatalf("reconsent check present = %v, want %v; checks: %+v", got, tc.wantHint, rep.Checks)
			}
			if tc.wantHint {
				c := findNamedCheck(t, rep, "api.reconsent")
				if !strings.Contains(c.Detail, "re-consent") {
					t.Fatalf("reconsent detail = %q", c.Detail)
				}
			}
		})
	}
}

// TestDoctorPrerequisitesAddWithoutReplacingAPI proves the new checks are
// additive: the offline skip lines keep their exact text and a live probe
// still reports all three reachability checks with GET-only traffic.
func TestDoctorPrerequisitesAddWithoutReplacingAPI(t *testing.T) {
	ctrl := fakecolab.New()
	defer ctrl.Close()
	cfg := prereqConfig(map[string]string{}, t.TempDir(), noADCFile, &fakeGCloudRunner{out: "(unset)\n"})

	offline, err := Probe(context.Background(), Config{Prerequisites: cfg}, testClient(t, ctrl))
	if err != nil {
		t.Fatalf("offline Probe: %v", err)
	}
	for _, tc := range []struct{ name, detail, remediation string }{
		{"control-plane.list", "offline mode: no requests made", "re-run with --live and GOLAB_LIVE_PROBES=1 for a GET-only probe"},
		{"api.public", "offline mode: no requests made", "re-run with --live and GOLAB_LIVE_PROBES=1 for a GET-only probe"},
		{"api.tunnel", "offline mode: no requests made", "re-run with --live and GOLAB_LIVE_PROBES=1 for a GET-only probe"},
	} {
		c := findNamedCheck(t, offline, tc.name)
		if c.Status != "skip" || c.Detail != tc.detail || c.Remediation != tc.remediation {
			t.Fatalf("%s changed: %+v", tc.name, c)
		}
	}
	if !hasNamedCheck(offline, "api.quota-project") {
		t.Fatal("offline report lost the new quota-project check")
	}
	if n := len(ctrl.Requests()); n != 0 {
		t.Fatalf("offline probe made %d requests", n)
	}

	ctrl.SetRuntimes([]fakecolab.Runtime{{
		Name: "runtimes/r-1", Endpoint: "ep-1", URL: "https://kernel.example/",
		Token: "planted-proxy-token-SECRET", ExpireTime: time.Now().Add(time.Hour),
	}})
	live, err := Probe(context.Background(), Config{
		Live: true, Getenv: liveEnv, Timeout: 2 * time.Second, Prerequisites: cfg,
	}, testClient(t, ctrl))
	if err != nil {
		t.Fatalf("live Probe: %v", err)
	}
	for _, name := range []string{"control-plane.list", "api.public", "api.tunnel"} {
		c := findNamedCheck(t, live, name)
		if c.Status != "pass" && c.Status != "fail" {
			t.Fatalf("live %s status = %q", name, c.Status)
		}
	}
	if !hasNamedCheck(live, "api.quota-project") {
		t.Fatal("live report lost the new quota-project check")
	}
	for _, r := range ctrl.Requests() {
		if r.Method != "GET" {
			t.Fatalf("non-GET request %s %s", r.Method, r.Path)
		}
	}
}
