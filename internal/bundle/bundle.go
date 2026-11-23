// Package bundle records reproducible execution provenance without a network
// call or a VM allocation. A bundle hashes code, environment, accelerator and
// the upstream compatibility version into a stable ID; verification recomputes
// the ID and can re-hash local files. Bundles are labelled "recorded", not
// "verified": kernel state, RNG, time and network make generic caching unsound.
package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// ErrSecretKey means an env key looked like a credential.
	ErrSecretKey = errors.New("bundle: secret-shaped environment key")
	// ErrUnsafePath means a file path escapes the bundle root or is absolute.
	ErrUnsafePath = errors.New("bundle: unsafe file path")
	// ErrMismatch means a recomputed digest differs from the recorded one.
	ErrMismatch = errors.New("bundle: provenance mismatch")
)

const schema = "golab/bundle/1"

var secretKeyRE = regexp.MustCompile(`(?i)(token|secret|password|passwd|authorization|cookie|verifier|credential|api[_-]?key)`)

// File is one local input to a bundle. Path must be relative to the bundle
// root; Content is hashed and never written to the manifest.
type File struct {
	Path    string
	Content []byte
}

// Input is the complete set of recorded inputs.
type Input struct {
	Files           []File
	Env             map[string]string
	Accelerator     string
	Variant         string
	Shape           string
	UpstreamVersion string
	GolabVersion    string
}

// FileRecord is the digest of one input file.
type FileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Provenance describes what the digests mean. It is constant so the manifest
// stays byte-identical across repeated recordings.
type Provenance struct {
	RecordedBy string `json:"recorded_by"`
	Note       string `json:"note"`
}

// Manifest is the on-disk provenance document.
type Manifest struct {
	Schema          string       `json:"schema"`
	ID              string       `json:"id"`
	CodeHash        string       `json:"code_hash"`
	EnvHash         string       `json:"env_hash"`
	Accelerator     string       `json:"accelerator"`
	Variant         string       `json:"variant"`
	Shape           string       `json:"shape"`
	UpstreamVersion string       `json:"upstream_version"`
	GolabVersion    string       `json:"golab_version"`
	Files           []FileRecord `json:"files"`
	Provenance      Provenance   `json:"provenance"`
}

// canonical is the ID input: every recorded field except the ID itself.
type canonical struct {
	Schema          string       `json:"schema"`
	CodeHash        string       `json:"code_hash"`
	EnvHash         string       `json:"env_hash"`
	Accelerator     string       `json:"accelerator"`
	Variant         string       `json:"variant"`
	Shape           string       `json:"shape"`
	UpstreamVersion string       `json:"upstream_version"`
	GolabVersion    string       `json:"golab_version"`
	Files           []FileRecord `json:"files"`
	Provenance      Provenance   `json:"provenance"`
}

func (m Manifest) canonical() canonical {
	return canonical{
		Schema: m.Schema, CodeHash: m.CodeHash, EnvHash: m.EnvHash,
		Accelerator: m.Accelerator, Variant: m.Variant, Shape: m.Shape,
		UpstreamVersion: m.UpstreamVersion, GolabVersion: m.GolabVersion,
		Files: m.Files, Provenance: m.Provenance,
	}
}

// Hash returns the lowercase hex SHA-256 of data.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// New computes a deterministic manifest from in. It performs no I/O.
func New(in Input) (*Manifest, error) {
	records := make([]FileRecord, 0, len(in.Files))
	for _, f := range in.Files {
		if err := validatePath(f.Path); err != nil {
			return nil, err
		}
		records = append(records, FileRecord{Path: filepath.ToSlash(f.Path), SHA256: Hash(f.Content)})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	var code strings.Builder
	for _, r := range records {
		code.WriteString(r.Path)
		code.WriteByte(0)
		code.WriteString(r.SHA256)
		code.WriteByte('\n')
	}
	envKeys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		if secretKeyRE.MatchString(k) {
			return nil, fmt.Errorf("%w: %q (value not reported)", ErrSecretKey, k)
		}
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	var env strings.Builder
	for _, k := range envKeys {
		env.WriteString(k)
		env.WriteByte('=')
		env.WriteString(in.Env[k])
		env.WriteByte('\n')
	}
	m := &Manifest{
		Schema:          schema,
		CodeHash:        Hash([]byte(code.String())),
		EnvHash:         Hash([]byte(env.String())),
		Accelerator:     in.Accelerator,
		Variant:         in.Variant,
		Shape:           in.Shape,
		UpstreamVersion: in.UpstreamVersion,
		GolabVersion:    in.GolabVersion,
		Files:           records,
		Provenance: Provenance{
			RecordedBy: "golab bundle record",
			Note:       "recorded provenance, not a verified replay; no cache by default",
		},
	}
	b, err := json.Marshal(m.canonical())
	if err != nil {
		return nil, err
	}
	m.ID = Hash(b)
	return m, nil
}

func validatePath(p string) error {
	if p == "" || filepath.IsAbs(p) {
		return fmt.Errorf("%w: %q", ErrUnsafePath, p)
	}
	clean := filepath.Clean(filepath.FromSlash(p))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q", ErrUnsafePath, p)
	}
	return nil
}

// Verify parses a manifest and recomputes its ID.
func Verify(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("bundle: decode manifest: %w", err)
	}
	if m.Schema != schema {
		return nil, fmt.Errorf("bundle: unsupported schema %q", m.Schema)
	}
	b, err := json.Marshal(m.canonical())
	if err != nil {
		return nil, err
	}
	if got := Hash(b); got != m.ID {
		return nil, fmt.Errorf("%w: recorded %s, recomputed %s", ErrMismatch, m.ID, got)
	}
	return &m, nil
}

// Read reads and verifies a manifest file.
func Read(ctx context.Context, path string) (*Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Verify(b)
}

// VerifyFiles re-hashes local inputs against the recorded digests.
func (m *Manifest) VerifyFiles(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, r := range m.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validatePath(r.Path); err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Path)))
		if err != nil {
			return fmt.Errorf("bundle: read %s: %w", r.Path, err)
		}
		if got := Hash(b); got != r.SHA256 {
			return fmt.Errorf("%w: %s changed (recorded %s, got %s)", ErrMismatch, r.Path, r.SHA256, got)
		}
	}
	return nil
}

// Write persists a manifest atomically with 0600 permissions.
func Write(ctx context.Context, path string, m *Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".golab-bundle-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
