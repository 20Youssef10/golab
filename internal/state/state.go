// Package state persists sessions and settings with the Python CLI's on-disk
// schema, but with cross-process locking, atomic writes and loud failure on a
// corrupt file (fixes defects D-1 and D-2 in docs/01 §10).
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
)

var (
	// ErrCorrupt means the state file exists but cannot be parsed. It must
	// never be treated as an empty store.
	ErrCorrupt = errors.New("corrupt state file")
	// ErrUnsafeFile means the state path is not a regular, owner-only file
	// (e.g. a symbolic link or a directory). It must be refused, never read.
	ErrUnsafeFile = errors.New("unsafe state file")
	// ErrNotFound means the requested session does not exist.
	ErrNotFound = errors.New("session not found")
)

// LastExecution mirrors the Python `last_execution` tuple.
type LastExecution struct {
	Kind  string  `json:"kind"`
	Value *string `json:"value,omitempty"`
	At    string  `json:"at"`
}

// PendingExecution records an execution that may still be running after a
// transport interruption. It is written before Execute and cleared on a
// terminal success, using the same atomic session store as every other field.
// Recovery is necessarily partial: output emitted while the socket was down
// cannot be replayed by this client.
type PendingExecution struct {
	MsgID      string    `json:"msg_id"`
	SessionID  string    `json:"session_id"`
	Delivered  int       `json:"delivered"`
	StartedAt  time.Time `json:"started_at"`
	CodeSHA256 string    `json:"code_sha256"`
}

