package cli

// No upstream Python port: browser-created runtimes are discovered from the
// read-only List on either plane. These tests use fakecolab on both the legacy
// tunnel and Public API planes and assert that no allocation POST is issued.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

const refreshedProxyToken = "SECRET-REFRESHED-PROXY-TOKEN-DO-NOT-PRINT"

func secondWebRuntime() fakecolab.Runtime {
	rt := webAttachRuntime()
	rt.Name = "runtimes/r-web-5678"
	rt.Endpoint = "web-endpoint-2"
	rt.URL = "https://runtime-web-2.example"
	rt.Token = "SECRET-SECOND-PROXY-TOKEN-DO-NOT-PRINT"
	return rt
}

// `sessions --web` must work on BOTH planes, including browsers' runtimes that
// were never created by this CLI and have no local session.
func TestSessionsWebListsRuntimesOnBothPlanes(t *testing.T) {
	t.Run("legacy tunnel", func(t *testing.T) {
		h := newHarness(t)
		a, err := h.client.Assign(context.Background(), colabapi.AssignRequest{})
		if err != nil {
			t.Fatal(err)
		}
		h.ctrl.ResetLog()
		if code := h.run(t, "", "sessions", "--web"); code != 0 {
			t.Fatalf("exit = %d, stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stdout.String(), "[?] "+a.Endpoint) {
			t.Errorf("stdout lacks the browser runtime: %q", h.stdout.String())
		}
		if strings.Contains(h.stdout.String(), a.RuntimeProxyInfo.Token.Reveal()) ||
			strings.Contains(h.stderr.String(), a.RuntimeProxyInfo.Token.Reveal()) {
			t.Error("sessions --web leaked the proxy token")
		}
		assertNoAllocationPosts(t, h)
	})
	t.Run("public API", func(t *testing.T) {
		rt := webAttachRuntime()
		h := newPublicAttachHarness(t, rt)
		h.ctrl.ResetLog()
		if code := h.run(t, "", "sessions", "--web"); code != 0 {
			t.Fatalf("exit = %d, stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stdout.String(), "[?] "+rt.Endpoint) {
			t.Errorf("stdout lacks the browser runtime: %q", h.stdout.String())
		}
		assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
		assertNoAllocationPosts(t, h)
	})
}

func TestSessionsWebMarksAttachedRuntime(t *testing.T) {
	rt := webAttachRuntime()
	h := newPublicAttachHarness(t, rt)
	if code := h.run(t, "", "attach", "--runtime", rt.Name, "--session", "webclone"); code != 0 {
		t.Fatalf("attach exit = %d", code)
	}
	h.ctrl.ResetLog()
	if code := h.run(t, "", "sessions", "--web"); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "[webclone] "+rt.Endpoint) {
		t.Errorf("attached runtime was not named: %q", h.stdout.String())
	}
	assertNoAllocationPosts(t, h)
}

func TestAttachAllImportsEveryUnattachedRuntime(t *testing.T) {
	rt1, rt2 := webAttachRuntime(), secondWebRuntime()
	h := newPublicAttachHarness(t, rt1, rt2)
	// One runtime is already attached under a different local name.
	s := h.putSession(t, "already")
	s.Endpoint, s.RuntimeName = rt1.Endpoint, rt1.Name
	if err := h.deps.Store.Put(s); err != nil {
		t.Fatal(err)
	}
	h.ctrl.ResetLog()

	if code := h.run(t, "", "attach", "--all"); code != 0 {
		t.Fatalf("attach --all exit = %d, stderr=%q", code, h.stderr.String())
	}
	sessions, err := h.deps.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("stored %d sessions, want 2: %+v", len(sessions), sessions)
	}
	if _, ok := sessions["already"]; !ok {
		t.Error("already-attached session was lost")
	}
	if added, ok := sessions["r-web-5678"]; !ok {
		t.Errorf("second runtime was not imported: %+v", sessions)
	} else if added.Token.Reveal() != rt2.Token || added.URL != rt2.URL {
		t.Errorf("imported connection info = %+v", added)
	}
	if strings.Contains(h.stdout.String(), rt2.Token) || strings.Contains(h.stderr.String(), rt2.Token) {
		t.Error("attach --all leaked a proxy token")
	}
	assertNoAllocationPosts(t, h)

	// A second run is a no-op: no duplicate local sessions.
	if code := h.run(t, "", "attach", "--all"); code != 0 {
		t.Fatalf("second attach --all exit = %d", code)
	}
	sessions, err = h.deps.Store.List()
	if err != nil || len(sessions) != 2 {
		t.Fatalf("attach --all is not idempotent: %+v, %v", sessions, err)
	}
	assertNoAllocationPosts(t, h)
}

