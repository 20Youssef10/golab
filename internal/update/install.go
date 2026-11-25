package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Extraction failures are typed so callers can branch on them with errors.Is.
// They are declared here (rather than errors.go) to keep this fix local to the
// install path; verification sentinels are unchanged.
var (
	ErrInvalidArchive          = errors.New("release archive is not a valid tar.gz")
	ErrUnsafeArchivePath       = errors.New("release archive has an unsafe member path")
	ErrUnexpectedArchiveMember = errors.New("release archive has an unexpected member type")
	ErrBinaryNotFound          = errors.New("release archive has no executable")
	ErrDuplicateBinary         = errors.New("release archive has more than one executable")
	ErrEmptyBinary             = errors.New("release archive executable is empty")
)

// executablePath locates the binary a self-update replaces. It is a variable
// so tests can redirect the install into a temporary file without overwriting
// the running test binary; production always uses os.Executable. This mirrors
// the trustedMaterialLoader seam in trust.go and is never exposed via Config,
// so callers cannot redirect a real update.
var executablePath = os.Executable

// syncFile is the fsync seam used by replaceBinary. It is a variable so tests
// can assert the durability ordering (temp file synced before the rename,
// parent directory synced after) without relying on real disk behaviour.
// Production always calls File.Sync, including on a directory handle.
var syncFile = func(f *os.File) error { return f.Sync() }

// VerifyAndInstall downloads, verifies and installs a release. Verification
// (checksum AND cosign keyless signature bound to the pinned identity) is
// mandatory and happens before any filesystem mutation. A downgrade and any
// missing verification material are rejected before any network request.
func VerifyAndInstall(ctx context.Context, cfg Config, rel *Release) error {
	if rel == nil || rel.Version == "" || rel.URL == "" {
		return ErrBadRelease
	}
	if err := verifyNotDowngrade(cfg.CurrentVersion, rel.Version); err != nil {
		return err
	}
	if strings.TrimSpace(rel.Checksum) == "" {
		return ErrMissingChecksum
	}
	if strings.TrimSpace(rel.SignatureURL) == "" {
		return ErrMissingSignature
	}
	if strings.TrimSpace(rel.SBOMURL) == "" {
		return ErrMissingSBOM
	}

	artifact, err := getBytes(ctx, httpClient(cfg), rel.URL, maxArtifact)
	if err != nil {
		return err
	}
	if err := verifyArtifact(ctx, cfg, artifact, rel); err != nil {
		return err
	}
	sbomBundle, err := getBytes(ctx, httpClient(cfg), rel.SBOMURL, maxBundleBytes)
	if err != nil {
		return fmt.Errorf("fetch SBOM attestation: %w", err)
	}
	if err := verifySBOM(ctx, cfg, artifact, sbomBundle); err != nil {
		return err
	}

	// All verification above binds to the ARCHIVE bytes. Extraction happens
	// only afterwards and must never re-derive the signed digest from the
	// embedded executable: doing so would let an attacker swap the binary
	// inside an otherwise correctly signed release.
	binary, err := extractBinary(artifact, runtime.GOOS, maxArtifact)
	if err != nil {
		return err
	}

	target, err := executablePath()
	if err != nil {
		return fmt.Errorf("locate running executable: %w", err)
	}
	if err := replaceBinary(target, binary, runtime.GOOS, 0o755); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
}

