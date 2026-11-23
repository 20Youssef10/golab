// Ported upstream references:
//   - tests/test_client.py::test_client_assign_new
//   - tests/test_client.py::test_client_unassign
//   - tests/test_client.py::test_public_client_list_assignments
//
// These tests pin the fake's C1-C8 / Public API behaviour independently of
// internal/colabapi, so a client test failure can be attributed to one side.
package fakecolab_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

func TestFakeAssignXSRFStateMachine(t *testing.T) {
	s := fakecolab.New()
	defer s.Close()
	cli := s.Client()
	base := s.TunURL() + "/tun/m/assign?nbh=" + "aaaa_bbbb." + strings.Repeat(".", 8)

	get, err := cli.Get(base)
	require.NoError(t, err)
	body, err := io.ReadAll(get.Body)
	require.NoError(t, err)
	get.Body.Close()
	require.Equal(t, http.StatusOK, get.StatusCode)
	require.True(t, strings.HasPrefix(string(body), ")]}'\n"))
	require.Contains(t, string(body), "xsrf")

	// POST without the XSRF token must be rejected.
	noToken, err := cli.Post(base, "application/json", strings.NewReader(""))
	require.NoError(t, err)
	noToken.Body.Close()
	require.Equal(t, http.StatusForbidden, noToken.StatusCode)

	// POST with the matching token succeeds, then a re-GET is idempotent.
	req, err := http.NewRequest(http.MethodPost, base, strings.NewReader(""))
	require.NoError(t, err)
	req.Header.Set("X-Goog-Colab-Token", s.LastXSRFToken())
	post, err := cli.Do(req)
	require.NoError(t, err)
	post.Body.Close()
	require.Equal(t, http.StatusOK, post.StatusCode)

	get2, err := cli.Get(base)
	require.NoError(t, err)
	body2, err := io.ReadAll(get2.Body)
	require.NoError(t, err)
	get2.Body.Close()
	require.Contains(t, string(body2), "runtimeProxyInfo")

	log := s.Requests()
	require.Len(t, log, 4)
	require.Equal(t, http.MethodGet, log[0].Method)
	require.NotEmpty(t, log[0].Query.Get("nbh"))
	require.Equal(t, http.MethodPost, log[2].Method)
}

func TestFakeFaultInjectionAndLog(t *testing.T) {
	s := fakecolab.New()
	defer s.Close()
	s.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/tun/m/assign", Status: http.StatusBadGateway})
	base := s.TunURL() + "/tun/m/assign?nbh=fault_nbh"

	resp, err := s.Client().Post(base, "application/json", strings.NewReader(""))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	// One-shot faults clear themselves: this POST now fails at the XSRF state
	// machine instead of returning the injected 502.
	resp2, err := s.Client().Post(base, "application/json", strings.NewReader(""))
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusForbidden, resp2.StatusCode)

	log := s.Requests()
	require.Len(t, log, 2)
	s.ResetLog()
	require.Empty(t, s.Requests())
}

func TestFakePublicRuntimesAndXSSI(t *testing.T) {
	s := fakecolab.New()
	defer s.Close()
	s.SetPublicXSSI(")]}'\n")
	expiry := time.Now().UTC().Add(2 * time.Hour)
	s.SetRuntimes([]fakecolab.Runtime{{
		Name:        "runtimes/fallback-id",
		ExpireTime:  expiry,
		URL:         "https://runtime.example",
		Token:       "proxy-token",
		Variant:     "VARIANT_GPU",
		Accelerator: "T4",
		Shape:       "SHAPE_HIGHMEM",
	}})

	resp, err := s.Client().Get(s.PublicURL() + "/v1beta/runtimes")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.True(t, strings.HasPrefix(string(body), ")]}'\n"))
	require.Contains(t, string(body), "fallback-id")

	// The Public API is not a Colab-domain request, so authuser is not appended.
	log := s.Requests()
	require.Len(t, log, 1)
	require.Empty(t, log[0].Query.Get("authuser"))
}
