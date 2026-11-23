package update

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// CheckInterval is the minimum time between two release checks (once per day,
// matching upstream's _is_throttled: (now - last_check).days < 1).
const CheckInterval = 24 * time.Hour

// githubAPI is the only release source. Owner and Repo come from Config, which
// is populated by the composition root from compiled-in constants — there is
// deliberately no flag, environment variable or settings-file field that can
// redirect an update (docs/08 §3, fix for D-9).
const githubAPI = "https://api.github.com"

// Release describes one verified, downloadable release asset.
type Release struct {
	Version      string
	Tag          string
	URL          string
	Checksum     string
	SignatureURL string
	SBOMURL      string
	PublishedAt  time.Time
}

// Config carries the pinned release source and the caller-owned state needed
// for throttling. The caller persists the new LastCheck value after a due
// Check; this package never touches internal/state.
type Config struct {
	Owner          string
	Repo           string
	AssetPattern   string
	CurrentVersion string
	LastCheck      *time.Time
	Enabled        bool
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

func (c Config) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return logx.Logger()
}

// githubAsset is the subset of the GitHub Releases asset object we consume.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	HTMLURL     string        `json:"html_url"`
	PublishedAt time.Time     `json:"published_at"`
	Prerelease  bool          `json:"prerelease"`
	Assets      []githubAsset `json:"assets"`
}

// Check performs a throttled release check. The returned boolean reports
// whether a check was actually due (and therefore whether the caller should
// persist a fresh LastCheck). When not due, no network request is made and a
// nil release is returned.
func Check(ctx context.Context, cfg Config) (*Release, bool, error) {
	if !cfg.Enabled {
		return nil, false, nil
	}
	if cfg.LastCheck != nil && time.Since(*cfg.LastCheck) < CheckInterval {
		return nil, false, nil
	}
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, true, fmt.Errorf("%w: owner and repo must be pinned", ErrBadRelease)
	}
	if strings.TrimSpace(cfg.AssetPattern) == "" {
		return nil, true, fmt.Errorf("%w: asset pattern must be set", ErrBadRelease)
	}

	pattern, err := regexp.Compile(cfg.AssetPattern)
	if err != nil {
		return nil, true, fmt.Errorf("invalid asset pattern: %w", err)
	}

	apiURL := fmt.Sprintf("%s/repos/%s/%s/releases/latest",
		githubAPI, url.PathEscape(cfg.Owner), url.PathEscape(cfg.Repo))
	body, err := getBytes(ctx, httpClient(cfg), apiURL, maxReleaseJSON)
	if err != nil {
		return nil, true, err
	}
	var gr githubRelease
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, true, fmt.Errorf("parse release response: %w", err)
	}

	var asset, checksumAsset, signatureAsset, sbomAsset *githubAsset
	for i := range gr.Assets {
		a := &gr.Assets[i]
		switch {
		case isSBOMAsset(a.Name):
			if sbomAsset == nil {
				sbomAsset = a
			}
		case isChecksumAsset(a.Name):
			if checksumAsset == nil {
				checksumAsset = a
			}
		case isSignatureAsset(a.Name):
			if signatureAsset == nil {
				signatureAsset = a
			}
		case pattern.MatchString(a.Name):
			if asset == nil {
				asset = a
			}
		}
	}
	if asset == nil {
		return nil, true, fmt.Errorf("%w: no asset matches %q", ErrNoRelease, cfg.AssetPattern)
	}
	if checksumAsset == nil {
		return nil, true, fmt.Errorf("%w: no checksum asset in release %s", ErrMissingChecksum, gr.TagName)
	}
	if signatureAsset == nil {
		return nil, true, fmt.Errorf("%w: no cosign bundle in release %s", ErrMissingSignature, gr.TagName)
	}
	if sbomAsset == nil {
		return nil, true, fmt.Errorf("%w: no signed SBOM attestation in release %s", ErrMissingSBOM, gr.TagName)
	}

	checksum, err := fetchChecksum(ctx, cfg, checksumAsset.BrowserDownloadURL, asset.Name)
	if err != nil {
		return nil, true, err
	}

	version := strings.TrimPrefix(gr.TagName, "v")
	if !shouldOffer(version, cfg.CurrentVersion, gr.Prerelease || isPrerelease(version)) {
		cfg.logger().Debug("no newer release to offer",
			slog.String("tag", gr.TagName), slog.String("current", cfg.CurrentVersion))
		return nil, true, nil
	}

	rel := &Release{
		Version:      version,
		Tag:          gr.TagName,
		URL:          asset.BrowserDownloadURL,
		Checksum:     checksum,
		SignatureURL: signatureAsset.BrowserDownloadURL,
		SBOMURL:      sbomAsset.BrowserDownloadURL,
		PublishedAt:  gr.PublishedAt,
	}
	cfg.logger().Debug("release available",
		slog.String("tag", rel.Tag), slog.String("current", cfg.CurrentVersion))
	return rel, true, nil
}

// MaybeCheck schedules a non-blocking background check. It returns promptly
// and never writes to stdout: the banner (if any) is delivered exclusively via
// report, which the CLI routes to stderr. The internal timeout bounds the
// background work so a slow release server cannot keep a command alive.
func MaybeCheck(ctx context.Context, cfg Config, report func(*Release)) {
	if !cfg.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, checkBudget)
	go maybeCheckAsync(ctx, cancel, cfg, report)
}

func maybeCheckAsync(ctx context.Context, cancel context.CancelFunc, cfg Config, report func(*Release)) {
	defer cancel()
	rel, due, err := Check(ctx, cfg)
	if err != nil || !due || rel == nil {
		return
	}
	if report != nil {
		report(rel)
	}
}

func isChecksumAsset(name string) bool {
	lower := strings.ToLower(name)
	return lower == "checksums.txt" || strings.HasPrefix(lower, "sha256sums")
}

func isSignatureAsset(name string) bool {
	lower := strings.ToLower(name)
	if isSBOMAsset(name) {
		return false
	}
	return strings.Contains(lower, "sigstore") ||
		strings.Contains(lower, "cosign") ||
		strings.HasSuffix(lower, ".bundle")
}

// isSBOMAsset recognises a signed CycloneDX/SPDX SBOM attestation bundle. Such
// an asset is mandatory for every release (see the package documentation).
func isSBOMAsset(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "sbom") &&
		(strings.Contains(lower, "sigstore") || strings.Contains(lower, "cosign") || strings.HasSuffix(lower, ".bundle"))
}

func isPrerelease(version string) bool {
	v, err := parseVersion(version)
	return err == nil && v.Prerelease() != ""
}

// fetchChecksum downloads a checksums.txt and returns the hex digest for the
// named artifact. A missing entry is ErrMissingChecksum so an unverifiable
// release can never be advertised.
func fetchChecksum(ctx context.Context, cfg Config, checksumURL, assetName string) (string, error) {
	doc, err := getBytes(ctx, httpClient(cfg), checksumURL, maxChecksumDoc)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(doc), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != assetName {
			continue
		}
		hexSum := strings.TrimPrefix(strings.ToLower(fields[0]), "sha256:")
		if _, err := hex.DecodeString(hexSum); err != nil || len(hexSum) != 64 {
			return "", fmt.Errorf("%w: malformed digest for %s", ErrBadRelease, assetName)
		}
		return hexSum, nil
	}
	return "", fmt.Errorf("%w: no checksum for %s", ErrMissingChecksum, assetName)
}
