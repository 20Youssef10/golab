package colabapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

const (
	// DefaultDomain is the Colab session backend (control plane).
	DefaultDomain = "https://colab.research.google.com"
	// DefaultPublicAPI serves GET /v1beta/runtimes.
	DefaultPublicAPI = "https://colaboratory.googleapis.com"
	// DefaultClientAgent identifies this implementation to Colab.
	DefaultClientAgent = "golab"
	// DefaultRetries and DefaultBackoff mirror COLAB_CLI_ASSIGN_RETRIES / BACKOFF.
	DefaultRetries = 3
	DefaultBackoff = 10 * time.Second

	defaultHTTPTimeout = 60 * time.Second
	defaultDialTimeout = 5 * time.Second
	xssiPrefix         = ")]}'\n"
	controlPrefix      = "/tun/m"
)

// Client is the Colab control plane. It takes plain values and never imports
// internal/state or internal/cli.
type Client interface {
	Assign(ctx context.Context, req AssignRequest) (*AssignmentResponse, error)
	Unassign(ctx context.Context, endpoint string) error
	// UnassignRuntime is the additive Public API-aware delete. runtimeName may
	// be empty, in which case it is resolved from the endpoint.
	UnassignRuntime(ctx context.Context, endpoint, runtimeName string) error
	List(ctx context.Context) ([]ListedAssignment, error)
	Usage(ctx context.Context) (*Consumption, error)
	CredentialsPropagation(ctx context.Context, endpoint string) (*CredProp, error)
	// CheckAPIAccess probes both APIs. Per-API failures are reported by the
	// booleans; err is non-nil only when the context is cancelled.
	CheckAPIAccess(ctx context.Context) (publicOK, tunnelOK bool, err error)
}

// Config configures New. HTTPClient must already carry the caller's OAuth/ADC
// bearer credentials; New clones it and installs explicit timeouts.
//
// RequestTimeout, Retries and Backoff may be set explicitly. A zero value
// means "derive from the COLAB_CLI_* environment variable, then its default"
// (see env.go); a negative value explicitly disables retries / the backoff
// delay. This keeps backward compatibility with callers that relied on the
// previous zero-value behaviour while honouring the three env vars.
type Config struct {
	Domain         string
	PublicAPI      string
	HTTPClient     *http.Client
	ClientAgent    string
	UsePublicAPI   bool
	Logger         *slog.Logger
	RequestTimeout time.Duration
	Retries        int
	Backoff        time.Duration
}

type client struct {
	domain       *url.URL
	publicAPI    *url.URL
	http         *http.Client
	agent        string
	usePublicAPI bool
	logger       *slog.Logger
	retries      int
	backoff      time.Duration
}

// New validates cfg and returns a ready Client. No network I/O happens here.
func New(cfg Config) (Client, error) {
	domain, err := parseBase(cfg.Domain, DefaultDomain)
	if err != nil {
		return nil, fmt.Errorf("invalid Colab domain: %w", err)
	}
	pub, err := parseBase(cfg.PublicAPI, DefaultPublicAPI)
	if err != nil {
		return nil, fmt.Errorf("invalid Colab Public API: %w", err)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = logx.Logger()
	}
	agent := cfg.ClientAgent
	if agent == "" {
		agent = DefaultClientAgent
	}
	// Zero values mean "unset": derive from the environment (or its default).
	// Negative values explicitly disable retries / the backoff delay.
	retries := cfg.Retries
	switch {
	case retries > 0:
	case retries < 0:
		retries = 0
	default:
		retries = EnvAssignRetries()
	}
	backoff := cfg.Backoff
	switch {
	case backoff > 0:
	case backoff < 0:
		backoff = 0
	default:
		backoff = EnvRetryBackoff()
	}
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = EnvRequestTimeout()
	}
	return &client{
		domain: domain, publicAPI: pub, http: cloneClient(cfg.HTTPClient, timeout),
		agent: agent, usePublicAPI: cfg.UsePublicAPI,
		logger: logger, retries: retries, backoff: backoff,
	}, nil
}

func parseBase(raw, fallback string) (*url.URL, error) {
	if raw == "" {
		raw = fallback
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("expected absolute http(s) URL, got %q", raw)
	}
	return u, nil
}

