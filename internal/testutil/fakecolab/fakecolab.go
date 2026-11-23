// See doc.go for the package overview. This file deliberately shares no types
// with internal/colabapi so the two sides stay independently testable.
package fakecolab

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultXSSI = ")]}'\n"

// Request is one recorded request. Headers are stored verbatim for assertions;
// never print them to a shared log because they can contain XSRF tokens.
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Headers http.Header
	Body    string
	Public  bool
	At      time.Time
}

// Runtime is the fake's view of a Public API runtime resource.
type Runtime struct {
	Name        string
	Version     string
	ExpireTime  time.Time
	Endpoint    string
	URL         string
	Token       string
	Variant     string // VARIANT_CPU / VARIANT_GPU / VARIANT_TPU / VARIANT_UNSPECIFIED
	Accelerator string // NONE / T4 / ...
	Shape       string // SHAPE_STANDARD / SHAPE_HIGHMEM / ...
}

// PublicOperationSpec configures POST /v1beta/runtimes and the subsequent
// GET /v1/{op}:wait polling. ErrorCode != 0 yields a done=true Operation with
// an error (see docs/public_api_migration_plan.md §5); Polls is the number of
// :wait calls that return done=false before the final done=true response.
type PublicOperationSpec struct {
	ErrorCode    int
	ErrorMessage string
	Polls        int
	Runtime      Runtime
}

// AssignmentState is a snapshot of an in-memory assignment.
type AssignmentState struct {
	NBH      string
	Endpoint string
	Token    string
	URL      string
}

// Fault injects a server-side misbehaviour. Method and PathContains narrow the
// match (empty matches everything). Status/Body replace the normal response;
// Delay blocks before handling; Hang waits until the client disconnects.
// ApplyHandler runs the normal handler first (so the fault can simulate a lost
// response after a successful mutation). Times: 0 = once, negative = always.
type Fault struct {
	Method       string
	PathContains string
	Status       int
	Body         string
	Delay        time.Duration
	Hang         bool
	ApplyHandler bool
	Times        int
}

type faultState struct {
	Fault
	remaining int
}

type assignment struct {
	nbh, endpoint, token, url, accelerator string
	variant, shape                         int
}

type consumption struct {
	balance float64
	rate    float64
	count   int
}

// Server is the fake. It owns two TLS servers sharing one certificate so a
// single Client() trusts both the tunnel and Public API endpoints.
type Server struct {
	tun *httptest.Server
	pub *httptest.Server

	mu sync.Mutex

	xssi          string
	publicXSSI    string
	enforceXSRF   bool
	xsrfPrefix    string
	proxyPrefix   string
	tokenLifetime time.Duration

	faults          []faultState
	assignments     map[string]*assignment
	pendingAssign   map[string]string
	pendingUnassign map[string]string
	pendingProp     map[string]string
	propagateURL    string
	runtimes        []Runtime
	publicBody      string
	consumption     consumption
	requests        []Request
	seq             int
	lastXSRFToken   string

	publicAssign         PublicOperationSpec
	publicGetRuntime     Runtime
	publicGetRuntimeHTTP int
	publicDeleted        []string
	publicWaits          int
	publicLastRuntimeID  string
}

// Option customises a Server at construction.
type Option func(*Server)

// WithXSSI sets the control-plane XSSI prefix; "" disables it.
func WithXSSI(prefix string) Option { return func(s *Server) { s.xssi = prefix } }

// WithPublicXSSI sets the Public API XSSI prefix.
func WithPublicXSSI(prefix string) Option { return func(s *Server) { s.publicXSSI = prefix } }

// WithXSRFPrefix sets the prefix of generated XSRF tokens (useful in leak tests).
func WithXSRFPrefix(prefix string) Option { return func(s *Server) { s.xsrfPrefix = prefix } }

// WithProxyTokenPrefix sets the prefix of generated runtime-proxy tokens.
func WithProxyTokenPrefix(prefix string) Option {
	return func(s *Server) { s.proxyPrefix = prefix }
}

