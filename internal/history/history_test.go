package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func rawPayload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestLogger(t *testing.T, name string) *Logger {
	t.Helper()
	l, err := NewAt(filepath.Join(t.TempDir(), "history"), name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func field(t *testing.T, e Event, key string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatal(err)
	}
	return m[key]
}

// Ports upstream tests/test_history.py::TestHistory::test_log_and_get_history.
func TestLogAndGetHistory(t *testing.T) {
	l := newTestLogger(t, "test-session")
	if err := l.Append(EventSessionCreated, map[string]any{"variant": "DEFAULT"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(EventExecution, map[string]any{"code": "print(1)", "outputs": []any{}}); err != nil {
		t.Fatal(err)
	}

	h, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 {
		t.Fatalf("history length = %d, want 2", len(h))
	}
	if h[0].EventType != EventSessionCreated || h[1].EventType != EventExecution {
		t.Fatalf("event types = %q, %q", h[0].EventType, h[1].EventType)
	}
	if got := field(t, h[0], "variant"); got != "DEFAULT" {
		t.Errorf("variant = %v", got)
	}
	if got := field(t, h[1], "code"); got != "print(1)" {
		t.Errorf("code = %v", got)
	}
	if h[0].Timestamp.IsZero() || h[1].Timestamp.Before(h[0].Timestamp) {
		t.Fatalf("timestamps out of order: %v, %v", h[0].Timestamp, h[1].Timestamp)
	}

	// Upstream flattens payload keys into the top-level JSON object; keep that.
	b, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl lines = %d", len(lines))
	}
	var top map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &top); err != nil {
		t.Fatal(err)
	}
	if top["event_type"] != "execution" || top["code"] != "print(1)" {
		t.Fatalf("flattened jsonl = %s", lines[1])
	}
	if _, ok := top["payload"]; ok {
		t.Fatalf("payload must be flattened, got nested payload: %s", lines[1])
	}
	if !strings.Contains(lines[0], "+00:00") {
		t.Fatalf("timestamp should use UTC ISO-8601 with +00:00 like Python: %s", lines[0])
	}
}

// Ports upstream tests/test_history.py::TestHistory::test_list_sessions.
func TestListSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	for _, name := range []string{"s1", "s2", "s3"} {
		l, err := NewAt(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Append("event", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	// Unrelated files and lock files must not be listed as sessions.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %v, want 3", sessions)
	}
	for _, name := range []string{"s1", "s2", "s3"} {
		found := false
		for _, s := range sessions {
			if s == name {
				found = true
			}
		}
		if !found {
			t.Errorf("%q missing from %v", name, sessions)
		}
	}
}

// Ports the corrupt-file regression of docs/01 D-1 to the JSONL log: a
// truncated or malformed line aborts instead of being silently skipped.
func TestCorruptAndTruncatedLines(t *testing.T) {
	l := newTestLogger(t, "corrupt")
	good := `{"timestamp":"2026-03-23T12:00:00+00:00","event_type":"execution","code":"x=1"}` + "\n"
	bad := `{"timestamp":"2026-03-23T12:01:00+00:00","event_type":"execution"` // truncated

	if err := os.WriteFile(l.Path(), []byte(good+bad+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Read(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Read: expected ErrCorrupt, got %v", err)
	}
	if err := l.Append(EventExecution, map[string]any{"code": "y=2"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Append: expected ErrCorrupt, got %v", err)
	}
	b, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "y=2") {
		t.Fatalf("append to corrupt history must not modify it: %s", b)
	}

	// Blank/whitespace-only lines are ignored (as upstream does).
	if err := os.WriteFile(l.Path(), []byte("\n"+good+"  \n"+good), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 {
		t.Fatalf("history = %d, want 2", len(h))
	}
}

// Ports the cross-process atomicity requirement of docs/03 ADR-004 to history:
// concurrent appenders must never lose or interleave events.
func TestConcurrentAppendAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	const writers, perWriter = 8, 10
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, err := NewAt(dir, "shared")
			if err != nil {
				t.Errorf("NewAt: %v", err)
				return
			}
			for j := 0; j < perWriter; j++ {
				if err := l.Append(EventExecution, map[string]any{"i": i, "j": j}); err != nil {
					t.Errorf("append %d/%d: %v", i, j, err)
				}
			}
		}(i)
	}
	wg.Wait()

	l, err := NewAt(dir, "shared")
	if err != nil {
		t.Fatal(err)
	}
	h, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != writers*perWriter {
		t.Fatalf("history length = %d, want %d", len(h), writers*perWriter)
	}
	seen := map[string]bool{}
	for _, e := range h {
		i := field(t, e, "i").(float64)
		j := field(t, e, "j").(float64)
		key := fmt.Sprintf("%v/%v", i, j)
		if seen[key] {
			t.Errorf("duplicate event %s", key)
		}
		seen[key] = true
	}
	// .bak uses hard links and is intentionally skipped on Windows.
	if goruntime.GOOS != "windows" {
		if _, err := os.Stat(l.Path() + ".bak"); err != nil {
			t.Errorf("no .bak of last good file: %v", err)
		}
	}
}

// Ports the D-2 path-traversal regression of docs/01 §10 (names come straight
// from the CLI into the history path upstream).
func TestSessionNameValidation(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"s1", "a.b_c-d", strings.Repeat("x", 64)} {
		if _, err := NewAt(dir, name); err != nil {
			t.Errorf("%q should be valid: %v", name, err)
		}
	}
	for _, name := range []string{"", ".hidden", "../escape", "a/b", "CON", strings.Repeat("x", 65)} {
		if _, err := NewAt(dir, name); err == nil {
			t.Errorf("%q should be invalid", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escape.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path traversal created a file outside history dir: %v", err)
	}
}

// History payloads are free-form; the logger must defensively redact secrets
// (docs/09 rule 7 / AGENTS.md security rules).
func TestAppendRedactsSecrets(t *testing.T) {
	l := newTestLogger(t, "redact")
	payload := map[string]any{
		"code":          "curl 'https://x/?colab-runtime-proxy-token=abcdefghijk'",
		"token":         "supersecrettoken",
		"Authorization": "Bearer abcdefghijk",
		"nested":        map[string]any{"refresh_token": "anothersecret"},
	}
	if err := l.Append(EventExecution, payload); err != nil {
		t.Fatal(err)
	}
	h, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 1 {
		t.Fatalf("history length = %d", len(h))
	}
	b, _ := json.Marshal(h[0].Payload)
	got := string(b)
	for _, secret := range []string{"supersecrettoken", "abcdefghijk", "anothersecret"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret leaked into history: %s", got)
		}
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("expected redaction marker: %s", got)
	}
	// The user's code itself must survive (only the token in the URL is redacted).
	if !strings.Contains(got, "curl") {
		t.Errorf("benign code was discarded: %s", got)
	}
	// The caller's map must not be mutated.
	if payload["token"] != "supersecrettoken" {
		t.Fatalf("caller payload mutated: %v", payload)
	}
}