// cloneClient copies the caller's client (preserving its authenticator) and
// fills in explicit connect/TLS/header/idle timeouts where absent.
func cloneClient(base *http.Client, timeout time.Duration) *http.Client {
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	// The auth layer installs a 60 s client-level default. Treat that value
	// (and an unset timeout) as "not explicitly chosen by the caller" so the
	// COLAB_CLI_REQUEST_TIMEOUT override can shorten or lengthen it, while
	// preserving a deliberately smaller test/integration timeout.
	if c.Timeout <= 0 || c.Timeout == defaultHTTPTimeout {
		c.Timeout = timeout
	}
	if c.Transport == nil {
		c.Transport = http.DefaultTransport
	}
	if tr, ok := c.Transport.(*http.Transport); ok {
		tr = tr.Clone()
		if tr.DialContext == nil {
			tr.DialContext = (&net.Dialer{Timeout: defaultDialTimeout, KeepAlive: 30 * time.Second}).DialContext
		}
		if tr.TLSHandshakeTimeout <= 0 {
			tr.TLSHandshakeTimeout = 10 * time.Second
		}
		if tr.ResponseHeaderTimeout <= 0 {
			tr.ResponseHeaderTimeout = 30 * time.Second
		}
		if tr.IdleConnTimeout <= 0 {
			tr.IdleConnTimeout = 90 * time.Second
		}
		if tr.ExpectContinueTimeout <= 0 {
			tr.ExpectContinueTimeout = time.Second
		}
		c.Transport = tr
	}
	return c
}

// requestOptions carries the per-call wire details.
type requestOptions struct {
	xsrf        logx.Secret
	contentType string
	body        io.Reader
	parse       func([]byte) error
}

func (c *client) doRequest(ctx context.Context, method string, target *url.URL, opts requestOptions) error {
	req, err := http.NewRequestWithContext(ctx, method, target.String(), opts.body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Colab-Client-Agent", c.agent)
	if tok := opts.xsrf.Reveal(); tok != "" {
		req.Header.Set("X-Goog-Colab-Token", tok)
	}
	if opts.contentType != "" {
		req.Header.Set("Content-Type", opts.contentType)
	}
	// Never log headers or body: they may carry bearer/XSRF/proxy tokens.
	c.logger.Debug("colab control-plane request", "method", method, "path", target.Path)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{
			Code: resp.StatusCode, Body: redactBody(raw),
			Method: method, Path: target.Path,
		}
	}
	if opts.parse == nil {
		return nil
	}
	body := stripXSSI(raw)
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := opts.parse(body); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, target.Path, err)
	}
	return nil
}

func stripXSSI(body []byte) []byte {
	return bytes.TrimPrefix(body, []byte(xssiPrefix))
}

// makeURL builds an absolute URL. Host comparison uses host:port so a fake
// Public API on a different local port is not mistaken for the Colab domain.
func makeURL(base *url.URL, path string, params url.Values, colab bool) *url.URL {
	u := *base
	prefix := strings.TrimRight(base.Path, "/")
	u.Path = prefix + path
	if strings.Contains(path, "%") {
		if decoded, err := url.PathUnescape(path); err == nil {
			u.Path = prefix + decoded
			u.RawPath = strings.TrimRight(base.EscapedPath(), "/") + path
		}
	}
	q := url.Values{}
	for key, values := range params {
		for _, v := range values {
			q.Add(key, v)
		}
	}
	if colab {
		q.Set("authuser", "0")
	}
	u.RawQuery = q.Encode()
	return &u
}

func (c *client) tunURL(path string, params url.Values) *url.URL {
	return makeURL(c.domain, controlPrefix+path, params, true)
}

func (c *client) publicURL(path string, params url.Values) *url.URL {
	return makeURL(c.publicAPI, path, params, false)
}

// backoffDelay computes base*2^attempt with an overflow guard so a large
// attempt index can never wrap into a negative (or immediate) delay.
func backoffDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	shift := attempt
	if shift > 30 {
		shift = 30
	}
	delay := base * time.Duration(1<<shift)
	if delay/time.Duration(1<<shift) != base {
		return time.Duration(1<<63 - 1)
	}
	return delay
}

func (c *client) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type pendingAssignment struct {
	token logx.Secret
}

func (c *client) assignURL(req AssignRequest) *url.URL {
	params := url.Values{
		"nbh":         {req.NotebookHash.String()},
		"variant":     {string(req.Variant)},
		"accelerator": {string(req.Accelerator)},
	}
	if req.Shape == ShapeHighRAM {
		params.Set("shape", "hm")
	}
	return c.tunURL("/assign", params)
}