// New starts the fake. Callers must Close it.
func New(opts ...Option) *Server {
	s := &Server{
		xssi:            defaultXSSI,
		enforceXSRF:     true,
		xsrfPrefix:      "xsrf-",
		proxyPrefix:     "proxy-",
		tokenLifetime:   time.Hour,
		assignments:     make(map[string]*assignment),
		pendingAssign:   make(map[string]string),
		pendingUnassign: make(map[string]string),
		pendingProp:     make(map[string]string),
		consumption:     consumption{balance: 100, rate: 0.07},
	}
	for _, o := range opts {
		o(s)
	}
	handler := http.HandlerFunc(s.serve)
	s.tun = httptest.NewUnstartedServer(handler)
	s.tun.StartTLS()
	s.pub = httptest.NewUnstartedServer(handler)
	// Share the tunnel's certificate so one HTTP client trusts both servers.
	s.pub.TLS = &tls.Config{Certificates: s.tun.TLS.Certificates, NextProtos: []string{"http/1.1"}}
	s.pub.StartTLS()
	return s
}

// Close shuts down both servers.
func (s *Server) Close() {
	s.pub.Close()
	s.tun.Close()
}

// TunURL is the control-plane base URL (Colab domain).
func (s *Server) TunURL() string { return s.tun.URL }

// PublicURL is the Public API base URL (colaboratory.googleapis.com stand-in).
func (s *Server) PublicURL() string { return s.pub.URL }

// Client returns an HTTP client that trusts both fake servers.
func (s *Server) Client() *http.Client {
	c := s.tun.Client()
	c.Timeout = 5 * time.Second
	return c
}

// SetFault registers a fault. Faults are consumed in registration order.
func (s *Server) SetFault(f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := f.Times
	if n == 0 {
		n = 1
	}
	s.faults = append(s.faults, faultState{Fault: f, remaining: n})
}

// ClearFaults removes all queued faults.
func (s *Server) ClearFaults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = nil
}

// SetPublicXSSI changes the Public API XSSI prefix at runtime; "" disables it.
func (s *Server) SetPublicXSSI(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicXSSI = prefix
}

// SetXSRFEnforcement toggles rejection of POSTs with a missing/mismatched token.
func (s *Server) SetXSRFEnforcement(enforce bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enforceXSRF = enforce
}

// SetProxyTokenLifetime sets tokenExpiresInSeconds in assignment responses.
func (s *Server) SetProxyTokenLifetime(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenLifetime = d
}

// SetConsumption configures GET /tun/m/ccu-info.
func (s *Server) SetConsumption(balance, rate float64, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumption = consumption{balance: balance, rate: rate, count: count}
}

// SetRuntimes configures the Public API /v1beta/runtimes response.
func (s *Server) SetRuntimes(runtimes []Runtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimes = append([]Runtime(nil), runtimes...)
}

// SetPublicBody replaces the Public API response body wholesale (XSSI is still
// added by SetPublicXSSI).
func (s *Server) SetPublicBody(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicBody = body
}

// SetPropagationAuthorizeURL makes credentials-propagation dry runs report an
// unauthorized redirect URI, exercising the browser-consent branch.
func (s *Server) SetPropagationAuthorizeURL(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.propagateURL = uri
}

// SetPublicAssign configures the Public API create LRO and its polling.
func (s *Server) SetPublicAssign(spec PublicOperationSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicAssign = spec
	s.publicWaits = 0
}

// SetPublicGetRuntime configures GET /v1beta/runtimes/{name}. A status of 0
// means HTTP 200.
func (s *Server) SetPublicGetRuntime(rt Runtime, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicGetRuntime = rt
	s.publicGetRuntimeHTTP = status
}

// PublicDeleted returns the resource names deleted via the Public API.
func (s *Server) PublicDeleted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.publicDeleted...)
}

// PublicWaits returns the number of GET /v1/{op}:wait calls observed.
func (s *Server) PublicWaits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publicWaits
}

// Requests returns a copy of the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// ResetLog clears the request log.
func (s *Server) ResetLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// LastXSRFToken is the token most recently issued by GET /assign.
func (s *Server) LastXSRFToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastXSRFToken
}

// Assignments returns a snapshot of assignments keyed by notebook hash.
func (s *Server) Assignments() []AssignmentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AssignmentState, 0, len(s.assignments))
	for _, a := range s.assignments {
		out = append(out, AssignmentState{NBH: a.nbh, Endpoint: a.endpoint, Token: a.token, URL: a.url})
	}
	return out
}

func (s *Server) next() int {
	s.seq++
	return s.seq
}

