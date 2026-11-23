package update

// Ported from upstream tests/test_update.py:
//   - test_install_flag_no_op_when_already_up_to_date -> TestVerifyAndInstallRefusesDowngrade
//   - test_background_check_includes_disable_hint / quiet-subcommand tests ->
//     TestMaybeCheckNeverWritesToStdout (banner delivered only via callback).
// Windows running-exe replace and "delete .old on next start" implement the
// R-09 / docs/05 requirement.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestVerifyAndInstallRefusesDowngrade(t *testing.T) {
	s, downloads := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)
	cfg.CurrentVersion = "99.0.0"

	rel := releaseFromFixture(t, []byte("fake binary contents"), "")
	err := VerifyAndInstall(context.Background(), cfg, rel)
	if !errors.Is(err, ErrDowngrade) {
		t.Fatalf("expected ErrDowngrade, got %v", err)
	}
	if downloads.Load() != 0 {
		t.Fatalf("downgrade must be refused before any download, got %d requests", downloads.Load())
	}
}

func TestVerifyAndInstallValidatesReleaseMetadata(t *testing.T) {
	cfg := fakeConfig(nil)
	for _, tc := range []struct {
		name string
		rel  *Release
		want error
	}{
		{"nil release", nil, ErrBadRelease},
		{"empty version", &Release{URL: "u", Checksum: "c", SignatureURL: "s"}, ErrBadRelease},
		{"empty url", &Release{Version: "1.0.0", Checksum: "c", SignatureURL: "s"}, ErrBadRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyAndInstall(context.Background(), cfg, tc.rel)
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestReplaceBinaryUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path not exercised on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "golab")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(target, []byte("new"), "linux", 0o755); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("got %q, want new", got)
	}
}

func TestReplaceBinaryWindows(t *testing.T) {
	// Exercise the Windows running-exe strategy on any host OS. The .old file
	// must be left behind for deletion on the next start; the running exe is
	// never removed outright.
	dir := t.TempDir()
	target := filepath.Join(dir, "golab.exe")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(target, []byte("new"), "windows", 0o755); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("got %q, want new", got)
	}
	oldBytes, err := os.ReadFile(target + ".old")
	if err != nil {
		t.Fatalf(".old file must be left for next-start cleanup: %v", err)
	}
	if string(oldBytes) != "old" {
		t.Fatalf("old binary content = %q, want old", oldBytes)
	}

	// CleanupOld removes the .old file on the next start and is idempotent.
	if err := CleanupOld(target); err != nil {
		t.Fatalf("CleanupOld: %v", err)
	}
	if _, err := os.Stat(target + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".old file still present after CleanupOld: %v", err)
	}
	if err := CleanupOld(target); err != nil {
		t.Fatalf("CleanupOld must be idempotent: %v", err)
	}
	// The currently installed executable must never be deleted.
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("running executable was removed: %v", err)
	}
}

func TestMaybeCheckNeverWritesToStdout(t *testing.T) {
	// Upstream's announcement goes through typer.echo (stdout). In Go the
	// banner must be delivered exclusively through the callback; the package
	// must not write a single byte to stdout.
	s, downloads := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)

	reported := make(chan *Release, 1)
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	MaybeCheck(context.Background(), cfg, func(rel *Release) { reported <- rel })

	// Wait for the background check to finish while stdout is captured.
	select {
	case <-reported:
	case <-time.After(3 * time.Second):
		os.Stdout = old
		w.Close()
		t.Fatal("callback was not invoked")
	}
	w.Close()
	os.Stdout = old
	captured, _ := io.ReadAll(r)
	if len(captured) != 0 {
		t.Fatalf("unexpected stdout output: %q", captured)
	}
	if downloads.Load() < 1 {
		t.Fatalf("expected network traffic, got %d requests", downloads.Load())
	}
}

func TestMaybeCheckSkippedWhenNotDue(t *testing.T) {
	s, downloads := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)
	recent := time.Now().Add(-time.Hour)
	cfg.LastCheck = &recent

	called := make(chan struct{}, 1)
	MaybeCheck(context.Background(), cfg, func(*Release) { called <- struct{}{} })
	select {
	case <-called:
		t.Fatal("callback must not fire when check is throttled")
	case <-time.After(150 * time.Millisecond):
	}
	if downloads.Load() != 0 {
		t.Fatalf("no network expected when throttled, got %d requests", downloads.Load())
	}
}