// extractBinary returns the bytes of the executable inside a GoReleaser
// .tar.gz release asset. Only the single expected member is read into memory;
// no other archive member is ever written to disk. Member types and paths are
// checked before the name so that, for example, a symlink named "golab" can
// never be mistaken for the executable.
func extractBinary(archive []byte, goos string, limit int64) ([]byte, error) {
	want := "golab"
	if goos == "windows" {
		want = "golab.exe"
	}
	if len(archive) == 0 {
		return nil, fmt.Errorf("%w: empty archive", ErrInvalidArchive)
	}
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("%w: open gzip stream: %w", ErrInvalidArchive, err)
	}
	defer zr.Close()

	// A gzip bomb can expand far beyond the compressed size. Bound the
	// decompressed stream itself, not just the declared tar member sizes.
	tr := tar.NewReader(&boundedReader{r: zr, limit: limit})
	var binary []byte
	matches := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: read tar entries: %w", ErrInvalidArchive, err)
		}
		if !safeArchivePath(hdr.Name) {
			return nil, fmt.Errorf("%w: %q", ErrUnsafeArchivePath, hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			// Only regular files may supply executable bytes.
		default:
			return nil, fmt.Errorf("%w: %q has type %d", ErrUnexpectedArchiveMember, hdr.Name, hdr.Typeflag)
		}
		if hdr.Size > limit {
			return nil, fmt.Errorf("%w: member %q declares %d bytes", ErrTooLarge, hdr.Name, hdr.Size)
		}
		if hdr.Name != want {
			// Documentation members are skipped in memory, never extracted.
			if _, err := io.Copy(io.Discard, io.LimitReader(tr, hdr.Size)); err != nil {
				return nil, fmt.Errorf("%w: read tar member %q: %w", ErrInvalidArchive, hdr.Name, err)
			}
			continue
		}
		matches++
		if matches > 1 {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateBinary, want)
		}
		data, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil {
			return nil, fmt.Errorf("%w: read executable member: %w", ErrInvalidArchive, err)
		}
		if int64(len(data)) != hdr.Size {
			return nil, fmt.Errorf("%w: truncated executable member", ErrInvalidArchive)
		}
		binary = data
	}
	if matches == 0 {
		return nil, fmt.Errorf("%w: %q", ErrBinaryNotFound, want)
	}
	if len(binary) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrEmptyBinary, want)
	}
	return binary, nil
}

// safeArchivePath accepts only flat, relative, plain file names. GoReleaser
// emits archives with no directory components, so separators, traversal and
// absolute paths are all hostile regardless of the member's declared type.
func safeArchivePath(name string) bool {
	if name == "" || name == "." || name == ".." || path.IsAbs(name) {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	return true
}

// boundedReader fails once limit decompressed bytes have been read; it never
// silently truncates, so an overlong stream is reported as ErrTooLarge.
type boundedReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.read >= b.limit {
		return 0, fmt.Errorf("%w: decompressed archive exceeds %d bytes", ErrTooLarge, b.limit)
	}
	if remaining := b.limit - b.read; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	return n, err
}

// replaceBinary installs data at target. On Windows a running .exe cannot be
// overwritten, so it is renamed to .old first; the new binary takes its place
// and the .old file is removed on the next start by CleanupOld. The currently
// running executable is never deleted. On Unix an atomic rename over the target
// is sufficient and preserves the running process.
func replaceBinary(target string, data []byte, goos string, mode fs.FileMode) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".golab-update-*")
	if err != nil {
		return fmt.Errorf("create update temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the temp file has been renamed

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write update temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod update temp file: %w", err)
	}
	if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync update temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close update temp file: %w", err)
	}

	if goos == "windows" {
		old := target + ".old"
		// Remove a stale .old from a previous update first. This is never the
		// running executable, so it is safe to delete.
		if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale backup: %w", err)
		}
		if err := os.Rename(target, old); err != nil {
			return fmt.Errorf("rename running executable to backup: %w", err)
		}
		if err := os.Rename(tmpName, target); err != nil {
			// Roll back so the installation is not left without an executable.
			_ = os.Rename(old, target)
			return fmt.Errorf("install new executable: %w", err)
		}
		// Best-effort: persist the new directory entry after a successful rename.
		syncParentDir(dir)
		return nil
	}

	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("install new executable: %w", err)
	}
	// Best-effort: persist the renamed directory entry so a crash cannot
	// leave the binary missing even though its data blocks were synced.
	syncParentDir(dir)
	return nil
}

// syncParentDir fsyncs the directory holding a renamed executable so the
// directory entry itself reaches stable storage. It is best-effort: some
// platforms and filesystems reject Sync on a directory handle, and a failure
// here must not turn a completed install into an error.
func syncParentDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = syncFile(d)
}

// CleanupOld removes the .old backup left by a Windows self-update. The CLI
// composition root calls this at startup. It is idempotent and never removes
// the currently installed executable.
func CleanupOld(target string) error {
	err := os.Remove(target + ".old")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
