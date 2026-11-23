package history

import (
	"bufio"
	"bytes"
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
	"github.com/20Youssef10/golab/internal/state"
)

// Logger is a per-session JSONL event log. Construct one with New (default
// history dir) or NewAt (explicit dir, useful for tests and --config).
type Logger struct {
	dir  string
	name string
	path string
}

// New returns a logger for name in the platform history directory.
func New(name string) (*Logger, error) {
	dir, err := platform.HistoryDir()
	if err != nil {
		return nil, err
	}
	return NewAt(dir, name)
}

// NewAt returns a logger for name under dir, creating the directory if
// needed. Session names are validated with state.ValidateName (fixes D-2).
func NewAt(dir, name string) (*Logger, error) {
	if err := state.ValidateName(name); err != nil {
		return nil, err
	}
	if dir == "" {
		var err error
		dir, err = platform.HistoryDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Upstream makes existing log files private at startup; do the same.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			_ = platform.MakePrivate(filepath.Join(dir, entry.Name()))
		}
	}
	return &Logger{dir: dir, name: name, path: filepath.Join(dir, name+".jsonl")}, nil
}

// Path returns the JSONL file path.
func (l *Logger) Path() string { return l.path }

// Session returns the validated session name.
func (l *Logger) Session() string { return l.name }

// Append adds one event with the current UTC timestamp. It uses a 10 s lock
// timeout; use AppendContext to supply your own deadline.
func (l *Logger) Append(eventType string, payload any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return l.AppendContext(ctx, eventType, payload)
}

// AppendContext adds one event, locking across processes and rewriting the
// log atomically (temp file, fsync, rename) like internal/state.
func (l *Logger) AppendContext(ctx context.Context, eventType string, payload any) error {
	body, err := redactedPayload(payload)
	if err != nil {
		return err
	}
	event := Event{
		Timestamp: time.Now().UTC(),
		EventType: eventType,
		Payload:   body,
	}
	return l.locked(ctx, func() error {
		events, err := readHistory(l.path)
		if err != nil {
			return err
		}
		events = append(events, event)
		return writeHistory(l.path, events)
	})
}

// Read returns all events in chronological order. It uses a 10 s lock
// timeout; use ReadContext to supply your own deadline.
func (l *Logger) Read() ([]Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return l.ReadContext(ctx)
}

// ReadContext returns all events while holding the cross-process lock.
func (l *Logger) ReadContext(ctx context.Context) ([]Event, error) {
	var out []Event
	err := l.locked(ctx, func() error {
		var err error
		out, err = readHistory(l.path)
		return err
	})
	return out, err
}

func (l *Logger) locked(ctx context.Context, fn func() error) error {
	lock := flock.New(l.path + ".lock")
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("timed out acquiring history lock for %s", l.path)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

// ListSessions returns the session names that have a JSONL log in dir.
func ListSessions(dir string) ([]string, error) {
	if dir == "" {
		var err error
		dir, err = platform.HistoryDir()
		if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".jsonl") {
			out = append(out, strings.TrimSuffix(name, ".jsonl"))
		}
	}
	sort.Strings(out)
	return out, nil
}

func readHistory(path string) ([]Event, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	scan := bufio.NewScanner(bytes.NewReader(b))
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var out []Event
	lineNo := 0
	for scan.Scan() {
		lineNo++
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("%w: %s:%d: %w", ErrCorrupt, path, lineNo, err)
		}
		out = append(out, event)
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func writeHistory(path string, events []Event) error {
	var buf bytes.Buffer
	for _, event := range events {
		b, err := json.Marshal(event)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return atomicWrite(path, buf.Bytes())
}

// atomicWrite writes to a temp file in the same directory, fsyncs, keeps a
// .bak of the last good file and renames into place (docs/03 ADR-004).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".golab-history-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
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
	if _, err := os.Stat(path); err == nil && runtime.GOOS != "windows" {
		_ = os.Link(path, path+".bak")
		_ = platform.MakePrivate(path + ".bak")
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return platform.MakePrivate(path)
}
