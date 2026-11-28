// Package account manages named profiles and their isolated local state.
//
// A legacy single-user install (one OAuth token plus one sessions.json) is
// adopted on first use as the "default" profile without moving or rewriting
// either file: the legacy keyring entry and fallback token remain the default
// account's token, and sessions.json remains its session store. Named accounts
// get their own token and session files (see names.go). No credential is ever
// included in an error or a log message.
package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/20Youssef10/golab/internal/platform"
)

const (
	// DefaultName is the profile adopted from a pre-accounts installation.
	DefaultName = "default"
	// FileName is the versioned profile registry in the config dir.
	FileName = "accounts.json"
	// CurrentVersion is stamped on every registry write.
	CurrentVersion = 1
)

var (
	// ErrCorrupt means the registry exists but cannot be parsed or validated.
	// It is never treated as empty: a corrupt file must abort, not reset.
	ErrCorrupt = errors.New("corrupt accounts registry")
	// ErrNotInitialized means no registry file exists yet.
	ErrNotInitialized = errors.New("accounts registry not initialized")
	// ErrUnsafeFile means the registry path is a symlink, directory or a file
	// with permissions that expose its contents.
	ErrUnsafeFile = errors.New("unsafe accounts registry")
	// ErrNotFound means the named profile does not exist.
	ErrNotFound = errors.New("account not found")
	// ErrExists means a profile with that name already exists.
	ErrExists = errors.New("account already exists")
	// ErrActive means the active profile needs an explicit override to remove.
	ErrActive = errors.New("account is active")
	// ErrLast means the final profile cannot be removed.
	ErrLast = errors.New("cannot remove the last account")
)