func (s *Server) matchFault(r *http.Request) *faultState {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.faults {
		f := &s.faults[i]
		if f.remaining == 0 {
			continue
		}
		if f.Method != "" && !strings.EqualFold(f.Method, r.Method) {
			continue
		}
		if f.PathContains != "" && !strings.Contains(r.URL.Path, f.PathContains) {
			continue
		}
		if f.remaining > 0 {
			f.remaining--
		}
		copy := *f
		return &copy
	}
	return nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
		Headers: r.Header.Clone(), Body: string(body),
		Public: isPublicPath(r.URL.Path), At: time.Now(),
	})
	s.mu.Unlock()

	fault := s.matchFault(r)
	if fault != nil {
		if fault.Hang {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		}
		if fault.Delay > 0 {
			timer := time.NewTimer(fault.Delay)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
		}
		if !fault.ApplyHandler {
			status := fault.Status
			if status == 0 {
				status = http.StatusInternalServerError
			}
			writeReply(w, isPublicPath(r.URL.Path), s.xssiFor(r), status, []byte(fault.Body), "text/plain")
			return
		}
	}

	status, payload, contentType := s.route(r)
	if fault != nil && fault.Status > 0 {
		status, payload, contentType = fault.Status, []byte(fault.Body), "text/plain"
	}
	writeReply(w, isPublicPath(r.URL.Path), s.xssiFor(r), status, payload, contentType)
}

func isPublicPath(path string) bool {
	return strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/v1/")
}

func (s *Server) xssiFor(r *http.Request) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isPublicPath(r.URL.Path) {
		return s.publicXSSI
	}
	return s.xssi
}

func writeReply(w http.ResponseWriter, public bool, xssi string, status int, payload []byte, contentType string) {
	if len(payload) > 0 && status >= 200 && status < 300 && xssi != "" {
		payload = append([]byte(xssi), payload...)
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	if len(payload) > 0 {
		_, _ = w.Write(payload)
	}
}

func (s *Server) route(r *http.Request) (int, []byte, string) {
	if r.URL.Path == "/v1beta/runtimes" {
		if r.Method == http.MethodPost {
			return s.publicCreateRuntime(r)
		}
		return s.publicRuntimes()
	}
	// Long-running operations: GET /v1/{op}:wait?timeout=30s.
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/") && strings.HasSuffix(r.URL.Path, ":wait") {
		return s.publicWait()
	}
	// GET/DELETE /v1beta/{name=runtimes/*}.
	if strings.HasPrefix(r.URL.Path, "/v1beta/runtimes/") {
		switch r.Method {
		case http.MethodGet:
			return s.publicGet(r)
		case http.MethodDelete:
			return s.publicDelete(r)
		}
	}
	if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1beta/") {
		return s.publicDelete(r)
	}
	switch {
	case r.URL.Path == "/tun/m/assign":
		if r.Method == http.MethodGet {
			return s.getAssign(r)
		}
		if r.Method == http.MethodPost {
			return s.postAssign(r)
		}
	case r.URL.Path == "/tun/m/assignments" && r.Method == http.MethodGet:
		return s.listAssignments()
	case strings.HasPrefix(r.URL.Path, "/tun/m/unassign/"):
		return s.unassign(r)
	case r.URL.Path == "/tun/m/ccu-info" && r.Method == http.MethodGet:
		return s.ccuInfo()
	case strings.HasPrefix(r.URL.Path, "/tun/m/credentials-propagation/"):
		return s.credentialsPropagation(r)
	}
	return http.StatusNotFound, []byte(`{"error":"not found"}`), "application/json"
}

func (s *Server) getAssign(r *http.Request) (int, []byte, string) {
	nbh := r.URL.Query().Get("nbh")
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.assignments[nbh]; ok {
		return 200, mustJSON(s.assignedPayload(a)), "application/json"
	}
	token := s.xsrfPrefix + strconv.Itoa(s.next())
	s.pendingAssign[nbh] = token
	s.lastXSRFToken = token
	acc := r.URL.Query().Get("accelerator")
	if acc == "" {
		acc = "NONE"
	}
	variant := r.URL.Query().Get("variant")
	if variant == "" {
		variant = "DEFAULT"
	}
	return 200, mustJSON(map[string]any{"acc": acc, "nbh": nbh, "token": token, "variant": variant}), "application/json"
}

