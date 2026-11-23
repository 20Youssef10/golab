package update

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// parseVersion normalises a tag or version string and parses it as SemVer.
// A leading "v" (the GitHub release convention) is accepted.
func parseVersion(s string) (*semver.Version, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return semver.NewVersion(s)
}

// IsNewer reports whether candidate is strictly greater than current.
// Invalid inputs are treated as "not newer" rather than causing an update —
// this mirrors upstream's _is_newer returning False on InvalidVersion and is
// the first line of defence against downgrades.
//
// Pre-release handling follows SemVer ordering: 1.2.0-rc.1 is older than
// 1.2.0, and a final release is newer than its pre-releases.
func IsNewer(candidate, current string) bool {
	c, err := parseVersion(candidate)
	if err != nil {
		return false
	}
	cur, err := parseVersion(current)
	if err != nil {
		return false
	}
	return c.GreaterThan(cur)
}

// shouldOffer decides whether a discovered release may be advertised. It
// enforces "no downgrade" and suppresses pre-releases for a stable install so
// that a beta tagged as "latest" is never auto-installed.
func shouldOffer(version, current string, prerelease bool) bool {
	if !IsNewer(version, current) {
		return false
	}
	if !prerelease {
		return true
	}
	cur, err := parseVersion(current)
	if err != nil {
		return false
	}
	// A pre-release is only interesting to a user already on a pre-release.
	return cur.Prerelease() != ""
}

// verifyNotDowngrade refuses to install a version older than the current one.
func verifyNotDowngrade(current, candidate string) error {
	cand, err := parseVersion(candidate)
	if err != nil {
		return fmt.Errorf("%w: release version %q", ErrInvalidVersion, candidate)
	}
	cur, err := parseVersion(current)
	if err != nil {
		// An unparseable current version (e.g. a dev build) cannot be compared;
		// do not block the update in that case.
		return nil
	}
	if cand.LessThan(cur) {
		return fmt.Errorf("%w: %s is older than installed %s", ErrDowngrade, candidate, current)
	}
	return nil
}
