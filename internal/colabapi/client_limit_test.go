package colabapi

// Regression for defect §4.2: a response body larger than maxResponseSize was
// truncated by io.LimitReader and then decoded, so an oversized reply could
// silently become valid-but-partial JSON. The fix must return
// ErrResponseTooLarge and must never invoke the parser on a truncated body.
// No upstream Python test covers this; the Python client has no body limit.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The first maxResponseSize bytes are a complete, valid JSON object (trailing
// whitespace is legal); the single extra byte pushes it over the limit. Under
// the defect, io.LimitReader stops at the object and json.Unmarshal succeeds,
// so the oversized reply is accepted as a normal assignment response.
func TestOversizedResponseRejectedWithoutPartialDecode(t *testing.T) {
	object := `{"endpoint":"ep-oversized"}`
	body := object + strings.Repeat(" ", maxResponseSize-len(object)) + "x"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := testWireClient(t, srv)
	var decoded struct {
		Endpoint string `json:"endpoint"`
	}
	parsed := false
	err := c.doRequest(context.Background(), http.MethodGet, c.tunURL("/tun/m/oversized", nil), requestOptions{
		parse: func(b []byte) error {
			parsed = true
			return json.Unmarshal(b, &decoded)
		},
	})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want errors.Is(err, ErrResponseTooLarge)", err)
	}
	if parsed {
		t.Fatalf("parser was called on a truncated body; decoded = %+v", decoded)
	}
}

// A normal response must still be decoded exactly as before.
func TestNormalResponseStillDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"endpoint":"ep-normal"}`))
	}))
	defer srv.Close()

	c := testWireClient(t, srv)
	var decoded struct {
		Endpoint string `json:"endpoint"`
	}
	parsed := false
	err := c.doRequest(context.Background(), http.MethodGet, c.tunURL("/tun/m/normal", nil), requestOptions{
		parse: func(b []byte) error {
			parsed = true
			return json.Unmarshal(b, &decoded)
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !parsed {
		t.Fatal("parser was not called for a normal response")
	}
	if decoded.Endpoint != "ep-normal" {
		t.Fatalf("endpoint = %q, want %q", decoded.Endpoint, "ep-normal")
	}
}