// getAssignment returns either the XSRF handshake token (shape A) or an
// already-allocated assignment (shape B). Exactly one of the first two results
// is non-nil when err is nil.
func (c *client) getAssignment(ctx context.Context, req AssignRequest) (*pendingAssignment, *Assignment, error) {
	u := c.assignURL(req)
	// Shape B has no "variant" field; shape A's is a string while shape B's
	// (when present on other endpoints) is an int, so it is not decoded here.
	var wire struct {
		Token            logx.Secret       `json:"token"`
		Endpoint         string            `json:"endpoint"`
		RuntimeProxyInfo *RuntimeProxyInfo `json:"runtimeProxyInfo"`
	}
	if err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
		parse: func(b []byte) error { return json.Unmarshal(b, &wire) },
	}); err != nil {
		return nil, nil, err
	}
	if wire.Endpoint != "" && wire.RuntimeProxyInfo != nil {
		return nil, &Assignment{Endpoint: wire.Endpoint, RuntimeProxyInfo: *wire.RuntimeProxyInfo}, nil
	}
	if wire.Token.Reveal() == "" {
		return nil, nil, fmt.Errorf("%w: /assign GET has neither assignment nor XSRF token", ErrUnexpectedResponse)
	}
	return &pendingAssignment{token: wire.Token}, nil, nil
}

func (c *client) postAssignment(ctx context.Context, req AssignRequest, xsrf logx.Secret) (*AssignmentResponse, error) {
	u := c.assignURL(req)
	var out AssignmentResponse
	if err := c.doRequest(ctx, http.MethodPost, u, requestOptions{
		xsrf:  xsrf,
		parse: func(b []byte) error { return json.Unmarshal(b, &out) },
	}); err != nil {
		return nil, err
	}
	if out.Endpoint == "" {
		return nil, fmt.Errorf("%w: /assign POST has no endpoint", ErrUnexpectedResponse)
	}
	return &out, nil
}

func normalizeAssignRequest(req AssignRequest) AssignRequest {
	if req.Variant == "" {
		req.Variant = VariantDefault
	}
	if req.Accelerator == "" {
		req.Accelerator = AccelNone
	}
	return req
}

// mapAssignError adds the friendly typed errors used by `new` and `run`.
func mapAssignError(err error, req AssignRequest) error {
	se := statusOf(err)
	if se == nil {
		return err
	}
	switch se.Code {
	case http.StatusPreconditionFailed:
		return fmt.Errorf("%w: %w", ErrTooManyAssignments, err)
	case http.StatusBadRequest:
		if req.Accelerator != AccelNone {
			return fmt.Errorf("%w: %s: %w", ErrNoQuota, req.Accelerator, err)
		}
	}
	return err
}

func fromAssignment(a *Assignment, req AssignRequest) *AssignmentResponse {
	return &AssignmentResponse{
		Accelerator:      req.Accelerator,
		Endpoint:         a.Endpoint,
		RuntimeProxyInfo: a.RuntimeProxyInfo,
		Variant:          variantToAssignment(req.Variant),
		Reattached:       true,
	}
}

// Assign implements C1+C2 with retry/backoff and GET reconciliation so an
// uncertain POST never double-bills.
func (c *client) Assign(ctx context.Context, req AssignRequest) (*AssignmentResponse, error) {
	req = normalizeAssignRequest(req)
	if c.usePublicAPI {
		return c.assignPublic(ctx, req)
	}
	pending, existing, err := c.getAssignment(ctx, req)
	if err != nil {
		return nil, mapAssignError(err, req)
	}
	if existing != nil {
		return fromAssignment(existing, req), nil
	}
	xsrf := pending.token

	for attempt := 0; attempt <= c.retries; attempt++ {
		res, err := c.postAssignment(ctx, req, xsrf)
		if err == nil {
			return res, nil
		}

		if se := statusOf(err); se != nil {
			switch se.Code {
			case http.StatusPreconditionFailed:
				return nil, mapAssignError(err, req)
			case http.StatusBadRequest:
				if req.Accelerator != AccelNone {
					return nil, mapAssignError(err, req)
				}
			}
			if !retryableStatus(se.Code) {
				return nil, err
			}
		} else if !isTimeout(err) {
			return nil, err
		}
		if attempt == c.retries {
			return nil, err
		}

		if err := c.sleep(ctx, backoffDelay(c.backoff, attempt)); err != nil {
			return nil, err
		}

		pending, existing, err = c.getAssignment(ctx, req)
		if err != nil {
			return nil, mapAssignError(err, req)
		}
		if existing != nil {
			return fromAssignment(existing, req), nil
		}
		xsrf = pending.token
	}
	return nil, ErrUnexpectedResponse
}

