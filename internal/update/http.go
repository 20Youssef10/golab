package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// Bounded downloads: explicit connect/TLS/header/idle timeouts everywhere, and
// hard caps so a hostile release server cannot exhaust memory or disk.
const (
	checkBudget       = 15 * time.Second
	httpClientTimeout = 30 * time.Second
	connectTimeout    = 5 * time.Second
	tlsTimeout        = 5 * time.Second
	headerTimeout     = 10 * time.Second
	idleTimeout       = 30 * time.Second

	maxReleaseJSON = 2 << 20   // 2 MiB
	maxChecksumDoc = 1 << 20   // 1 MiB
	maxBundleBytes = 8 << 20   // 8 MiB
	maxArtifact    = 512 << 20 // 512 MiB

	maxUpdateRedirects = 10
)

// allowedUpdateHost reports whether host is one GitHub uses in the update
// flow: api.github.com serves release metadata, github.com serves the
// browser_download_url values, and the githubusercontent hosts are GitHub's
// current and legacy release-asset CDN targets (a github.com release download
// normally 302s to one of them). Matching is exact, never suffix-based, so
// github.com.attacker.test cannot pass as github.com.
func allowedUpdateHost(host string) bool {
	switch host {
	case "api.github.com", "github.com", "objects.githubusercontent.com",
		"release-assets.githubusercontent.com", "github-releases.githubusercontent.com":
		return true
	default:
		return false
	}
}

// ErrUnsafeRedirect is the sentinel for a refused update redirect. Callers
// should use errors.Is rather than matching error strings.
var ErrUnsafeRedirect = errors.New("unsafe update redirect")

// RedirectError is the typed error returned when an update response tries to
// redirect the request somewhere the update client must not send it (gap
// report §4.6). It unwraps to ErrUnsafeRedirect so errors.Is keeps working.
// URL strings are redacted on construction, so credentials embedded in a
// planted redirect cannot leak through Error() or logs.
type RedirectError struct {
	From   string
	To     string
	Reason string
}

func (e *RedirectError) Error() string {
	return fmt.Sprintf("%s: %s -> %s (%s)", ErrUnsafeRedirect, e.From, e.To, e.Reason)
}

func (e *RedirectError) Unwrap() error { return ErrUnsafeRedirect }

// publicURL strips userinfo (which logx.Redact does not cover) and redacts
// secret-bearing query parameters before a URL goes into an error message.
func publicURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	redacted := *u
	redacted.User = nil
	return logx.Redact(redacted.String())
}

// checkUpdateRedirect is the update client's redirect policy. It refuses an
// https->http downgrade and any target that is not an allowlisted HTTPS host,
// so a planted release cannot make us repeat the request to an attacker (the
// request may carry credentials in headers even though the artifact itself is
// signature-checked later).
func checkUpdateRedirect(req *http.Request, via []*http.Request) error {
	var previous *url.URL
	if len(via) > 0 {
		previous = via[len(via)-1].URL
	}
	refuse := func(reason string) error {
		return &RedirectError{From: publicURL(previous), To: publicURL(req.URL), Reason: reason}
	}
	if len(via) >= maxUpdateRedirects {
		return refuse("too many redirects")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return refuse("redirect scheme is not https")
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return refuse("redirect uses a non-standard port")
	}
	if !allowedUpdateHost(req.URL.Hostname()) {
		return refuse("redirect host is not allowlisted")
	}
	return nil
}

// httpClient returns a client with explicit timeouts at every layer and the
// redirect policy above (docs/03 §6, AGENTS.md mandate 5). An injected client
// is copied rather than mutated: callers (and test fakes) may share it, and a
// caller-supplied CheckRedirect must not be able to weaken the policy.
func httpClient(cfg Config) *http.Client {
	if cfg.HTTPClient != nil {
		return &http.Client{
			Transport:     cfg.HTTPClient.Transport,
			CheckRedirect: checkUpdateRedirect,
			Jar:           cfg.HTTPClient.Jar,
			Timeout:       cfg.HTTPClient.Timeout,
		}
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: idleTimeout}).DialContext,
		TLSHandshakeTimeout:   tlsTimeout,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       idleTimeout,
		MaxIdleConns:          10,
	}
	return &http.Client{
		Timeout:       httpClientTimeout,
		Transport:     transport,
		CheckRedirect: checkUpdateRedirect,
	}
}

// getBytes performs a bounded GET. A mismatch between Content-Length and the
// number of bytes actually received is reported as ErrTruncatedDownload so a
// partial binary can never be installed.
func getBytes(ctx context.Context, client *http.Client, requestURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, redactErr(fmt.Errorf("build update request: %w", err))
	}
	resp, err := client.Do(req)
	if err != nil {
		// net/http wraps CheckRedirect errors in a url.Error whose message
		// contains the raw redirect URL (including any userinfo). Return the
		// already-sanitised typed error instead, so a planted URL cannot leak
		// credentials through the error text or logs.
		var redirectErr *RedirectError
		if errors.As(err, &redirectErr) {
			return nil, redirectErr
		}
		return nil, redactErr(fmt.Errorf("update request failed: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, URL: requestURL}
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, resp.ContentLength)
	}

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if readErr != nil {
		if errors.Is(readErr, io.ErrUnexpectedEOF) || errors.Is(readErr, io.EOF) {
			return nil, ErrTruncatedDownload
		}
		return nil, fmt.Errorf("read update response: %w", readErr)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, limit)
	}
	if resp.ContentLength >= 0 && int64(len(data)) != resp.ContentLength {
		return nil, ErrTruncatedDownload
	}
	return data, nil
}
