package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrResponseTooLarge means a kernel REST response exceeded maxFrameSize. The
// body is discarded rather than decoded: reading only the first maxFrameSize
// bytes could otherwise turn a truncated document into a valid-looking but
// partial JSON result. Callers may match it with errors.Is.
var ErrResponseTooLarge = errors.New("kernel REST response too large")

// restClient implements the kernel REST lifecycle of docs/02 §3.2.
type restClient struct {
	baseURL        string
	token          string // taken from logx.Secret.Reveal() only when building headers
	agent          string
	client         *http.Client
	requestTimeout time.Duration
}

func defaultHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second, KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

func newREST(target Target, cfg Config) *restClient {
	client := cfg.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &restClient{
		baseURL:        strings.TrimRight(target.URL, "/"),
		token:          target.Token.Reveal(),
		agent:          cfg.ClientAgent,
		client:         client,
		requestTimeout: timeout,
	}
}

// headers builds the exact header set of docs/02 §3.1. The proxy token is
// revealed here, at the point of use, never on a logging path.
func (c *restClient) headers() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	h.Set("X-Colab-Runtime-Proxy-Token", c.token)
	h.Set("X-Colab-Client-Agent", c.agent)
	h.Set("User-Agent", "Jupyter Kernel Client")
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json")
	return h
}

// do returns the raw body and status. All non-2xx codes are reported to the
// caller as an int: status text is never inspected or matched as a string.
func (c *restClient) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header = c.headers()
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, safeError(err, c.token)
	}
	defer resp.Body.Close()
	// Read one byte past the limit so an oversized body is distinguishable from
	// one that merely fills the limit. Never hand a truncated body to json.Unmarshal:
	// a prefix of a larger document can itself be valid JSON, which would silently
	// decode into a partial result.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFrameSize+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(data) > maxFrameSize {
		return nil, resp.StatusCode, ErrResponseTooLarge
	}
	return data, resp.StatusCode, nil
}

func (c *restClient) createKernel(ctx context.Context) (string, error) {
	data, status, err := c.do(ctx, http.MethodPost, "/api/kernels",
		map[string]any{"name": "python3", "path": nil})
	if err != nil {
		return "", fmt.Errorf("create kernel: %w", err)
	}
	switch status {
	case http.StatusOK, http.StatusCreated:
	default:
		if status == http.StatusUnauthorized {
			return "", &SessionLostError{Op: "create kernel", StatusCode: status}
		}
		return "", &HTTPStatusError{Op: "create kernel", Code: status}
	}
	var model struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &model); err != nil {
		return "", fmt.Errorf("create kernel: malformed response: %w", err)
	}
	if model.ID == "" {
		return "", errors.New("create kernel: response has no id")
	}
	return model.ID, nil
}

// inspectKernel reports whether a cached kernel still exists. A 404 is a
// normal "gone" answer; a 401 proves the session itself was lost.
func (c *restClient) inspectKernel(ctx context.Context, id string) (bool, error) {
	_, status, err := c.do(ctx, http.MethodGet, "/api/kernels/"+url.PathEscape(id), nil)
	if err != nil {
		return false, fmt.Errorf("inspect kernel: %w", err)
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	case http.StatusUnauthorized:
		return false, &SessionLostError{Op: "inspect kernel", StatusCode: status}
	default:
		return false, &HTTPStatusError{Op: "inspect kernel", Code: status}
	}
}

func (c *restClient) lifecycle(ctx context.Context, op, id string) error {
	method := http.MethodPost
	escapedID := url.PathEscape(id)
	path := "/api/kernels/" + escapedID + "/" + op
	if op == "shutdown" {
		method = http.MethodDelete
		path = "/api/kernels/" + escapedID
	}
	_, status, err := c.do(ctx, method, path, map[string]any{})
	if err != nil {
		return fmt.Errorf("%s kernel: %w", op, err)
	}
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusNotFound, http.StatusUnauthorized:
		return &SessionLostError{Op: op + " kernel", StatusCode: status}
	default:
		return &HTTPStatusError{Op: op + " kernel", Code: status}
	}
}
