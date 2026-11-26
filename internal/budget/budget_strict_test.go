package budget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Upstream parity: google-colab-cli profiles are plain YAML, so a document
// marker, quoted scalars, block scalars and flow mappings must all load. The
// old hand-rolled subset parser in budget.go rejected every one of these with
// ErrUnknownKey/ErrInvalidThreshold. Keep these cases table-driven so the
// accepted surface stays explicit.
func TestLoadProfileStrictYAML(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body, profile string
		want                Policy
	}{
		{
			name: "leading document marker",
			body: "---\nversion: 1\ndefaults:\n  budget:\n    max_balance: 100\n    max_hourly: 20\n",
			want: Policy{MaxBalance: 100, MaxHourly: 20},
		},
		{
			name: "quoted scalars",
			body: "version: \"1\"\ndefaults:\n  budget:\n    max_balance: \"100\"\n    enforce: \"true\"\n",
			want: Policy{MaxBalance: 100, Enforce: true},
		},
		{
			name: "block scalars",
			body: "version: 1\ndefaults:\n  budget:\n    max_balance: |\n      100\n    max_hourly: >-\n      20\n",
			want: Policy{MaxBalance: 100, MaxHourly: 20},
		},
		{
			name:    "inline maps",
			body:    "version: 1\ndefaults: {budget: {max_balance: 100, max_hourly: 20, enforce: true}}\nprofiles: {ci: {budget: {max_balance: 50}}}\n",
			profile: "ci",
			want:    Policy{MaxBalance: 50, MaxHourly: 20, Enforce: true},
		},
		{
			name: "native boolean with comment",
			body: "---\nversion: 1 # schema version\ndefaults:\n  budget:\n    max_balance: 100 # reserve floor\n    enforce: true\n",
			want: Policy{MaxBalance: 100, Enforce: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, sanitize(tc.name)+".yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadProfile(context.Background(), path, tc.profile)
			if err != nil {
				t.Fatalf("LoadProfile(%q) error = %v, want success", tc.profile, err)
			}
			if got != tc.want {
				t.Fatalf("LoadProfile(%q) = %+v, want %+v", tc.profile, got, tc.want)
			}
		})
	}
}

// A duplicate key used to be last-wins silently, which let a later line
// quietly override a user's reserve floor. Strict decoding must reject it.
func TestLoadProfileStrictYAMLRejects(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{
			name: "duplicate budget key",
			body: "version: 1\ndefaults:\n  budget:\n    max_balance: 100\n    max_balance: 200\n",
			want: ErrUnknownKey,
		},
		{
			name: "duplicate key in flow map",
			body: "version: 1\ndefaults: {budget: {max_balance: 100, max_balance: 200}}\n",
			want: ErrUnknownKey,
		},
		{
			name: "duplicate version",
			body: "version: 1\nversion: 1\n",
			want: ErrUnknownKey,
		},
		{
			name: "unknown top-level key",
			body: "version: 1\nlimits:\n  budget:\n    max_balance: 5\n",
			want: ErrUnknownKey,
		},
		{
			name: "unknown profile key",
			body: "version: 1\nprofiles:\n  ci:\n    notes: hello\n",
			want: ErrUnknownKey,
		},
		{
			name: "unknown defaults key",
			body: "version: 1\ndefaults:\n  limits:\n    max_balance: 5\n",
			want: ErrUnknownKey,
		},
		{
			name: "non-positive threshold",
			body: "version: 1\ndefaults:\n  budget:\n    max_balance: 0\n",
			want: ErrInvalidThreshold,
		},
		{
			name: "non-numeric quoted threshold",
			body: "version: 1\ndefaults:\n  budget:\n    max_hourly: \"lots\"\n",
			want: ErrInvalidThreshold,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, sanitize(tc.name)+".yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfile(context.Background(), path, ""); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func sanitize(name string) string {
	out := []rune(name)
	for i, r := range out {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			out[i] = '-'
		}
	}
	return string(out)
}