// Unassign implements C4+C5 (tunnel) or the Public API DELETE path. It keeps
// its original signature; callers that already know the Public API runtime
// name can use UnassignRuntime to avoid the list lookup.
func (c *client) Unassign(ctx context.Context, endpoint string) error {
	return c.UnassignRuntime(ctx, endpoint, "")
}

// UnassignRuntime deletes a runtime by endpoint and/or Public API runtime
// name. On the tunnel plane runtimeName is ignored; on the Public API plane it
// is the resource name ("runtimes/r-…"). Unknown endpoints are treated as a
// no-op because the Public API DELETE uses allowMissing=true.
func (c *client) UnassignRuntime(ctx context.Context, endpoint, runtimeName string) error {
	if c.usePublicAPI {
		return c.unassignPublic(ctx, endpoint, runtimeName)
	}
	return c.unassignTunnel(ctx, endpoint)
}

func (c *client) unassignTunnel(ctx context.Context, endpoint string) error {
	u := c.tunURL("/unassign/"+url.PathEscape(endpoint), nil)
	var wire struct {
		Token logx.Secret `json:"token"`
	}
	if err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
		parse: func(b []byte) error { return json.Unmarshal(b, &wire) },
	}); err != nil {
		return err
	}
	return c.doRequest(ctx, http.MethodPost, u, requestOptions{xsrf: wire.Token})
}

// List implements C3 or the Public API /v1beta/runtimes path.
func (c *client) List(ctx context.Context) ([]ListedAssignment, error) {
	if c.usePublicAPI {
		return c.listPublic(ctx)
	}
	return c.listTunnel(ctx)
}

func (c *client) listTunnel(ctx context.Context) ([]ListedAssignment, error) {
	u := c.tunURL("/assignments", nil)
	var wire struct {
		Assignments []ListedAssignment `json:"assignments"`
	}
	if err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
		parse: func(b []byte) error { return json.Unmarshal(b, &wire) },
	}); err != nil {
		return nil, err
	}
	if wire.Assignments == nil {
		return []ListedAssignment{}, nil
	}
	return wire.Assignments, nil
}

// public wire models (client.py::ListRuntimesResponse).
type publicRuntime struct {
	Name           string `json:"name"`
	Version        string `json:"version,omitempty"`
	ConnectionInfo struct {
		ExpireTime string `json:"expireTime"`
		Endpoint   string `json:"endpoint"`
		URL        string `json:"url"`
		Token      string `json:"token"`
	} `json:"connectionInfo"`
	RuntimeSpec struct {
		Variant     string `json:"variant"`
		Accelerator string `json:"accelerator"`
		Shape       string `json:"shape"`
	} `json:"runtimeSpec"`
}

func (c *client) listPublic(ctx context.Context) ([]ListedAssignment, error) {
	u := c.publicURL("/v1beta/runtimes", nil)
	var wire struct {
		Runtimes []publicRuntime `json:"runtimes"`
	}
	if err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
		parse: func(b []byte) error { return json.Unmarshal(b, &wire) },
	}); err != nil {
		return nil, err
	}
	if len(wire.Runtimes) == 0 {
		return []ListedAssignment{}, nil
	}
	now := time.Now()
	out := make([]ListedAssignment, 0, len(wire.Runtimes))
	for _, r := range wire.Runtimes {
		out = append(out, mapPublicRuntime(r, now))
	}
	return out, nil
}

// mapPublicRuntime converts upstream's Runtime model into our wire-neutral
// ListedAssignment (client.py::_list_assignments_public).
func mapPublicRuntime(r publicRuntime, now time.Time) ListedAssignment {
	shape := ShapeStandard
	if r.RuntimeSpec.Shape == "SHAPE_HIGHMEM" {
		shape = ShapeHighRAM
	}
	variant := AssignmentDefault
	switch r.RuntimeSpec.Variant {
	case "VARIANT_GPU":
		variant = AssignmentGPU
	case "VARIANT_TPU":
		variant = AssignmentTPU
	}
	acc := Accelerator(r.RuntimeSpec.Accelerator)
	switch acc {
	case AccelG4, AccelT4, AccelL4, AccelA100, AccelH100, AccelV5E1, AccelV6E1:
	default:
		acc = AccelNone
	}
	expiresIn := 3600
	if dt, err := time.Parse(time.RFC3339, r.ConnectionInfo.ExpireTime); err == nil {
		expiresIn = int(dt.Sub(now).Seconds())
		if expiresIn < 0 {
			expiresIn = 0
		}
	}
	endpoint := r.ConnectionInfo.Endpoint
	if endpoint == "" {
		endpoint = strings.TrimPrefix(r.Name, "runtimes/")
	}
	return ListedAssignment{
		Accelerator:  acc,
		Endpoint:     endpoint,
		RuntimeName:  r.Name,
		Variant:      variant,
		MachineShape: shape,
		RuntimeProxyInfo: RuntimeProxyInfo{
			Token:                 logx.NewSecret(r.ConnectionInfo.Token),
			TokenExpiresInSeconds: expiresIn,
			URL:                   r.ConnectionInfo.URL,
		},
	}
}