func TestAttachRefreshUpdatesProxyTokenURLAndExpiry(t *testing.T) {
	rt := webAttachRuntime()
	h := newPublicAttachHarness(t, rt)
	if code := h.run(t, "", "attach", "--runtime", rt.Name, "--session", "webclone"); code != 0 {
		t.Fatalf("attach exit = %d", code)
	}
	before, err := h.deps.Store.Get("webclone")
	if err != nil {
		t.Fatal(err)
	}
	if before.Token.Reveal() != rt.Token {
		t.Fatalf("initial token = %q", before.Token.Reveal())
	}

	// A fresh List returns a rotated token, URL and expiry.
	updated := rt
	updated.Token = refreshedProxyToken
	updated.URL = "https://runtime-web-rotated.example"
	updated.ExpireTime = time.Now().UTC().Add(2 * time.Hour)
	h.ctrl.SetRuntimes([]fakecolab.Runtime{updated})
	h.ctrl.ResetLog()

	if code := h.run(t, "", "attach", "--refresh", "--runtime", rt.Name); code != 0 {
		t.Fatalf("attach --refresh exit = %d, stderr=%q", code, h.stderr.String())
	}
	after, err := h.deps.Store.Get("webclone")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token.Reveal() != refreshedProxyToken || after.URL != updated.URL {
		t.Errorf("refresh did not update credentials: %+v", after)
	}
	if after.TokenExpiresAt == nil || time.Until(*after.TokenExpiresAt) < 90*time.Minute {
		t.Errorf("refresh did not update expiry: %v", after.TokenExpiresAt)
	}
	if strings.Contains(h.stdout.String(), refreshedProxyToken) || strings.Contains(h.stderr.String(), refreshedProxyToken) {
		t.Error("attach --refresh leaked the rotated token")
	}
	assertNoAllocationPosts(t, h)
}

func TestAttachRefreshAllUpdatesEveryAttachedRuntime(t *testing.T) {
	rt1, rt2 := webAttachRuntime(), secondWebRuntime()
	h := newPublicAttachHarness(t, rt1, rt2)
	if code := h.run(t, "", "attach", "--all"); code != 0 {
		t.Fatalf("attach --all exit = %d", code)
	}
	up1, up2 := rt1, rt2
	up1.Token, up2.Token = refreshedProxyToken, "SECRET-SECOND-ROTATED-TOKEN-DO-NOT-PRINT"
	h.ctrl.SetRuntimes([]fakecolab.Runtime{up1, up2})
	h.ctrl.ResetLog()

	if code := h.run(t, "", "attach", "--refresh"); code != 0 {
		t.Fatalf("attach --refresh (all) exit = %d, stderr=%q", code, h.stderr.String())
	}
	for name, want := range map[string]string{"r-web-1234": refreshedProxyToken, "r-web-5678": "SECRET-SECOND-ROTATED-TOKEN-DO-NOT-PRINT"} {
		s, err := h.deps.Store.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if s.Token.Reveal() != want {
			t.Errorf("%s token = %q, want rotated", name, s.Token.Reveal())
		}
	}
	assertNoAllocationPosts(t, h)
}

func TestAttachAllWithNoRuntimesNamesThePlane(t *testing.T) {
	h := newHarness(t) // legacy tunnel plane
	h.ctrl.ResetLog()
	if code := h.run(t, "", "attach", "--all"); code != 0 {
		t.Fatalf("attach --all exit = %d, stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "legacy tunnel") {
		t.Errorf("error did not name the plane: %q", h.stderr.String())
	}
	sessions, err := h.deps.Store.List()
	if err != nil || len(sessions) != 0 {
		t.Errorf("empty list wrote sessions: %+v, %v", sessions, err)
	}
	assertNoAllocationPosts(t, h)
}

func TestAttachRefreshUnknownRuntimeIsUsageError(t *testing.T) {
	rt := webAttachRuntime()
	h := newPublicAttachHarness(t, rt)
	if code := h.run(t, "", "attach", "--refresh", "--runtime", "runtimes/r-missing"); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%q", code, h.stderr.String())
	}
	assertNoAllocationPosts(t, h)
}

func TestAttachRejectsConflictingModes(t *testing.T) {
	for _, args := range [][]string{
		{"--all", "--runtime", "runtimes/r-web-1234"},
		{"--all", "--refresh"},
	} {
		h := newHarness(t)
		if code := h.run(t, "", append([]string{"attach"}, args...)...); code != 2 {
			t.Errorf("attach %v exit = %d, want 2; stderr=%q", args, code, h.stderr.String())
		}
		assertNoAllocationPosts(t, h)
	}
}
