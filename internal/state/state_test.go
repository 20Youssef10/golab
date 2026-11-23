package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Ports upstream tests/test_state.py::test_add_get_remove (intent).
func TestStorePutGetListDelete(t *testing.T) {
	s := newTestStore(t)
	sess := Session{Name: "s1", Token: logx.NewSecret("tok"), URL: "https://x", Endpoint: "ep"}
	if err := s.Put(sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Token.Reveal() != "tok" || got.Endpoint != "ep" {
		t.Fatalf("got %+v", got)
	}
	all, err := s.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %v, %v", all, err)
	}
	if err := s.Delete("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Ports the D-1 regression: a corrupt file must abort, never become {}.
func TestCorruptFileAborts(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("List: expected ErrCorrupt, got %v", err)
	}
	if err := s.Put(Session{Name: "x"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Put: expected ErrCorrupt, got %v", err)
	}
}

// Ports the D-1 regression: a crash mid-write must not lose other sessions.
func TestConcurrentWritesFromSeparateStores(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	stores := make([]*Store, 8)
	for i := range stores {
		s, err := NewStore(p)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = s
	}
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				name := fmt.Sprintf("s%d_%d", i, j)
				if err := s.Put(Session{Name: name, Token: logx.NewSecret("t")}); err != nil {
					t.Errorf("put %s: %v", name, err)
				}
			}
		}(i, s)
	}
	wg.Wait()
	all, err := stores[0].List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 80 {
		t.Fatalf("got %d sessions, want 80", len(all))
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Errorf("no .bak created: %v", err)
	}
}

// Ports upstream tests/test_state.py::test_invalid_names (D-2).
func TestValidateName(t *testing.T) {
	for _, name := range []string{"s1", "a.b_c-d", strings.Repeat("x", 64)} {
		if err := ValidateName(name); err != nil {
			t.Errorf("%q should be valid: %v", name, err)
		}
	}
	for _, name := range []string{"", ".hidden", "../x", "a/b", "CON", strings.Repeat("x", 65)} {
		if err := ValidateName(name); err == nil {
			t.Errorf("%q should be invalid", name)
		}
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s, err := NewSettingsStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Load()
	if err != nil || !v.EnableUpdateCheck {
		t.Fatalf("defaults = %+v, %v", v, err)
	}
	v.UsePublicAPI = true
	if err := s.Save(v); err != nil {
		t.Fatal(err)
	}
	v2, err := s.Load()
	if err != nil || !v2.UsePublicAPI {
		t.Fatalf("reload = %+v, %v", v2, err)
	}
}
