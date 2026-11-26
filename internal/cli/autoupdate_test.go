package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/update"
)

// fakeUpdateCheck replaces the real release check with a local, deterministic
// one. It counts "update-check HTTP requests" with an httptest server and can
// invoke report from a goroutine after a short delay, so a caller that fails
// to join observes an incomplete stderr write.
type fakeUpdateCheck struct {
	hits     atomic.Int32
	finished chan struct{}
	server   *httptest.Server
}

func newFakeUpdateCheck(t *testing.T, reportRelease bool) *fakeUpdateCheck {
	t.Helper()
	f := &fakeUpdateCheck{finished: make(chan struct{}, 4)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.hits.Add(1)
	}))
	t.Cleanup(f.server.Close)
	orig := maybeCheckFn
	maybeCheckFn = func(ctx context.Context, _ update.Config, report func(*update.Release)) func() {
		done := make(chan struct{})
		go func() {
			defer func() {
				close(done)
				f.finished <- struct{}{}
			}()
			resp, err := f.server.Client().Get(f.server.URL + "/repos/o/r/releases/latest")
			if err == nil {
				_ = resp.Body.Close()
			}
			if reportRelease {
				// Make a missing join deterministic: the banner is written well
				// after a command that does not wait has already returned.
				time.Sleep(50 * time.Millisecond)
				if report != nil {
					report(&update.Release{Version: "9.9.9"})
				}
			}
		}()
		return func() { <-done }
	}
	t.Cleanup(func() { maybeCheckFn = orig })
	return f
}

// enableDueUpdateCheck makes the harness settings eligible for a release
// check: enabled and no recent LastCheck.
func enableDueUpdateCheck(t *testing.T, h *testHarness) {
	t.Helper()
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.EnableUpdateCheck = true
	settings.LastCheck = nil
	if err := h.deps.SettingsStore.Save(settings); err != nil {
		t.Fatal(err)
	}
}

// runRealStartup drives execute() through the non-injected branch by stubbing
// only the dependency constructor. The auto-update notice therefore runs.
func runRealStartup(t *testing.T, h *testHarness, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	orig := newAppDeps
	newAppDeps = func(context.Context, app.Config) (*app.Deps, error) { return h.deps, nil }
	t.Cleanup(func() { newAppDeps = orig })
	return execute(context.Background(), args, strings.NewReader(""), &h.stdout, &h.stderr)
}

// TestExplainSkipsUpdateNotice pins defect §4.16: a --explain dry run must not
// trigger an update-check HTTP request, while an ordinary command still does.
func TestExplainSkipsUpdateNotice(t *testing.T) {
	t.Run("explain makes no request", func(t *testing.T) {
		h := newHarness(t)
		enableDueUpdateCheck(t, h)
		f := newFakeUpdateCheck(t, false)

		if code := runRealStartup(t, h, "--explain", "completion", "bash"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if n := f.hits.Load(); n != 0 {
			t.Fatalf("--explain made %d update-check HTTP requests, want 0", n)
		}
		if strings.Contains(h.stderr.String(), "Update available") {
			t.Fatalf("--explain printed an update banner: %q", h.stderr.String())
		}
	})
	t.Run("ordinary command still checks", func(t *testing.T) {
		h := newHarness(t)
		enableDueUpdateCheck(t, h)
		f := newFakeUpdateCheck(t, false)

		if code := runRealStartup(t, h, "completion", "bash"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if n := f.hits.Load(); n < 1 {
			t.Fatalf("ordinary command made %d update-check HTTP requests, want >= 1", n)
		}
	})
}

// TestUpdateNoticeWriterJoinedBeforeReturn pins defect §4.16: the asynchronous
// notice's stderr write must complete before execute() returns. The snapshot
// is taken immediately at return; the delayed fake makes a missing join fail
// deterministically instead of racing the assertion.
func TestUpdateNoticeWriterJoinedBeforeReturn(t *testing.T) {
	h := newHarness(t)
	enableDueUpdateCheck(t, h)
	f := newFakeUpdateCheck(t, true)

	code := runRealStartup(t, h, "completion", "bash")
	snapshot := h.stderr.String()
	// Always drain the fake before the test ends so no goroutine writes into a
	// dead harness (important while this test is red).
	<-f.finished

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, snapshot)
	}
	if n := f.hits.Load(); n < 1 {
		t.Fatalf("expected an update-check HTTP request, got %d", n)
	}
	if !strings.Contains(snapshot, "[colab] Update available: 9.9.9.") {
		t.Fatalf("stderr write did not complete before command return: %q", snapshot)
	}
	if !strings.Contains(snapshot, "Run 'golab update --install' to upgrade.") {
		t.Fatalf("notice text changed: %q", snapshot)
	}
}
