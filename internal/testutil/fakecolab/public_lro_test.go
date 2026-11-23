// Ported from upstream docs/public_api_migration_plan.md §6 (fake-server
// coverage for the planned Public API LRO endpoints).
package fakecolab_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

// Step 6.1: POST returns an Operation; :wait is polled until done.
func TestFakePublicAssignLRO(t *testing.T) {
	s := fakecolab.New()
	defer s.Close()
	s.SetPublicAssign(fakecolab.PublicOperationSpec{
		Polls: 2,
		Runtime: fakecolab.Runtime{
			Name: "runtimes/r-lro", Endpoint: "m-s-lro", URL: "https://lro.example", Token: "tok",
			Variant: "VARIANT_CPU", Accelerator: "NONE", Shape: "SHAPE_STANDARD",
		},
	})

	resp, err := s.Client().Post(s.PublicURL()+"/v1beta/runtimes?runtimeId=r-1&requestId=req-1", "application/json", strings.NewReader(`{"runtimeSpec":{}}`))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `"done"`)
	require.Contains(t, string(body), "operations/")

	for i := 0; i < 2; i++ {
		w, err := s.Client().Get(s.PublicURL() + "/v1/operations/op-1:wait?timeout=30s")
		require.NoError(t, err)
		b, err := io.ReadAll(w.Body)
		require.NoError(t, err)
		w.Body.Close()
		require.Contains(t, string(b), `"done"`)
	}
	require.Equal(t, 2, s.PublicWaits())
}

// Step 6.1: operation error codes are injected into done=true responses.
func TestFakePublicAssignOperationError(t *testing.T) {
	s := fakecolab.New()
	defer s.Close()
	s.SetPublicAssign(fakecolab.PublicOperationSpec{ErrorCode: 9, ErrorMessage: "limit"})

	resp, err := s.Client().Post(s.PublicURL()+"/v1beta/runtimes", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, string(body), `"code":9`)
	require.Contains(t, string(body), "limit")
}

// Step 6.1: DELETE with allowMissing=true, including unknown names.
func TestFakePublicDeleteAllowMissing(t *testing.T) {
	s := fakecolab.New()
	defer s.Close()
	cli := s.Client()
	for _, name := range []string{"runtimes/r-1", "runtimes/r-unknown"} {
		req, err := http.NewRequest(http.MethodDelete, s.PublicURL()+"/v1beta/"+name+"?allowMissing=true", nil)
		require.NoError(t, err)
		resp, err := cli.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	require.Equal(t, []string{"runtimes/r-1", "runtimes/r-unknown"}, s.PublicDeleted())
	for _, r := range s.Requests() {
		require.Equal(t, "true", r.Query.Get("allowMissing"))
	}
}

// GET runtime used by ALREADY_EXISTS recovery.
func TestFakePublicGetRuntime(t *testing.T) {
	s := fakecolab.New()
	defer s.Close()
	s.SetPublicGetRuntime(fakecolab.Runtime{Name: "runtimes/r-existing", Endpoint: "m-s-existing"}, 0)
	resp, err := s.Client().Get(s.PublicURL() + "/v1beta/runtimes/r-1")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "runtimes/r-existing")

	s.SetPublicGetRuntime(fakecolab.Runtime{}, http.StatusNotFound)
	resp2, err := s.Client().Get(s.PublicURL() + "/v1beta/runtimes/r-1")
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusNotFound, resp2.StatusCode)
}
