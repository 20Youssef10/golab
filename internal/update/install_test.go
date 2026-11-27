package update

// Ported from upstream tests/test_update.py:
//   - test_install_flag_no_op_when_already_up_to_date -> TestVerifyAndInstallRefusesDowngrade
//   - test_background_check_includes_disable_hint / quiet-subcommand tests ->
//     TestMaybeCheckNeverWritesToStdout (banner delivered only via callback).
// Windows running-exe replace and "delete .old on next start" implement the
// R-09 / docs/05 requirement.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Regression for the critical self-update bug: the release asset is a
// GoReleaser .tar.gz, not a bare executable. A v0.1.7 binary installed the
// archive bytes, printed a success message, and left a gzip file on disk that
// failed with "Exec format error" on the next run. Verification must keep
// binding to the ARCHIVE while the executable installed is the member inside.
//
// Port of upstream tests/test_update.py::test_install_installs_verified_asset
// (extended to cover archive extraction, which upstream does not exercise).
func TestVerifyAndInstallExtractsBinaryFromArchive(t *testing.T) {
	archive := fakeArtifact(t)
	if len(archive) <= len(fakeExecutable) || !bytes.HasPrefix(archive, []byte{0x1f, 0x8b}) {
		t.Fatal("test fixture is not a gzip archive")
	}
	s, _ := newReleaseServer(t, releaseFixture{artifact: archive})
	defer s.Close()
	cfg := fakeConfig(s)

	dir := t.TempDir()
	target := filepath.Join(dir, "golab")
	if err := os.WriteFile(target, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	redirectExecutable(t, target)

	if err := VerifyAndInstall(context.Background(), cfg, releaseFromFixture(t, archive, "")); err != nil {
		t.Fatalf("VerifyAndInstall: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, fakeExecutable) {
		t.Fatalf("installed %d bytes, want the %d-byte binary inside the archive; installed prefix = % x (archive prefix = % x)",
			len(got), len(fakeExecutable), got[:min(len(got), 16)], archive[:min(len(archive), 16)])
	}
	// No other archive member may ever be written to disk.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"golab"}
	if runtime.GOOS == "windows" {
		// A running .exe cannot be overwritten: replaceBinary renames the old
		// executable to <target>.old and removes it on the next start.
		wantNames = append(wantNames, "golab.old")
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != len(wantNames) {
		t.Fatalf("unexpected files written by update: %v (want %v)", names, wantNames)
	}
	for i, want := range wantNames {
		if names[i] != want {
			t.Fatalf("unexpected files written by update: %v (want %v)", names, wantNames)
		}
	}
}

// A signed archive is still untrusted input at the extraction layer. Every
// hostile member must be rejected, and the legitimate binary must be unique.
func TestExtractBinaryRejectsHostileArchives(t *testing.T) {
	good := archiveEntry{name: "golab", data: fakeExecutable, mode: 0o755}
	doc := archiveEntry{name: "CHANGELOG.md", data: []byte("# changelog\n")}
	for _, tc := range []struct {
		name    string
		archive func(*testing.T) []byte
		limit   int64
		want    error
	}{
		{"path traversal", func(t *testing.T) []byte {
			return makeTarGz(t, archiveEntry{name: "../evil", data: []byte("pwned")}, good)
		}, maxArtifact, ErrUnsafeArchivePath},
		{"absolute path", func(t *testing.T) []byte {
			return makeTarGz(t, archiveEntry{name: "/tmp/evil", data: []byte("pwned")}, good)
		}, maxArtifact, ErrUnsafeArchivePath},
		{"nested path", func(t *testing.T) []byte {
			return makeTarGz(t, archiveEntry{name: "docs/evil", data: []byte("pwned")}, good)
		}, maxArtifact, ErrUnsafeArchivePath},
		{"symlink binary", func(t *testing.T) []byte {
			return makeTarGz(t, archiveEntry{name: "golab", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}, doc)
		}, maxArtifact, ErrUnexpectedArchiveMember},
		{"hardlink binary", func(t *testing.T) []byte {
			return makeTarGz(t, archiveEntry{name: "golab", typeflag: tar.TypeLink, linkname: "CHANGELOG.md"}, doc)
		}, maxArtifact, ErrUnexpectedArchiveMember},
		{"fifo entry", func(t *testing.T) []byte {
			return makeTarGz(t, good, archiveEntry{name: "pipe", typeflag: tar.TypeFifo})
		}, maxArtifact, ErrUnexpectedArchiveMember},
		{"missing binary", func(t *testing.T) []byte {
			return makeTarGz(t, doc)
		}, maxArtifact, ErrBinaryNotFound},
		{"duplicate binary", func(t *testing.T) []byte {
			return makeTarGz(t, good, good)
		}, maxArtifact, ErrDuplicateBinary},
		{"empty binary", func(t *testing.T) []byte {
			return makeTarGz(t, archiveEntry{name: "golab", mode: 0o755}, doc)
		}, maxArtifact, ErrEmptyBinary},
		{"declared size over cap", func(t *testing.T) []byte {
			return makeTarGz(t, good, doc)
		}, int64(len(fakeExecutable)) - 1, ErrTooLarge},
		{"decompressed stream over cap", func(t *testing.T) []byte {
			return makeTarGz(t, good, doc)
		}, 48, ErrTooLarge},
		{"not a gzip archive", func(t *testing.T) []byte {
			return []byte("this is not gzip")
		}, maxArtifact, ErrInvalidArchive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractBinary(tc.archive(t), "linux", tc.limit)
			if !errors.Is(err, tc.want) {
				t.Fatalf("extractBinary error = %v, want errors.Is(_, %v)", err, tc.want)
			}
			if got != nil {
				t.Fatalf("rejected archive returned %d bytes", len(got))
			}
		})
	}
}

// LIVE RELEASE REGRESSION (v0.2.0 -> v0.3.0 signed self-update): the new
// release archives carry the two Agent Skills under .agents/skills/ plus the
// flat golab-mcp binary. The updater must accept those read-only members,
// stream them to io.Discard, and still install ONLY the platform binary.
func TestExtractBinaryAllowsSkillMembers(t *testing.T) {
	archive := makeTarGz(t,
		archiveEntry{name: "golab", data: fakeExecutable, mode: 0o755},
		archiveEntry{name: "golab-mcp", data: []byte("flat mcp binary"), mode: 0o755},
		archiveEntry{name: "golab-mcp", data: []byte("duplicate skipped member"), mode: 0o755},
		archiveEntry{name: ".agents/skills/golab-operations/SKILL.md", data: []byte("# skill\n")},
		archiveEntry{name: ".agents/skills/golab-operations/references/safety.md", data: []byte("# refs\n")},
	)
	got, err := extractBinary(archive, "linux", maxArtifact)
	if err != nil {
		t.Fatalf("extractBinary(skills archive): %v", err)
	}
	if !bytes.Equal(got, fakeExecutable) {
		t.Fatalf("extracted %d bytes, want only the golab member (%d bytes)", len(got), len(fakeExecutable))
	}
	// The same shape on Windows: golab-mcp.exe is skipped, golab.exe installed.
	win := makeTarGz(t,
		archiveEntry{name: "golab.exe", data: fakeExecutable, mode: 0o755},
		archiveEntry{name: "golab-mcp.exe", data: []byte("flat mcp binary"), mode: 0o755},
		archiveEntry{name: ".agents/skills/colab-mcp/SKILL.md", data: []byte("# skill\n")},
	)
	got, err = extractBinary(win, "windows", maxArtifact)
	if err != nil {
		t.Fatalf("extractBinary(windows skills archive): %v", err)
	}
	if !bytes.Equal(got, fakeExecutable) {
		t.Fatalf("windows archive extracted %d bytes, want golab.exe only", len(got))
	}
}

// Traversal under the newly allowed skill prefix must stay fatal.
func TestExtractBinaryRejectsSkillTraversal(t *testing.T) {
	good := archiveEntry{name: "golab", data: fakeExecutable, mode: 0o755}
	for _, name := range []string{
		".agents/skills/../evil.md",
		".agents/skills/a/b/../../../evil",
	} {
		t.Run(name, func(t *testing.T) {
			archive := makeTarGz(t, good, archiveEntry{name: name, data: []byte("pwned")})
			got, err := extractBinary(archive, "linux", maxArtifact)
			if !errors.Is(err, ErrUnsafeArchivePath) {
				t.Fatalf("extractBinary error = %v, want ErrUnsafeArchivePath", err)
			}
			if got != nil {
				t.Fatalf("rejected archive returned %d bytes", len(got))
			}
		})
	}
}

// The allowlist is explicit: flat release files and .md skills, nothing else.
func TestSafeArchivePathAllowlist(t *testing.T) {
	allowed := []string{
		"LICENSE", "NOTICE", "README.md", "CHANGELOG.md",
		"golab", "golab.exe", "golab-mcp", "golab-mcp.exe",
		".agents/skills/colab-mcp/SKILL.md",
		".agents/skills/golab-operations/references/safety.md",
	}
	rejected := []string{
		"", ".", "..", "../evil", "/tmp/evil", `docs\evil`,
		"docs/evil", "golab-mcp.exe.bak",
		".agents/skills/../evil.md", ".agents/skills/a/b/../../../evil",
		".agents/skills/evil.txt", ".agents/skills/a/./b.md",
		".agents/skills/", ".agents/skills/a/", `/.agents/skills/a.md`,
		`.agents\skills\a.md`,
	}
	for _, name := range allowed {
		if !safeArchivePath(name) {
			t.Errorf("safeArchivePath(%q) = false, want true", name)
		}
	}
	for _, name := range rejected {
		if safeArchivePath(name) {
			t.Errorf("safeArchivePath(%q) = true, want false", name)
		}
	}
}

func TestExtractBinaryWindowsName(t *testing.T) {
	archive := makeTarGz(t,
		archiveEntry{name: "golab.exe", data: fakeExecutable, mode: 0o755},
		archiveEntry{name: "README.md", data: []byte("# golab\n")},
	)
	got, err := extractBinary(archive, "windows", maxArtifact)
	if err != nil {
		t.Fatalf("extractBinary(windows): %v", err)
	}
	if !bytes.Equal(got, fakeExecutable) {
		t.Fatalf("got %d bytes, want the 37-byte golab.exe payload", len(got))
	}
	if _, err := extractBinary(archive, "linux", maxArtifact); !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("linux extraction of a windows-only archive: %v", err)
	}
}

// The signed digest must bind to the ARCHIVE bytes. If a bundle signs only
// the inner executable while the checksum covers the archive, verification
// must fail before the install target is touched.
func TestVerifyAndInstallDigestBindsToArchiveBytes(t *testing.T) {
	archive := fakeArtifact(t)
	bundle := makeBundle(t, fakeExecutable, bundleOptions{goodSig: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: archive, bundle: bundle})
	defer s.Close()
	cfg := fakeConfig(s)

	dir := t.TempDir()
	target := filepath.Join(dir, "golab")
	if err := os.WriteFile(target, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	redirectExecutable(t, target)

	err := VerifyAndInstall(context.Background(), cfg, releaseFromFixture(t, archive, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for a bundle over inner bytes, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old executable" {
		t.Fatalf("failed update modified the installed binary: %q", got)
	}
}

// Extraction failures happen after verification but before any file write.
func TestVerifyAndInstallExtractionFailureDoesNotMutate(t *testing.T) {
	archive := makeTarGz(t,
		archiveEntry{name: "golab", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		archiveEntry{name: "CHANGELOG.md", data: []byte("# changelog\n")},
	)
	s, _ := newReleaseServer(t, releaseFixture{artifact: archive})
	defer s.Close()
	cfg := fakeConfig(s)

	dir := t.TempDir()
	target := filepath.Join(dir, "golab")
	if err := os.WriteFile(target, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	redirectExecutable(t, target)

	err := VerifyAndInstall(context.Background(), cfg, releaseFromFixture(t, archive, ""))
	if !errors.Is(err, ErrUnexpectedArchiveMember) {
		t.Fatalf("expected ErrUnexpectedArchiveMember, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old executable" {
		t.Fatalf("failed extraction modified the installed binary: %q", got)
	}
}

func TestVerifyAndInstallRefusesDowngrade(t *testing.T) {
	s, downloads := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)
	cfg.CurrentVersion = "99.0.0"

	rel := releaseFromFixture(t, fakeArtifact(t), "")
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

// Durability regression: a crash after replaceBinary must never leave a
// zero-length or missing executable. The temp file must be fsynced while it
// still holds the complete payload and before it is renamed over the target;
// the parent directory must be fsynced after the rename so the new directory
// entry survives a power loss. Both paths (Unix atomic rename and the Windows
// rename-via-.old strategy) must honour this ordering.
func TestReplaceBinarySyncsAroundRename(t *testing.T) {
	payload := []byte("new executable bytes")
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "golab")
			if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}

			var names []string
			tmpSyncedBeforeRename := false
			tmpHadFullPayload := false
			dirSyncedAfterRename := false

			prev := syncFile
			syncFile = func(f *os.File) error {
				name := f.Name()
				names = append(names, name)
				onDisk, _ := os.ReadFile(target)
				if strings.Contains(name, ".golab-update-") {
					tmpSyncedBeforeRename = string(onDisk) == "old"
					if data, err := os.ReadFile(name); err == nil {
						tmpHadFullPayload = bytes.Equal(data, payload)
					}
				} else if name == dir {
					dirSyncedAfterRename = bytes.Equal(onDisk, payload)
				}
				return nil
			}
			t.Cleanup(func() { syncFile = prev })

			if err := replaceBinary(target, payload, goos, 0o755); err != nil {
				t.Fatalf("replaceBinary: %v", err)
			}
			if !tmpHadFullPayload {
				t.Fatalf("temp file was never synced with the full payload; sync calls = %v", names)
			}
			if !tmpSyncedBeforeRename {
				t.Fatalf("temp file was not synced before the rename; sync calls = %v", names)
			}
			if !dirSyncedAfterRename {
				t.Fatalf("parent directory was not synced after the rename; sync calls = %v", names)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("installed binary = %q, want %q", got, payload)
			}
		})
	}
}

// A failed fsync on the temp file must be reported and must abort before the
// rename, leaving the currently installed binary untouched.
func TestReplaceBinaryReportsTempSyncError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "golab")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := syncFile
	dirSynced := false
	syncFile = func(f *os.File) error {
		if strings.Contains(f.Name(), ".golab-update-") {
			return errors.New("simulated fsync failure")
		}
		if f.Name() == dir {
			dirSynced = true
		}
		return nil
	}
	t.Cleanup(func() { syncFile = prev })

	err := replaceBinary(target, []byte("new"), "linux", 0o755)
	if err == nil || !strings.Contains(err.Error(), "sync update temp file") {
		t.Fatalf("replaceBinary error = %v, want a sync update temp file error", err)
	}
	if dirSynced {
		t.Fatal("parent directory synced despite a failed temp-file sync")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Fatalf("failed sync modified the installed binary: %q", got)
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

// TestMaybeCheckWaitJoinsCallback pins the join contract the CLI relies on:
// wait must not return until the report callback (including its writer work)
// has finished, so no diagnostic can outlive the command.
func TestMaybeCheckWaitJoinsCallback(t *testing.T) {
	s, downloads := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)

	callbackDone := make(chan struct{})
	wait := MaybeCheck(context.Background(), cfg, func(*Release) {
		time.Sleep(20 * time.Millisecond)
		close(callbackDone)
	})
	wait()
	select {
	case <-callbackDone:
	default:
		t.Fatal("wait returned before the report callback finished")
	}
	if downloads.Load() < 1 {
		t.Fatalf("expected network traffic, got %d requests", downloads.Load())
	}
	// A second wait must be safe (the CLI may join from more than one place).
	wait()
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