func (s *Server) postAssign(r *http.Request) (int, []byte, string) {
	nbh := r.URL.Query().Get("nbh")
	token := r.Header.Get("X-Goog-Colab-Token")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enforceXSRF {
		want, ok := s.pendingAssign[nbh]
		if !ok || want != token {
			return http.StatusForbidden, []byte(`{"error":"bad xsrf"}`), "application/json"
		}
	}
	if a, ok := s.assignments[nbh]; ok {
		return 200, mustJSON(s.assignedPayload(a)), "application/json"
	}
	n := s.next()
	variant := mapVariantInt(r.URL.Query().Get("variant"))
	acc := r.URL.Query().Get("accelerator")
	if acc == "" {
		acc = "NONE"
	}
	shape := 0
	if r.URL.Query().Get("shape") == "hm" {
		shape = 1
	}
	a := &assignment{
		nbh: nbh, endpoint: fmt.Sprintf("endpoint-%d", n),
		token:       s.proxyPrefix + strconv.Itoa(n),
		url:         fmt.Sprintf("https://runtime-%d.example", n),
		accelerator: acc, variant: variant, shape: shape,
	}
	s.assignments[nbh] = a
	delete(s.pendingAssign, nbh)
	return 200, mustJSON(s.assignedPayload(a)), "application/json"
}

func (s *Server) listAssignments() (int, []byte, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.assignments))
	for _, a := range s.assignments {
		item := s.assignedPayload(a)
		item["machineShape"] = a.shape
		out = append(out, item)
	}
	return 200, mustJSON(map[string]any{"assignments": out}), "application/json"
}

func (s *Server) assignedPayload(a *assignment) map[string]any {
	return map[string]any{
		"accelerator": a.accelerator,
		"endpoint":    a.endpoint,
		"variant":     a.variant,
		"runtimeProxyInfo": map[string]any{
			"token":                 a.token,
			"tokenExpiresInSeconds": int(s.tokenLifetime / time.Second),
			"url":                   a.url,
		},
	}
}

func (s *Server) unassign(r *http.Request) (int, []byte, string) {
	endpoint := strings.TrimPrefix(r.URL.Path, "/tun/m/unassign/")
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodGet {
		token := s.xsrfPrefix + "unassign-" + strconv.Itoa(s.next())
		s.pendingUnassign[endpoint] = token
		return 200, mustJSON(map[string]string{"token": token}), "application/json"
	}
	if r.Method == http.MethodPost {
		if s.enforceXSRF {
			if want, ok := s.pendingUnassign[endpoint]; !ok || want != r.Header.Get("X-Goog-Colab-Token") {
				return http.StatusForbidden, []byte(`{"error":"bad xsrf"}`), "application/json"
			}
		}
		for nbh, a := range s.assignments {
			if a.endpoint == endpoint {
				delete(s.assignments, nbh)
			}
		}
		delete(s.pendingUnassign, endpoint)
		return 200, []byte("[]"), "application/json"
	}
	return http.StatusMethodNotAllowed, nil, ""
}

func (s *Server) ccuInfo() (int, []byte, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return 200, mustJSON(map[string]any{
		"currentBalance":        s.consumption.balance,
		"consumptionRateHourly": s.consumption.rate,
		"assignmentsCount":      s.consumption.count,
	}), "application/json"
}

func (s *Server) credentialsPropagation(r *http.Request) (int, []byte, string) {
	endpoint := strings.TrimPrefix(r.URL.Path, "/tun/m/credentials-propagation/")
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodGet {
		token := s.xsrfPrefix + "prop-" + strconv.Itoa(s.next())
		s.pendingProp[endpoint] = token
		// docs/02: the payload follows the first newline.
		return 200, []byte(defaultXSSI + `{"token":"` + token + `"}`), "application/json"
	}
	if r.Method == http.MethodPost {
		if s.enforceXSRF {
			if want, ok := s.pendingProp[endpoint]; !ok || want != r.Header.Get("X-Goog-Colab-Token") {
				return http.StatusForbidden, []byte(`{"error":"bad xsrf"}`), "application/json"
			}
		}
		if s.propagateURL != "" && r.URL.Query().Get("dryrun") == "true" {
			return 200, mustJSON(map[string]any{
				"success": false, "unauthorized_redirect_uri": s.propagateURL,
			}), "application/json"
		}
		return 200, []byte(`{"success":true}`), "application/json"
	}
	return http.StatusMethodNotAllowed, nil, ""
}

