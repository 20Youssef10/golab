package compat

// Refresh builds a provenance-tagged command-surface snapshot from the
// OFFICIAL upstream CLI source. It is deliberately conservative: only literal
// long-option strings inside explicit typer.Option(...) declarations and
// explicit/derived command registrations are captured. Nothing is synthesised
// from parameter names, comments or string constants, and the result is labelled
// as a partial capture rather than a complete upstream surface or live truth.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/version"
)

const (
	// OfficialRawBase is the only production source. There is no flag or
	// environment variable that can redirect a release build away from it.
	OfficialRawBase = "https://raw.githubusercontent.com"
	// DefaultCLIPath is the upstream Typer root module at the pinned ref.
	DefaultCLIPath = "src/colab_cli/cli.py"
	// DefaultRef mirrors the compatibility target this build advertises. It is
	// compatibility text, not a guaranteed git tag; pass --ref to pin a tag.
	DefaultRef = version.Upstream

	KindSelf     = "self-snapshot"
	KindUpstream = "upstream-fetched"

	MethodTyperLiterals = "typer-source-literals"

	maxUpstreamSource = 2 << 20 // 2 MiB, bounded like the update client's JSON cap

	connectTimeout       = 5 * time.Second
	tlsTimeout           = 5 * time.Second
	headerTimeout        = 10 * time.Second
	idleTimeout          = 30 * time.Second
	upstreamTimeout      = 30 * time.Second
	maxUpstreamRedirects = 10
)

