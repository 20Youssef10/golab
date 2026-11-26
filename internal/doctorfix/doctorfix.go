// Package doctorfix plans and applies local remediations for `doctor --fix`.
//
// It is strictly local: no network, no GCP mutation, no permission escalation.
// Inspect is read-only. Apply touches ONLY safe, reversible operations:
// narrowing file modes, renaming an unlocked stale lock aside, and renaming a
// legacy token aside when a Go token-store file proves migration happened.
// Nothing is ever deleted and no unsafe fix can be applied.
package doctorfix

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gofrs/flock"
)

const staleLockAge = 24 * time.Hour

// Config carries resolved paths only; it performs no I/O itself.
type Config struct {
	ConfigDir       string
	SessionsPath    string
	SettingsPath    string
	OAuthTokenPath  string
	LegacyTokenPath string
	KnownHostsPath  string
}

// FS is the injectable filesystem seam. TryLock returns true when another
// process currently holds the advisory lock for that path.
type FS struct {
	Stat    func(string) (os.FileInfo, error)
	Chmod   func(string, os.FileMode) error
	Rename  func(string, string) error
	TryLock func(string) (bool, error)
	Now     func() time.Time
}

// DefaultFS uses the real filesystem and gofrs/flock with a short timeout so
// a live holder can never be renamed out from under itself.
func DefaultFS() FS {
	return FS{
		Stat:   os.Stat,
		Chmod:  func(p string, m os.FileMode) error { return os.Chmod(p, m) },
		Rename: os.Rename,
		TryLock: func(p string) (bool, error) {
			lock := flock.New(p)
			ok, err := lock.TryLockContext(context.Background(), 50*time.Millisecond)
			if err != nil {
				return false, err
			}
			if !ok {
				return true, nil // a live holder exists
			}
			_ = lock.Unlock()
			return false, nil
		},
		Now: time.Now,
	}
}

// Fix is one exact, human-readable remediation.
type Fix struct {
	ID        string      `json:"id"`
	Kind      string      `json:"kind"` // chmod | stale-lock | legacy-token
	Path      string      `json:"path"`
	Command   string      `json:"command"`
	Reason    string      `json:"reason"`
	Mode      os.FileMode `json:"mode,omitempty"`
	SafeApply bool        `json:"safe_apply"`
	Skipped   string      `json:"skipped,omitempty"`
}

// Result records what Apply actually did for one fix.
type Result struct {
	ID     string `json:"id"`
	Status string `json:"status"` // applied | skipped | failed
	Reason string `json:"reason,omitempty"`
}

func withDefaults(fs FS) FS {
	if fs.Stat == nil {
		fs.Stat = DefaultFS().Stat
	}
	if fs.Chmod == nil {
		fs.Chmod = DefaultFS().Chmod
	}
	if fs.Rename == nil {
		fs.Rename = DefaultFS().Rename
	}
	if fs.TryLock == nil {
		fs.TryLock = DefaultFS().TryLock
	}
	if fs.Now == nil {
		fs.Now = DefaultFS().Now
	}
	return fs
}

// Inspect produces a deterministic plan without changing anything.
func Inspect(ctx context.Context, cfg Config, fs FS) ([]Fix, error) {
	fs = withDefaults(fs)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var fixes []Fix
	type target struct {
		id, path string
		mode     os.FileMode
	}
	for _, t := range []target{
		{"config.dir.mode", cfg.ConfigDir, 0o700},
		{"state.sessions.mode", cfg.SessionsPath, 0o600},
		{"state.settings.mode", cfg.SettingsPath, 0o600},
		{"auth.token-file.mode", cfg.OAuthTokenPath, 0o600},
		{"auth.legacy-token.mode", cfg.LegacyTokenPath, 0o600},
	} {
		if t.path == "" {
			continue
		}
		info, err := fs.Stat(t.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0o077 == 0 {
			continue
		}
		fixes = append(fixes, Fix{
			ID: t.id, Kind: "chmod", Path: t.path,
			Command: fmt.Sprintf("chmod %03o %s", t.mode, t.path),
			Reason: fmt.Sprintf("%s is accessible by group or others (mode %03o)",
				filepath.Base(t.path), info.Mode().Perm()),
			Mode: t.mode, SafeApply: true,
		})
	}
	locks := []string{}
	for _, p := range []string{cfg.SessionsPath, cfg.SettingsPath, cfg.KnownHostsPath} {
		if p != "" {
			locks = append(locks, p+".lock")
		}
	}
	for i, lock := range locks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := fs.Stat(lock)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if fs.Now().Sub(info.ModTime()) < staleLockAge {
			continue
		}
		held, err := fs.TryLock(lock)
		if err != nil {
			return nil, err
		}
		if held {
			continue
		}
		fixes = append(fixes, Fix{
			ID: fmt.Sprintf("state.stale-lock.%d", i), Kind: "stale-lock", Path: lock,
			Command:   "mv " + lock + " " + lock + ".stale",
			Reason:    "lock file is older than 24h and no process holds it",
			SafeApply: true,
		})
	}
	if cfg.LegacyTokenPath != "" {
		if _, err := fs.Stat(cfg.LegacyTokenPath); err == nil {
			fix := Fix{
				ID: "auth.legacy-token", Kind: "legacy-token", Path: cfg.LegacyTokenPath,
				Command: "mv " + cfg.LegacyTokenPath + " " + cfg.LegacyTokenPath + ".golab-backup",
				Reason:  "legacy Python token.json is still present",
			}
			if _, err := fs.Stat(cfg.OAuthTokenPath); err == nil {
				fix.SafeApply = true
				fix.Reason += "; Go token store file exists, so a reversible rename is safe"
			} else {
				fix.Skipped = "no Go token store file: confirm the refresh token was migrated before removing this file"
			}
			fixes = append(fixes, fix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	sort.SliceStable(fixes, func(i, j int) bool {
		order := map[string]int{"chmod": 0, "stale-lock": 1, "legacy-token": 2}
		return order[fixes[i].Kind] < order[fixes[j].Kind]
	})
	return fixes, nil
}

// Apply performs ONLY the safe fixes. It never deletes a file, never widens
// permissions and never applies a fix that Inspect marked unsafe.
func Apply(ctx context.Context, fixes []Fix, fs FS) ([]Result, error) {
	fs = withDefaults(fs)
	results := make([]Result, 0, len(fixes))
	failures := 0
	for _, fix := range fixes {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if !fix.SafeApply {
			results = append(results, Result{ID: fix.ID, Status: "skipped", Reason: fix.Skipped})
			continue
		}
		var err error
		switch fix.Kind {
		case "chmod":
			err = fs.Chmod(fix.Path, fix.Mode)
		case "stale-lock":
			var held bool
			held, err = fs.TryLock(fix.Path)
			if err == nil && held {
				results = append(results, Result{ID: fix.ID, Status: "skipped", Reason: "lock is now held"})
				continue
			}
			if err == nil {
				err = fs.Rename(fix.Path, fix.Path+".stale")
			}
		case "legacy-token":
			err = fs.Rename(fix.Path, fix.Path+".golab-backup")
		default:
			err = fmt.Errorf("unknown fix kind %q", fix.Kind)
		}
		if err != nil {
			failures++
			results = append(results, Result{ID: fix.ID, Status: "failed", Reason: err.Error()})
			continue
		}
		results = append(results, Result{ID: fix.ID, Status: "applied"})
	}
	if failures > 0 {
		return results, fmt.Errorf("doctor --fix: %d operation(s) failed", failures)
	}
	return results, nil
}