// Usage implements C6 and applies consumption.py's tunnel→display mapping.
func (c *client) Usage(ctx context.Context) (*Consumption, error) {
	u := c.tunURL("/ccu-info", nil)
	var wire struct {
		CurrentBalance        float64 `json:"currentBalance"`
		ConsumptionRateHourly float64 `json:"consumptionRateHourly"`
		AssignmentsCount      int     `json:"assignmentsCount"`
	}
	if err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
		parse: func(b []byte) error { return json.Unmarshal(b, &wire) },
	}); err != nil {
		return nil, err
	}
	return &Consumption{
		PaidComputeUnitsBalance: wire.CurrentBalance,
		ConsumptionRateHourly:   wire.ConsumptionRateHourly,
		AssignmentsCount:        wire.AssignmentsCount,
	}, nil
}

func propagationParams(dryRun bool) url.Values {
	return url.Values{
		"authtype":  {"dfs_ephemeral"},
		"version":   {"2"},
		"dryrun":    {strconv.FormatBool(dryRun)},
		"propagate": {"true"},
		"record":    {"false"},
	}
}

// parseFlexibleJSON handles bodies of the form "<prefix>\n{json}" as well as
// bare JSON (commands/automation.py::_parse_propagation_response).
func parseFlexibleJSON(body []byte, target any) error {
	text := string(body)
	candidates := []string{text}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		candidates = append(candidates, text[i+1:])
	}
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		candidates = append(candidates, text[i+1:])
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if err := json.Unmarshal([]byte(candidate), target); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: no JSON object in credentials-propagation response", ErrUnexpectedResponse)
}

func (c *client) postPropagation(ctx context.Context, endpoint string, xsrf logx.Secret, dryRun bool) (*CredProp, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("file_id", "empty.ipynb"); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	u := c.tunURL("/credentials-propagation/"+url.PathEscape(endpoint), propagationParams(dryRun))
	var wire struct {
		Success                 bool   `json:"success"`
		UnauthorizedRedirectURI string `json:"unauthorized_redirect_uri"`
	}
	if err := c.doRequest(ctx, http.MethodPost, u, requestOptions{
		xsrf: xsrf, contentType: mw.FormDataContentType(), body: bytes.NewReader(buf.Bytes()),
		parse: func(b []byte) error { return parseFlexibleJSON(b, &wire) },
	}); err != nil {
		return nil, err
	}
	return &CredProp{Success: wire.Success, UnauthorizedRedirectURI: wire.UnauthorizedRedirectURI}, nil
}

// CredentialsPropagation implements C7+C8: GET for XSRF, then a dry-run POST.
func (c *client) CredentialsPropagation(ctx context.Context, endpoint string) (*CredProp, error) {
	u := c.tunURL("/credentials-propagation/"+url.PathEscape(endpoint), propagationParams(true))
	var wire struct {
		Token logx.Secret `json:"token"`
	}
	if err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
		parse: func(b []byte) error { return parseFlexibleJSON(b, &wire) },
	}); err != nil {
		return nil, err
	}
	if wire.Token.Reveal() == "" {
		return nil, fmt.Errorf("%w: no credentials-propagation XSRF token", ErrUnexpectedResponse)
	}
	res, err := c.postPropagation(ctx, endpoint, wire.Token, true)
	if err != nil {
		return nil, err
	}
	res.Token = wire.Token
	res.Confirm = func(ctx context.Context) (*CredProp, error) {
		return c.postPropagation(ctx, endpoint, wire.Token, false)
	}
	return res, nil
}

// CheckAPIAccess probes both APIs independently, mirroring client.py's
// check_api_access. Settings ownership stays with internal/state; callers
// persist the returned booleans themselves.
func (c *client) CheckAPIAccess(ctx context.Context) (bool, bool, error) {
	_, pubErr := c.listPublic(ctx)
	_, tunErr := c.listTunnel(ctx)
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	return pubErr == nil, tunErr == nil, nil
}