// Profile is one named account. Email may be empty until the first successful
// authentication. No token material is ever stored in the registry.
type Profile struct {
	Name      string   `json:"name"`
	Email     string   `json:"email,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	CreatedAt string   `json:"created_at"`
}

// File is the on-disk registry shape.
type File struct {
	Version  int       `json:"version"`
	Active   string    `json:"active"`
	Profiles []Profile `json:"profiles"`
}

// Registry is a locked, atomically-written accounts.json.
type Registry struct{ path string }

// Path returns the default registry path in the config dir.
func Path() (string, error) {
	d, err := platform.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, FileName), nil
}

// New creates the registry directory if needed. An empty path uses Path().
func New(path string) (*Registry, error) {
	if path == "" {
		var err error
		path, err = Path()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &Registry{path: path}, nil
}

// Path returns the underlying file path.
func (r *Registry) Path() string { return r.path }

func (r *Registry) locked(fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lock := flock.New(r.path + ".lock")
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("timed out acquiring accounts lock for %s", r.path)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func defaultFile() File {
	return File{
		Version: CurrentVersion,
		Active:  DefaultName,
		Profiles: []Profile{{
			Name:      DefaultName,
			Provider:  "oauth2",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}},
	}
}

// Ensure loads the registry, adopting a legacy install as the default profile
// when no registry exists. A corrupt or newer-version registry aborts.
func (r *Registry) Ensure() (File, error) {
	var out File
	err := r.locked(func() error {
		f, err := r.loadLocked()
		if errors.Is(err, ErrNotInitialized) {
			out = defaultFile()
			return r.writeLocked(out)
		}
		if err != nil {
			return err
		}
		out = f
		return nil
	})
	return out, err
}

// Load returns the current registry. A missing registry is ErrNotInitialized;
// callers that want migration should use Ensure.
func (r *Registry) Load() (File, error) {
	var out File
	err := r.locked(func() error {
		var err error
		out, err = r.loadLocked()
		return err
	})
	return out, err
}

func (r *Registry) loadLocked() (File, error) {
	fi, err := os.Lstat(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, ErrNotInitialized
	}
	if err != nil {
		return File{}, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return File{}, fmt.Errorf("%w: %s", ErrUnsafeFile, r.path)
	}
	b, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, ErrNotInitialized
	}
	if err != nil {
		return File{}, err
	}
	after, err := os.Lstat(r.path)
	if err != nil {
		return File{}, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(fi, after) {
		return File{}, fmt.Errorf("%w: %s changed during read", ErrUnsafeFile, r.path)
	}
	if runtime.GOOS != "windows" {
		if err := platform.MakePrivate(r.path); err != nil {
			return File{}, err
		}
		final, err := os.Lstat(r.path)
		if err != nil {
			return File{}, err
		}
		if final.Mode().Perm()&0o077 != 0 {
			return File{}, fmt.Errorf("%w: %s mode %o", ErrUnsafeFile, r.path, final.Mode().Perm())
		}
	}
	if strings.TrimSpace(string(b)) == "" {
		return File{}, fmt.Errorf("%w: %s is empty", ErrCorrupt, r.path)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("%w: %s: %w", ErrCorrupt, r.path, err)
	}
	if err := validateFile(f); err != nil {
		return File{}, err
	}
	return f, nil
}

func validateFile(f File) error {
	if f.Version < 0 || f.Version > CurrentVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrCorrupt, f.Version)
	}
	if len(f.Profiles) == 0 {
		return fmt.Errorf("%w: no profiles", ErrCorrupt)
	}
	seen := make(map[string]bool, len(f.Profiles))
	for _, p := range f.Profiles {
		if ValidateName(p.Name) != nil {
			return fmt.Errorf("%w: invalid profile name", ErrCorrupt)
		}
		if seen[p.Name] {
			return fmt.Errorf("%w: duplicate profile %q", ErrCorrupt, p.Name)
		}
		seen[p.Name] = true
	}
	if !seen[f.Active] {
		return fmt.Errorf("%w: active profile %q does not exist", ErrCorrupt, f.Active)
	}
	return nil
}

// writeLocked publishes a complete registry with temp + fsync + rename.
func (r *Registry) writeLocked(f File) error {
	f.Version = CurrentVersion
	sort.Slice(f.Profiles, func(i, j int) bool { return f.Profiles[i].Name < f.Profiles[j].Name })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".golab-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(r.path)
	}
	if err := os.Rename(name, r.path); err != nil {
		return err
	}
	if err := platform.MakePrivate(r.path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// List returns profiles sorted by name.
func (r *Registry) List() ([]Profile, error) {
	f, err := r.Load()
	if err != nil {
		return nil, err
	}
	out := append([]Profile(nil), f.Profiles...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one profile.
func (r *Registry) Get(name string) (Profile, error) {
	f, err := r.Load()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range f.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, ErrNotFound
}

// Active returns the name of the active profile.
func (r *Registry) Active() (string, error) {
	f, err := r.Load()
	if err != nil {
		return "", err
	}
	return f.Active, nil
}

// Add stores a new profile. It does not change the active profile.
func (r *Registry) Add(p Profile) error {
	if err := ValidateName(p.Name); err != nil {
		return err
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return r.locked(func() error {
		f, err := r.loadLocked()
		if err != nil {
			return err
		}
		for _, existing := range f.Profiles {
			if existing.Name == p.Name {
				return fmt.Errorf("%w: %s", ErrExists, p.Name)
			}
		}
		f.Profiles = append(f.Profiles, p)
		return r.writeLocked(f)
	})
}

// SetActive selects an existing profile.
func (r *Registry) SetActive(name string) error {
	return r.locked(func() error {
		f, err := r.loadLocked()
		if err != nil {
			return err
		}
		found := false
		for _, p := range f.Profiles {
			if p.Name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		f.Active = name
		return r.writeLocked(f)
	})
}

// Update applies fn to an existing profile without losing its other metadata.
func (r *Registry) Update(name string, fn func(*Profile)) error {
	return r.locked(func() error {
		f, err := r.loadLocked()
		if err != nil {
			return err
		}
		found := false
		for i := range f.Profiles {
			if f.Profiles[i].Name != name {
				continue
			}
			found = true
			before := f.Profiles[i]
			fn(&f.Profiles[i])
			if f.Profiles[i].Name != name {
				return errors.New("account update must not change the profile name")
			}
			_ = before
			break
		}
		if !found {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return r.writeLocked(f)
	})
}

// Remove deletes a profile. The active profile needs force=true; the last
// remaining profile can never be removed. Removing the active profile falls
// back to the first remaining one so active is never dangling.
func (r *Registry) Remove(name string, force bool) error {
	return r.locked(func() error {
		f, err := r.loadLocked()
		if err != nil {
			return err
		}
		if len(f.Profiles) <= 1 {
			return ErrLast
		}
		found := -1
		for i, p := range f.Profiles {
			if p.Name == name {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		if f.Active == name && !force {
			return fmt.Errorf("%w: %s", ErrActive, name)
		}
		f.Profiles = append(f.Profiles[:found], f.Profiles[found+1:]...)
		if f.Active == name {
			// Prefer the legacy default profile as the fallback; it is the
			// predictable home for a signed-in single-user install.
			f.Active = f.Profiles[0].Name
			for _, p := range f.Profiles {
				if p.Name == DefaultName {
					f.Active = DefaultName
					break
				}
			}
		}
		return r.writeLocked(f)
	})
}