func (s *Server) publicRuntimes() (int, []byte, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publicBody != "" {
		return 200, []byte(s.publicBody), "application/json"
	}
	runtimes := make([]map[string]any, 0, len(s.runtimes))
	for _, rt := range s.runtimes {
		runtimes = append(runtimes, publicRuntimeWire(rt))
	}
	return 200, mustJSON(map[string]any{"runtimes": runtimes}), "application/json"
}

// publicRuntimeWire mirrors upstream's Runtime model (client.py::Runtime).
func publicRuntimeWire(rt Runtime) map[string]any {
	variant := rt.Variant
	if variant == "" {
		variant = "VARIANT_CPU"
	}
	shape := rt.Shape
	if shape == "" {
		shape = "SHAPE_STANDARD"
	}
	acc := rt.Accelerator
	if acc == "" {
		acc = "NONE"
	}
	return map[string]any{
		"name":    rt.Name,
		"version": rt.Version,
		"connectionInfo": map[string]any{
			"expireTime": rt.ExpireTime.UTC().Format(time.RFC3339),
			"endpoint":   rt.Endpoint,
			"url":        rt.URL,
			"token":      rt.Token,
		},
		"runtimeSpec": map[string]any{
			"variant": variant, "accelerator": acc, "shape": shape,
		},
	}
}

// assignResponseRuntime fills in defaults so an operation response is always a
// usable Runtime, even when the test did not configure one explicitly.
func (s *Server) assignResponseRuntime() Runtime {
	rt := s.publicAssign.Runtime
	if rt.Name == "" {
		id := s.publicLastRuntimeID
		if id == "" {
			id = "r-1"
		}
		rt.Name = "runtimes/" + id
	}
	if rt.Endpoint == "" {
		rt.Endpoint = strings.TrimPrefix(rt.Name, "runtimes/")
	}
	if rt.URL == "" {
		rt.URL = "https://runtime.example"
	}
	if rt.Token == "" {
		rt.Token = "proxy-token"
	}
	if rt.ExpireTime.IsZero() {
		rt.ExpireTime = time.Now().UTC().Add(time.Hour)
	}
	return rt
}

func (s *Server) publicCreateRuntime(r *http.Request) (int, []byte, string) {
	s.mu.Lock()
	s.publicLastRuntimeID = r.URL.Query().Get("runtimeId")
	spec := s.publicAssign
	s.mu.Unlock()

	op := map[string]any{"name": "operations/op-1", "done": spec.ErrorCode != 0 || spec.Polls == 0}
	if spec.ErrorCode != 0 {
		op["error"] = map[string]any{"code": spec.ErrorCode, "message": spec.ErrorMessage}
	} else if spec.Polls == 0 {
		op["response"] = publicRuntimeWire(s.assignResponseRuntime())
	}
	return 200, mustJSON(op), "application/json"
}

func (s *Server) publicWait() (int, []byte, string) {
	s.mu.Lock()
	s.publicWaits++
	spec := s.publicAssign
	done := s.publicWaits >= spec.Polls
	s.mu.Unlock()

	op := map[string]any{"name": "operations/op-1", "done": done}
	if done {
		if spec.ErrorCode != 0 {
			op["error"] = map[string]any{"code": spec.ErrorCode, "message": spec.ErrorMessage}
		} else {
			op["response"] = publicRuntimeWire(s.assignResponseRuntime())
		}
	}
	return 200, mustJSON(op), "application/json"
}

func (s *Server) publicGet(r *http.Request) (int, []byte, string) {
	s.mu.Lock()
	rt := s.publicGetRuntime
	status := s.publicGetRuntimeHTTP
	s.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		return status, []byte(`{"error":"not found"}`), "application/json"
	}
	if rt.Name == "" {
		rt.Name = strings.TrimPrefix(r.URL.Path, "/v1beta/")
	}
	return 200, mustJSON(publicRuntimeWire(rt)), "application/json"
}

func (s *Server) publicDelete(r *http.Request) (int, []byte, string) {
	name := strings.TrimPrefix(r.URL.Path, "/v1beta/")
	s.mu.Lock()
	s.publicDeleted = append(s.publicDeleted, name)
	kept := s.runtimes[:0]
	for _, rt := range s.runtimes {
		if rt.Name != name {
			kept = append(kept, rt)
		}
	}
	s.runtimes = kept
	s.mu.Unlock()
	return 200, []byte(`{}`), "application/json"
}

func mapVariantInt(v string) int {
	switch strings.ToUpper(v) {
	case "GPU":
		return 1
	case "TPU":
		return 2
	default:
		return 0
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
