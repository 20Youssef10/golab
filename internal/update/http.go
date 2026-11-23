package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
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
)

// httpClient returns the injected client when present, otherwise a client with
// explicit timeouts at every layer (docs/03 §6, AGENTS.md mandate 5).
func httpClient(cfg Config) *http.Client {
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient
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
	return &http.Client{Timeout: httpClientTimeout, Transport: transport}
}

// getBytes performs a bounded GET. A mismatch between Content-Length and the
// number of bytes actually received is reported as ErrTruncatedDownload so a
// partial binary can never be installed.
func getBytes(ctx context.Context, client *http.Client, requestURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build update request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update request failed: %w", err)
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