var (
	reRef           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+~-]*$`)
	reSourcePath    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	reCommandName   = regexp.MustCompile(`app\.command\(\s*(?:name\s*=\s*)?["']([^"']+)["']`)
	reCommandBare   = regexp.MustCompile(`app\.command\(\s*\)`)
	reCommandInvoke = regexp.MustCompile(`app\.command\(\s*\)\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	reDef           = regexp.MustCompile(`^def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	reOption        = regexp.MustCompile(`(?s)(?:typer|click)\.Option\(([^)]*)\)`)
	reQuoted        = regexp.MustCompile("\"([^\"]*)\"|'([^']*)'")
	reLongFlag      = regexp.MustCompile(`^--[A-Za-z0-9][A-Za-z0-9-]*$`)
)

// ErrNoSurface means the fetched source did not contain a recognisable command
// surface. Failing loudly is safer than presenting a fabricated empty surface.
var ErrNoSurface = errors.New("compat: no commands found in upstream CLI source")

// FetchConfig controls one fetch. BaseURL, Client and Now are test seams:
// production leaves them unset and Fetch uses the official raw host, explicit
// timeouts and the wall clock.
type FetchConfig struct {
	BaseURL string
	Ref     string
	Path    string
	Client  *http.Client
	Now     func() time.Time
}

// Fetch downloads one upstream CLI module, hashes it, parses its literal
// command/flag surface and returns a snapshot with full provenance. It never
// writes a file (that is the caller's decision) and never presents itself as
// live upstream truth.
func Fetch(ctx context.Context, cfg FetchConfig) (Snapshot, error) {
	ref := cfg.Ref
	if ref == "" {
		ref = DefaultRef
	}
	sourcePath := cfg.Path
	if sourcePath == "" {
		sourcePath = DefaultCLIPath
	}
	if !reRef.MatchString(ref) {
		return Snapshot{}, fmt.Errorf("compat refresh: invalid ref %q", ref)
	}
	if !reSourcePath.MatchString(sourcePath) {
		return Snapshot{}, fmt.Errorf("compat refresh: invalid source path %q", sourcePath)
	}

	base := cfg.BaseURL
	injected := base != ""
	if !injected {
		base = OfficialRawBase
	}
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Snapshot{}, fmt.Errorf("compat refresh: invalid source base")
	}
	if !injected && (u.Scheme != "https" || !allowedUpstreamHost(u.Hostname())) {
		return Snapshot{}, fmt.Errorf("compat refresh: source base is not an official HTTPS host")
	}
	sourceURL := base + "/" + ref + "/" + sourcePath

	client := cfg.Client
	if client == nil {
		client = upstreamHTTPClient(injected)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("compat refresh: build request: %s", logx.Redact(err.Error()))
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", version.ClientAgent())
	resp, err := client.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("compat refresh: request failed: %s", logx.Redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Snapshot{}, fmt.Errorf("compat refresh: upstream returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamSource+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("compat refresh: read source: %s", logx.Redact(err.Error()))
	}
	if len(data) > maxUpstreamSource {
		return Snapshot{}, fmt.Errorf("compat refresh: source exceeds %d bytes", maxUpstreamSource)
	}
	surface, err := ParseCLISurface(data)
	if err != nil {
		return Snapshot{}, err
	}

	sum := sha256.Sum256(data)
	now := time.Now().UTC()
	if cfg.Now != nil {
		now = cfg.Now().UTC()
	}
	captured := now.Format(time.RFC3339)
	return Snapshot{
		Kind: KindUpstream,
		Scope: fmt.Sprintf("partial capture: literal long options and explicit command registrations in %s only; "+
			"short aliases, parameter-derived option names and commands defined in other upstream modules are NOT captured; "+
			"this is not a complete upstream CLI surface and not live truth", sourcePath),
		Source:      "official upstream CLI source: " + sourceURL,
		Captured:    captured,
		SourceURL:   sourceURL,
		Ref:         ref,
		FetchedAt:   captured,
		ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		Method:      MethodTyperLiterals,
		Commands:    surface,
	}, nil
}

// ParseCLISurface extracts a conservative command -> long-flag map from a
// Typer/Click CLI module. It reads only function signatures and explicit
// registration calls; option names are never inferred from Python identifiers.
func ParseCLISurface(src []byte) (map[string][]string, error) {
	type block struct {
		fn  string
		sig []string
	}
	var (
		blocks      []*block
		names       = map[string][]string{}
		pending     []string
		pendingBare bool
		pendingRoot bool
		current     *block
	)
	for _, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "app.command(") {
			if m := reCommandName.FindStringSubmatch(line); m != nil {
				pending = append(pending, m[1])
			} else if reCommandBare.MatchString(line) {
				pendingBare = true
			}
		}
		if strings.Contains(line, "app.callback(") {
			pendingRoot = true
		}
		if m := reCommandInvoke.FindStringSubmatch(line); m != nil {
			names[m[1]] = append(names[m[1]], derivedName(m[1]))
		}
		if m := reDef.FindStringSubmatch(line); m != nil {
			b := &block{fn: m[1], sig: []string{line}}
			if pendingRoot {
				names[b.fn] = append(names[b.fn], "colab")
				pendingRoot = false
			}
			names[b.fn] = append(names[b.fn], pending...)
			pending = nil
			if pendingBare {
				names[b.fn] = append(names[b.fn], derivedName(b.fn))
				pendingBare = false
			}
			blocks = append(blocks, b)
			current = b
			continue
		}
		if current == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") ||
			strings.HasPrefix(trimmed, ")") {
			// Still inside the (possibly multi-line) signature.
			current.sig = append(current.sig, line)
		} else {
			current = nil
		}
	}

	surface := map[string][]string{}
	for _, b := range blocks {
		if len(names[b.fn]) == 0 {
			continue
		}
		flags := optionNames(strings.Join(b.sig, "\n"))
		for _, name := range names[b.fn] {
			key := "colab"
			if name != "colab" {
				key = "colab " + derivedName(name)
			}
			surface[key] = mergeUnique(surface[key], flags)
		}
	}
	if len(surface) == 0 {
		return nil, ErrNoSurface
	}
	return surface, nil
}

func optionNames(sig string) []string {
	seen := map[string]bool{}
	for _, group := range reOption.FindAllStringSubmatch(sig, -1) {
		for _, quoted := range reQuoted.FindAllStringSubmatch(group[1], -1) {
			literal := quoted[1]
			if literal == "" {
				literal = quoted[2]
			}
			for _, part := range strings.FieldsFunc(literal, func(r rune) bool {
				return r == '/' || r == ' ' || r == '\t' || r == ','
			}) {
				part = strings.TrimSpace(part)
				if !reLongFlag.MatchString(part) {
					continue
				}
				seen[strings.TrimPrefix(part, "--")] = true
			}
		}
	}
	return uniqueSorted(seen)
}

func derivedName(fn string) string { return strings.ReplaceAll(fn, "_", "-") }

func mergeUnique(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		seen[v] = true
	}
	return uniqueSorted(seen)
}

func uniqueSorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func allowedUpstreamHost(host string) bool {
	switch host {
	case "raw.githubusercontent.com", "api.github.com", "github.com":
		return true
	default:
		return false
	}
}

// checkUpstreamRedirect refuses downgrades and redirects off the official
// GitHub hosts, mirroring the self-update client's redirect policy.
func checkUpstreamRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxUpstreamRedirects {
		return fmt.Errorf("compat refresh: too many redirects")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") ||
		(req.URL.Port() != "" && req.URL.Port() != "443") ||
		!allowedUpstreamHost(req.URL.Hostname()) {
		return fmt.Errorf("compat refresh: unsafe redirect refused")
	}
	return nil
}

func upstreamHTTPClient(injected bool) *http.Client {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: idleTimeout}).DialContext,
		TLSHandshakeTimeout:   tlsTimeout,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       idleTimeout,
		MaxIdleConns:          10,
	}
	if !injected {
		tr.Proxy = http.ProxyFromEnvironment
	}
	client := &http.Client{Timeout: upstreamTimeout, Transport: tr}
	if !injected {
		client.CheckRedirect = checkUpstreamRedirect
	}
	return client
}

// LoadSnapshot reads an on-disk snapshot. A snapshot without a known kind is
// rejected so an unlabelled file can never be presented as upstream truth.
func LoadSnapshot(path string) (Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("compat: read snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("compat: decode snapshot: %w", err)
	}
	switch snap.Kind {
	case KindSelf, KindUpstream, "documented-surface":
	default:
		return Snapshot{}, fmt.Errorf("compat: snapshot has unknown kind %q", snap.Kind)
	}
	if snap.Commands == nil {
		return Snapshot{}, fmt.Errorf("compat: snapshot has no command surface")
	}
	return snap, nil
}

// WriteSnapshot atomically writes a snapshot (temp file, fsync, rename) so an
// interrupted refresh can never leave a half-written snapshot behind.
func WriteSnapshot(path string, snap Snapshot) (err error) {
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("compat: encode snapshot: %w", err)
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".compat-snapshot-*")
	if err != nil {
		return fmt.Errorf("compat: create temp snapshot: %w", err)
	}
	name := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := tmp.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("compat: close temp snapshot: %w", closeErr)
			}
		}
		// After a successful rename the temp name is gone; that is expected.
		if remErr := os.Remove(name); remErr != nil && !errors.Is(remErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("compat: remove temp snapshot: %w", remErr)
		}
	}()
	if _, err = tmp.Write(body); err != nil {
		return fmt.Errorf("compat: write temp snapshot: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("compat: sync temp snapshot: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("compat: close temp snapshot: %w", err)
	}
	closed = true
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("compat: install snapshot: %w", err)
	}
	return nil
}
