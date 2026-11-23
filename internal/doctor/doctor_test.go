package doctor

import (
	"context"
	"encoding/json"
	"errors"
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

// TestDoctorFixtureInventory makes the "fakes are not proof" limitation
// visible: with an empty directory the report must say zero captured fixtures.
func TestDoctorFixtureInventory(t *testing.T) {
	rep, err := Probe(context.Background(), Config{FixtureDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var checks []string
	for _, c := range rep.Checks {
		checks = append(checks, c.Name)
		if c.Name == "wire.fixtures" && c.Status != "pass" {
			t.Errorf("fixture check status = %q, detail = %q", c.Status, c.Detail)
		}
	}
	if !strings.Contains(strings.Join(checks, ","), "wire.fixtures") {
		t.Fatalf("no fixture inventory check: %v", checks)
	}
}
