package update

// Ported from upstream tests/test_update.py:
//   - test_auto_update_runs_when_stale / test_auto_update_skips_when_recent /
//     test_auto_update_runs_on_first_invocation  -> TestCheckThrottle
//   - test_pypi_no_upgrade / _is_newer PEP 440 cases -> TestIsNewer
//   - test_cached_banner_suppressed_when_update_check_disabled -> TestCheckDisabled
//   - test_check_does_not_downgrade_latest_version -> TestCheckNoDowngradeRelease
//   - quiet-subcommand handling -> TestMaybeCheck* (banner via callback only)

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckThrottle(t *testing.T) {
	t.Run("first invocation is due and fetches", func(t *testing.T) {
		s, downloads := newReleaseServer(t, releaseFixture{})
		defer s.Close()
		cfg := fakeConfig(s)

		rel, due, err := Check(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if !due {
			t.Fatal("expected due=true on first invocation")
		}
		if rel == nil || rel.Version != testVer {
			t.Fatalf("unexpected release: %+v", rel)
		}
		if downloads.Load() < 1 {
			t.Fatalf("expected at least one network request, got %d", downloads.Load())
		}
	})

	t.Run("recent check is not due and performs no network I/O", func(t *testing.T) {
		s, downloads := newReleaseServer(t, releaseFixture{})
		defer s.Close()
		cfg := fakeConfig(s)
		recent := time.Now().Add(-time.Hour)
		cfg.LastCheck = &recent

		rel, due, err := Check(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if due {
			t.Fatal("expected due=false when last check was 1h ago")
		}
		if rel != nil {
			t.Fatalf("expected nil release when not due, got %+v", rel)
		}
		if downloads.Load() != 0 {
			t.Fatalf("expected zero network calls when throttled, got %d", downloads.Load())
		}
	})

	t.Run("stale check is due again", func(t *testing.T) {
		s, downloads := newReleaseServer(t, releaseFixture{})
		defer s.Close()
		cfg := fakeConfig(s)
		stale := time.Now().Add(-25 * time.Hour)
		cfg.LastCheck = &stale

		_, due, err := Check(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if !due || downloads.Load() < 1 {
			t.Fatalf("expected due check with network traffic, got due=%v requests=%d", due, downloads.Load())
		}
	})

	t.Run("exactly 24h boundary is due", func(t *testing.T) {
		s, _ := newReleaseServer(t, releaseFixture{})
		defer s.Close()
		cfg := fakeConfig(s)
		boundary := time.Now().Add(-CheckInterval)
		cfg.LastCheck = &boundary

		_, due, err := Check(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if !due {
			t.Fatal("expected due=true at the 24h boundary")
		}
	})
}

func TestCheckDisabled(t *testing.T) {
	// Port of upstream test_cached_banner_suppressed_when_update_check_disabled:
	// enable_update_check=False is a global opt-out: no fetch, no banner.
	s, downloads := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)
	cfg.Enabled = false

	var reported *Release
	MaybeCheck(context.Background(), cfg, func(r *Release) { reported = r })

	rel, due, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if due || rel != nil || reported != nil || downloads.Load() != 0 {
		t.Fatalf("disabled update: due=%v rel=%v reported=%v downloads=%d", due, rel, reported, downloads.Load())
	}
}

func TestIsNewer(t *testing.T) {
	// Upstream _is_newer uses PEP 440; Go uses semver with identical ordering
	// for the cases that matter (pre-releases sort before their final release).
	cases := []struct {
		candidate string
		current   string
		want      bool
	}{
		{"1.1.0", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.1.0", false}, // PyPI older than installed: not newer
		{"1.1.0", "1.0.9", true},
		{"1.2.0-rc.1", "1.2.0", false}, // pre-release is NOT newer than the final
		{"1.2.0", "1.2.0-rc.1", true},  // final is newer than the pre-release
		{"1.2.0-beta.1", "1.2.0-alpha.1", true},
		{"", "1.0.0", false},
		{"not-a-version", "1.0.0", false},
		{"1.0.0", "not-a-version", false},
	}
	for _, tc := range cases {
		t.Run(tc.candidate+"_vs_"+tc.current, func(t *testing.T) {
			if got := IsNewer(tc.candidate, tc.current); got != tc.want {
				t.Fatalf("IsNewer(%q,%q)=%v, want %v", tc.candidate, tc.current, got, tc.want)
			}
		})
	}
}

func TestCheckNoDowngradeRelease(t *testing.T) {
	// A GitHub "latest" older than the installed binary must not be presented
	// as an upgrade (implements "no downgrade" at the check layer too).
	s, _ := newReleaseServer(t, releaseFixture{tag: "v0.5.0", version: "0.5.0"})
	defer s.Close()
	cfg := fakeConfig(s)
	cfg.CurrentVersion = "1.0.0"

	rel, due, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !due {
		t.Fatal("expected a due check")
	}
	if rel != nil {
		t.Fatalf("older release must not be offered as an upgrade, got %+v", rel)
	}
}

func TestCheckMissingChecksumOrSignatureRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		fix  releaseFixture
		want error
	}{
		{"missing checksum", releaseFixture{noChecksum: true}, ErrMissingChecksum},
		{"missing signature", releaseFixture{noSignature: true}, ErrMissingSignature},
		{"missing SBOM attestation", releaseFixture{noSBOM: true}, ErrMissingSBOM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newReleaseServer(t, tc.fix)
			defer s.Close()
			_, _, err := Check(context.Background(), fakeConfig(s))
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestCheckRejectsInvalidAssetPattern(t *testing.T) {
	s, _ := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)
	cfg.AssetPattern = "[" // invalid regexp

	if _, _, err := Check(context.Background(), cfg); err == nil {
		t.Fatal("expected an error for an invalid asset pattern")
	}
}

func TestCheckSkipsPrereleaseWhenCurrentIsStable(t *testing.T) {
	// A prerelease tagged "latest" must not be auto-offered to a stable install.
	s, _ := newReleaseServer(t, releaseFixture{tag: "v2.0.0-rc.1", version: "2.0.0-rc.1", prerelease: true})
	defer s.Close()
	cfg := fakeConfig(s)
	cfg.CurrentVersion = "1.0.0"

	rel, _, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel != nil {
		t.Fatalf("prerelease must not be offered to a stable install, got %+v", rel)
	}
}