// Session is the JSON schema of one entry in sessions.json.
type Session struct {
	Name     string      `json:"name"`
	Token    logx.Secret `json:"token"`
	URL      string      `json:"url"`
	Endpoint string      `json:"endpoint"`
	// RuntimeName is the Public API resource name ("runtimes/r-…"). It is
	// optional so legacy sessions.json files (which only have Endpoint) still
	// deserialize, and is omitted from the file when empty.
	RuntimeName    string            `json:"runtime_name,omitempty"`
	TokenExpiresAt *time.Time        `json:"token_expires_at,omitempty"`
	Variant        string            `json:"variant,omitempty"`
	Accelerator    string            `json:"accelerator,omitempty"`
	MachineShape   string            `json:"machine_shape,omitempty"`
	KernelID       string            `json:"kernel_id,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
	LastExecution  *LastExecution    `json:"last_execution,omitempty"`
	Running        *string           `json:"running,omitempty"`
	Pending        *PendingExecution `json:"pending,omitempty"`
}

// Settings is the JSON schema of settings.json.
type Settings struct {
	UpdateURL          string     `json:"update_url,omitempty"`
	LastCheck          *time.Time `json:"last_check,omitempty"`
	EnableUpdateCheck  bool       `json:"enable_update_check"`
	LatestVersion      string     `json:"latest_version,omitempty"`
	UsePublicAPI       bool       `json:"use_public_api,omitempty"`
	PublicAPISupported *bool      `json:"public_api_supported,omitempty"`
	TunnelAPISupported *bool      `json:"tunnel_api_supported,omitempty"`
}

// DefaultSettings returns the Python CLI's defaults.
func DefaultSettings() Settings {
	return Settings{
		UpdateURL:         "https://pypi.org/pypi/google-colab-cli/json",
		EnableUpdateCheck: true,
	}
}

// Store is a locked, atomically-written sessions.json.
type Store struct{ path string }

// NewStore creates the store directory if needed.
func NewStore(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = platform.SessionsPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &Store{path: path}, nil
}

// Path returns the underlying file path.
func (s *Store) Path() string { return s.path }

func (s *Store) locked(ctx context.Context, fn func() error) error {
	lock := flock.New(s.path + ".lock")
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("timed out acquiring state lock for %s", s.path)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

// List returns all sessions, or an empty map if the file does not exist yet.
func (s *Store) List() (map[string]Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out map[string]Session
	err := s.locked(ctx, func() error {
		var err error
		out, err = readSessions(s.path)
		return err
	})
	return out, err
}

// Get returns one session or ErrNotFound.
func (s *Store) Get(name string) (*Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out *Session
	err := s.locked(ctx, func() error {
		sessions, err := readSessions(s.path)
		if err != nil {
			return err
		}
		v, ok := sessions[name]
		if !ok {
			return ErrNotFound
		}
		out = &v
		return nil
	})
	return out, err
}

// MatchAssignment reports whether a locally stored session corresponds to a
// server assignment. The tunnel API identifies an assignment by Endpoint
// ("m-s-…"); the Public API uses the resource name ("runtimes/r-…"). Either
// identity is sufficient (docs/public_api_migration_plan.md §4).
func MatchAssignment(sess Session, endpoint, runtimeName string) bool {
	return (endpoint != "" && sess.Endpoint == endpoint) ||
		(runtimeName != "" && sess.RuntimeName == runtimeName)
}

// FindByRuntimeName locates a persisted session by its Public API runtime name.
func (s *Store) FindByRuntimeName(name string) (*Session, bool) {
	sessions, err := s.List()
	if err != nil || name == "" {
		return nil, false
	}
	for _, sess := range sessions {
		if sess.RuntimeName == name {
			found := sess
			return &found, true
		}
	}
	return nil, false
}

// Put inserts or replaces a session.
func (s *Store) Put(v Session) error {
	if err := ValidateName(v.Name); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.locked(ctx, func() error {
		sessions, err := readSessions(s.path)
		if err != nil {
			return err
		}
		sessions[v.Name] = v
		// tier6 hunk: stamp the additive schema version on every write.
		return writeJSON(s.path, sessionFile{SchemaVersion: CurrentSchemaVersion, Sessions: sessions})
	})
}

// Delete removes a session. Deleting a missing session is a no-op.
func (s *Store) Delete(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.locked(ctx, func() error {
		sessions, err := readSessions(s.path)
		if err != nil {
			return err
		}
		if _, ok := sessions[name]; !ok {
			return nil
		}
		delete(sessions, name)
		// tier6 hunk: preserve the version stamp across deletes.
		return writeJSON(s.path, sessionFile{SchemaVersion: CurrentSchemaVersion, Sessions: sessions})
	})
}

// SettingsStore is the locked, atomically-written settings.json.
type SettingsStore struct{ path string }

// NewSettingsStore creates the store.
func NewSettingsStore(path string) (*SettingsStore, error) {
	if path == "" {
		var err error
		path, err = platform.SettingsPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &SettingsStore{path: path}, nil
}

func (s *SettingsStore) locked(ctx context.Context, fn func() error) error {
	lock := flock.New(s.path + ".lock")
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("timed out acquiring settings lock for %s", s.path)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

// Load returns settings, or defaults when the file does not exist.
func (s *SettingsStore) Load() (Settings, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out Settings
	err := s.locked(ctx, func() error {
		fi, err := statStateFile(s.path)
		if errors.Is(err, os.ErrNotExist) {
			out = DefaultSettings()
			return nil
		}
		if err != nil {
			return err
		}
		b, err := os.ReadFile(s.path)
		if errors.Is(err, os.ErrNotExist) {
			out = DefaultSettings()
			return nil
		}
		if err != nil {
			return err
		}
		if err := checkStateFileUnchanged(s.path, fi); err != nil {
			return err
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			out = DefaultSettings()
			return nil
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrCorrupt, s.path, err)
		}
		return nil
	})
	return out, err
}

// Save persists settings atomically.
func (s *SettingsStore) Save(v Settings) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.locked(ctx, func() error { return writeJSON(s.path, v) })
}

// statStateFile validates a state file before it is read. A missing file is
// reported as os.ErrNotExist so callers keep their empty/default semantics;
// anything other than a regular, owner-only file is refused with
// ErrUnsafeFile. Lstat (not Stat) is required so symlinks are rejected instead
// of followed. All callers hold the corresponding store's flock.
func statStateFile(p string) (os.FileInfo, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s: symbolic link", ErrUnsafeFile, p)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s: not a regular file", ErrUnsafeFile, p)
	}
	// Self-heal legacy or hand-created files with group/other bits.
	if fi.Mode().Perm()&0o077 != 0 {
		if err := platform.MakePrivate(p); err != nil {
			return nil, err
		}
	}
	return fi, nil
}

// checkStateFileUnchanged closes the gap between Lstat and ReadFile: the path
// must still resolve to the same inode, and still be a private regular file,
// after the bytes have been read. Refuse before self-healing — never chmod a
// path that has not just been re-validated.
func checkStateFileUnchanged(p string, before os.FileInfo) error {
	after, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s: changed during read", ErrUnsafeFile, p)
	}
	if err != nil {
		return err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return fmt.Errorf("%w: %s: changed during read", ErrUnsafeFile, p)
	}
	if after.Mode().Perm()&0o077 != 0 {
		return platform.MakePrivate(p)
	}
	return nil
}

func readSessions(p string) (map[string]Session, error) {
	fi, err := statStateFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkStateFileUnchanged(p, fi); err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]Session{}, nil
	}
	// tier6 hunk: decode the additive schema_version key without treating it
	// as a session. The version type itself lives in schema.go.
	var file sessionFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrCorrupt, p, err)
	}
	if file.Sessions == nil {
		file.Sessions = map[string]Session{}
	}
	return file.Sessions, nil
}

// syncParentDir flushes the directory entry created by os.Rename. Without it
// the data file can survive a crash while the rename that published it does
// not, leaving the old file (or nothing) at p.
func syncParentDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// writeJSON writes to a temp file in the same directory, fsyncs, keeps a .bak
// of the previous good file and renames into place.
func writeJSON(p string, v any) error {
	return writeJSONWithDirSync(p, v, syncParentDir)
}

// writeJSONWithDirSync is writeJSON with an injectable parent-dir sync, used
// by tests to observe the rename durability step.
func writeJSONWithDirSync(p string, v any, syncDir func(string) error) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(p)
	f, err := os.CreateTemp(dir, ".golab-tmp-*")
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
	if _, err := os.Stat(p); err == nil && runtime.GOOS != "windows" {
		_ = os.Link(p, p+".bak")
		_ = platform.MakePrivate(p + ".bak")
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	_ = platform.MakePrivate(p)
	// Best effort: the data file is already durable, and some filesystems
	// (notably on Windows) refuse to fsync a directory handle. A failure here
	// must not turn an otherwise successful write into an error.
	if syncDir != nil {
		_ = syncDir(dir)
	}
	return nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ValidateName enforces docs/03 §6 (fixes the path-traversal defect D-2).
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid session name %q: expected ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$", name)
	}
	// tier6 hunk: reserve the top-level metadata key so a session cannot
	// shadow schema_version when the store is written.
	if name == schemaVersionKey {
		return fmt.Errorf("invalid session name %q: reserved for state metadata", name)
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if windowsReserved[base] {
		return fmt.Errorf("invalid session name %q: Windows reserved device name", name)
	}
	return nil
}
