package version

import (
	"os"
	"regexp"
	"testing"
)

// TestNumberMatchesLatestChangelogRelease guards against cutting a tag whose
// binary still reports the previous version. CHANGELOG.md is the release
// ledger; the first dated ## [x.y.z] section must match the compiled Number.
func TestNumberMatchesLatestChangelogRelease(t *testing.T) {
	data, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	re := regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\]`)
	match := re.FindSubmatch(data)
	if match == nil {
		t.Fatal("no dated release section found in CHANGELOG.md")
	}
	if got, want := string(match[1]), Number; got != want {
		t.Fatalf("CLI version %q does not match latest changelog release %q", Number, got)
	}
}
