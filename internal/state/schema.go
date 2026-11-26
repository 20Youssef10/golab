package state

// Additive sessions.json schema versioning. The on-disk shape stays a flat
// map of session names (so existing tooling keeps working); schema_version is
// one reserved top-level numeric key. Legacy files without it read as version
// 0, corrupt files still abort, and migration only adds that key - it never
// rewrites or drops a session record, including unknown future fields.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// CurrentSchemaVersion is stamped on every new write.
	CurrentSchemaVersion = 1
	schemaVersionKey     = "schema_version"
)

// ErrNewerSchema means the file was written by a newer golab and this binary
// must refuse it rather than downgrade or silently drop unknown fields.
var ErrNewerSchema = errors.New("sessions.json schema version is newer than supported")

// sessionFile decodes both the legacy flat map and a versioned one.
type sessionFile struct {
	SchemaVersion int
	Sessions      map[string]Session
}

func (f sessionFile) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage, len(f.Sessions)+1)
	for name, s := range f.Sessions {
		b, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		m[name] = b
	}
	b, err := json.Marshal(f.SchemaVersion)
	if err != nil {
		return nil, err
	}
	m[schemaVersionKey] = b
	return json.Marshal(m)
}

func (f *sessionFile) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	f.Sessions = map[string]Session{}
	for name, blob := range raw {
		if name == schemaVersionKey {
			var v int
			if err := json.Unmarshal(blob, &v); err == nil {
				if v > CurrentSchemaVersion {
					return fmt.Errorf("%w: %w: %d > %d", ErrCorrupt, ErrNewerSchema, v, CurrentSchemaVersion)
				}
				f.SchemaVersion = v
				continue
			}
		}
		var s Session
		if err := json.Unmarshal(blob, &s); err != nil {
			return fmt.Errorf("session %q: %w", name, err)
		}
		f.Sessions[name] = s
	}
	return nil
}

// readRawSessions validates and parses the file without discarding unknown
// session fields. The caller must hold the store's lock.
func readRawSessions(p string) (map[string]json.RawMessage, error) {
	fi, err := statStateFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkStateFileUnchanged(p, fi); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return map[string]json.RawMessage{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrCorrupt, p, err)
	}
	if raw == nil {
		return map[string]json.RawMessage{}, nil
	}
	return raw, nil
}

func versionFromRaw(raw map[string]json.RawMessage) (int, error) {
	blob, ok := raw[schemaVersionKey]
	if !ok {
		return 0, nil
	}
	var v int
	if err := json.Unmarshal(blob, &v); err != nil {
		// A legacy session that happens to be named schema_version (an object,
		// not a number) is not metadata; treat it as version 0.
		return 0, nil
	}
	if v < 0 || v > CurrentSchemaVersion {
		return v, fmt.Errorf("%w: %w: %d", ErrCorrupt, ErrNewerSchema, v)
	}
	return v, nil
}

// SchemaVersion returns the persisted version, or 0 for a legacy file.
func (s *Store) SchemaVersion() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version int
	err := s.locked(ctx, func() error {
		raw, err := readRawSessions(s.path)
		if err != nil {
			return err
		}
		version, err = versionFromRaw(raw)
		return err
	})
	return version, err
}

// MigrationReport is a dry-run-friendly description of the upgrade.
type MigrationReport struct {
	Path       string   `json:"path"`
	From       int      `json:"from"`
	To         int      `json:"to"`
	Sessions   int      `json:"sessions"`
	WouldWrite bool     `json:"would_write"`
	Applied    bool     `json:"applied"`
	Notes      []string `json:"notes,omitempty"`
}

// Migrate adds schema_version to a legacy store. dryRun=true performs no
// write. A corrupt or newer-version file aborts; nothing is ever deleted.
func (s *Store) Migrate(dryRun bool) (MigrationReport, error) {
	rep := MigrationReport{Path: s.path, To: CurrentSchemaVersion}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.locked(ctx, func() error {
		raw, err := readRawSessions(s.path)
		if err != nil {
			return err
		}
		rep.From, err = versionFromRaw(raw)
		if err != nil {
			return err
		}
		rep.Sessions = len(raw)
		if _, numeric := raw[schemaVersionKey]; numeric {
			var probe int
			if json.Unmarshal(raw[schemaVersionKey], &probe) == nil {
				rep.Sessions--
			}
		}
		if rep.From == CurrentSchemaVersion {
			rep.Notes = append(rep.Notes, "already at current schema version; no write needed")
			return nil
		}
		rep.WouldWrite = true
		if dryRun {
			rep.Notes = append(rep.Notes, "dry run: no changes made")
			return nil
		}
		stamp := []byte(strconv.Itoa(CurrentSchemaVersion))
		raw[schemaVersionKey] = stamp
		if err := writeJSON(s.path, raw); err != nil {
			return err
		}
		rep.Applied = true
		rep.Notes = append(rep.Notes, "added schema_version; session records preserved verbatim")
		return nil
	})
	return rep, err
}
