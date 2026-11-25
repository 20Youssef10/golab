// Ported from upstream docs/public_api_migration_plan.md §6 (the Public API
// lifecycle tests that are planned upstream but not yet implemented there):
//
//	Step 6.1: test_public_client_assign_immediate
//	Step 6.1: test_public_client_assign_polls_until_done
//	Step 6.1: test_public_client_assign_failed_precondition_raises_too_many_assignments
//	Step 6.1: test_public_client_assign_invalid_argument_raises_400
//	Step 6.1: test_public_client_assign_already_exists_recovers
//	Step 6.1: test_public_client_unassign_with_runtime_name
//	Step 6.1: test_public_client_unassign_resolves_endpoint
//
// Env-var tests port tests/test_client.py::test_request_timeout_override_and_explicit_precedence,
// test_assign_retries_can_be_disabled and the bounded-backoff cases.
package colabapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

func publicClient(t *testing.T, fake *fakecolab.Server) colabapi.Client {
	t.Helper()
	c, err := colabapi.New(colabapi.Config{
		Domain:       fake.TunURL(),
		PublicAPI:    fake.PublicURL(),
		HTTPClient:   fake.Client(),
		ClientAgent:  "colab-go-test",
		UsePublicAPI: true,
	})
	require.NoError(t, err)
	return c
}

// Step 6.1: test_public_client_assign_immediate.
func TestPublicAssignImmediate(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetPublicAssign(fakecolab.PublicOperationSpec{
		Runtime: fakecolab.Runtime{
			Name: "runtimes/r-test", Endpoint: "m-s-endpoint", URL: "https://runtime.example",
			Token: "proxy-token", Variant: "VARIANT_GPU", Accelerator: "T4", Shape: "SHAPE_HIGHMEM",
		},
	})

	res, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: fixedHash(t), Variant: colabapi.VariantGPU, Accelerator: colabapi.AccelT4,
		Shape: colabapi.ShapeHighRAM,
	})
	require.NoError(t, err)
	require.Equal(t, "m-s-endpoint", res.Endpoint)
	require.Equal(t, "runtimes/r-test", res.RuntimeName)
	require.Equal(t, colabapi.AccelT4, res.Accelerator)
	require.Equal(t, colabapi.AssignmentGPU, res.Variant)
	require.False(t, res.Reattached)
	require.Equal(t, "proxy-token", res.RuntimeProxyInfo.Token.Reveal())

	reqs := fake.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, http.MethodPost, reqs[0].Method)
	require.Equal(t, "/v1beta/runtimes", reqs[0].Path)
	require.NotEmpty(t, reqs[0].Query.Get("runtimeId"))
	require.True(t, strings.HasPrefix(reqs[0].Query.Get("runtimeId"), "r-"))
	require.NotEmpty(t, reqs[0].Query.Get("requestId"))
	require.Empty(t, reqs[0].Query.Get("authuser"))
	require.Contains(t, reqs[0].Body, `"runtimeSpec"`)
	require.Contains(t, reqs[0].Body, "VARIANT_GPU")
	require.Contains(t, reqs[0].Body, "SHAPE_HIGHMEM")
	require.Equal(t, "application/json", reqs[0].Headers.Get("Content-Type"))
	require.Equal(t, 0, fake.PublicWaits(), "immediate completion must not poll")
}

// Step 6.1: test_public_client_assign_polls_until_done.
func TestPublicAssignPollsUntilDone(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetPublicAssign(fakecolab.PublicOperationSpec{
		Polls: 2,
		Runtime: fakecolab.Runtime{
			Name: "runtimes/r-poll", Endpoint: "m-s-poll", URL: "https://poll.example",
			Token: "poll-token", Variant: "VARIANT_CPU", Accelerator: "NONE", Shape: "SHAPE_STANDARD",
		},
	})

	res, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: fixedHash(t),
	})
	require.NoError(t, err)
	require.Equal(t, "m-s-poll", res.Endpoint)
	require.Equal(t, "runtimes/r-poll", res.RuntimeName)
	require.Equal(t, 2, fake.PublicWaits())

	for _, r := range fake.Requests() {
		if strings.Contains(r.Path, ":wait") {
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "30s", r.Query.Get("timeout"))
			require.True(t, strings.HasPrefix(r.Path, "/v1/operations/"))
			require.True(t, strings.HasSuffix(r.Path, ":wait"))
		}
	}
}

// Step 6.1: FAILED_PRECONDITION (code 9) → TooManyAssignments.
func TestPublicAssignFailedPrecondition(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetPublicAssign(fakecolab.PublicOperationSpec{ErrorCode: 9, ErrorMessage: "session limit reached"})

	_, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.Error(t, err)
	require.True(t, errors.Is(err, colabapi.ErrTooManyAssignments), "got %v", err)
	require.Contains(t, err.Error(), "session limit reached")
}

// Step 6.1: INVALID_ARGUMENT (code 3) → HTTP 400 / ErrNoQuota for accelerators.
func TestPublicAssignInvalidArgument400(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetPublicAssign(fakecolab.PublicOperationSpec{ErrorCode: 3, ErrorMessage: "no quota"})

	_, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: fixedHash(t), Accelerator: colabapi.AccelA100,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, colabapi.ErrNoQuota), "got %v", err)
	var se *colabapi.StatusError
	require.True(t, errors.As(err, &se))
	require.Equal(t, http.StatusBadRequest, se.Code)
}

// PERMISSION_DENIED (code 7) → HTTP 403 typed error.
func TestPublicAssignPermissionDenied403(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetPublicAssign(fakecolab.PublicOperationSpec{ErrorCode: 7, ErrorMessage: "denied"})

	_, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.Error(t, err)
	var se *colabapi.StatusError
	require.True(t, errors.As(err, &se), "got %v", err)
	require.Equal(t, http.StatusForbidden, se.Code)
}

// Step 6.1: ALREADY_EXISTS (code 6) recovers via GET runtime.
func TestPublicAssignAlreadyExistsRecovers(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetPublicAssign(fakecolab.PublicOperationSpec{ErrorCode: 6, ErrorMessage: "exists"})
	fake.SetPublicGetRuntime(fakecolab.Runtime{
		Name: "runtimes/r-existing", Endpoint: "m-s-existing", URL: "https://existing.example",
		Token: "existing-token", Variant: "VARIANT_GPU", Accelerator: "T4", Shape: "SHAPE_STANDARD",
	}, 0)

	res, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: fixedHash(t), Variant: colabapi.VariantGPU, Accelerator: colabapi.AccelT4,
	})
	require.NoError(t, err)
	require.True(t, res.Reattached)
	require.Equal(t, "m-s-existing", res.Endpoint)
	require.Equal(t, "runtimes/r-existing", res.RuntimeName)

	var sawGet bool
	for _, r := range fake.Requests() {
		if r.Method == http.MethodGet && strings.HasPrefix(r.Path, "/v1beta/runtimes/") {
			sawGet = true
		}
	}
	require.True(t, sawGet, "recovery must GET the existing runtime")
}

// Step 6.1: test_public_client_unassign_with_runtime_name.
func TestPublicUnassignWithRuntimeName(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	err := publicClient(t, fake).UnassignRuntime(context.Background(), "", "runtimes/r-gone")
	require.NoError(t, err)
	require.Equal(t, []string{"runtimes/r-gone"}, fake.PublicDeleted())

	reqs := fake.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, http.MethodDelete, reqs[0].Method)
	require.Equal(t, "/v1beta/runtimes/r-gone", reqs[0].Path)
	require.Equal(t, "true", reqs[0].Query.Get("allowMissing"))
	require.Empty(t, reqs[0].Headers.Get("X-Goog-Colab-Token"), "public DELETE has no XSRF handshake")
}

// Step 6.1: a bare r-<id> endpoint is itself a valid runtime name.
func TestPublicUnassignBareRuntimeID(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	require.NoError(t, publicClient(t, fake).UnassignRuntime(context.Background(), "r-abc123", ""))
	require.Equal(t, []string{"runtimes/r-abc123"}, fake.PublicDeleted())
}

// Step 6.1: test_public_client_unassign_resolves_endpoint.
func TestPublicUnassignResolvesEndpoint(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetRuntimes([]fakecolab.Runtime{{
		Name: "runtimes/r-resolve", Endpoint: "m-s-resolve", ExpireTime: time.Now().UTC().Add(time.Hour),
		URL: "https://resolve.example", Token: "tok", Variant: "VARIANT_CPU", Accelerator: "NONE", Shape: "SHAPE_STANDARD",
	}})

	c := publicClient(t, fake)
	listed, err := c.List(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "runtimes/r-resolve", listed[0].RuntimeName)

	fake.ResetLog()
	require.NoError(t, c.Unassign(context.Background(), "m-s-resolve"))
	require.Equal(t, []string{"runtimes/r-resolve"}, fake.PublicDeleted())
}

// An endpoint that is neither a runtime name nor listed is already gone;
// allowMissing=true makes the delete a no-op, so we must not error.
func TestPublicUnassignUnknownEndpointIsNoop(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	require.NoError(t, publicClient(t, fake).Unassign(context.Background(), "m-s-missing"))
	require.Empty(t, fake.PublicDeleted())
}

// A lost public POST is reconciled via GET before retrying, so we never create
// a duplicate runtime (docs plan §5 "honour retries on transient errors").
func TestPublicAssignReconcilesAfterTransientPOST(t *testing.T) {
	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "0")
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/v1beta/runtimes", Status: http.StatusBadGateway, Times: 1})
	fake.SetPublicGetRuntime(fakecolab.Runtime{
		Name: "runtimes/r-recovered", Endpoint: "m-s-recovered", URL: "https://r.example",
		Token: "tok", Variant: "VARIANT_CPU", Accelerator: "NONE", Shape: "SHAPE_STANDARD",
	}, 0)

	res, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.NoError(t, err)
	require.True(t, res.Reattached)
	require.Equal(t, "runtimes/r-recovered", res.RuntimeName)
	posts := 0
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPost {
			posts++
		}
	}
	require.Equal(t, 1, posts, "reconciliation avoids a second POST")
}

// §3.18: AIP-155 request ids identify ONE logical operation. Every POST
// attempt of a retried assignment must carry the same requestId, otherwise a
// delayed first attempt can create two runtimes. Ports the retry half of
// docs/public_api_migration_plan.md §5 (requestId + retry reconciliation).
func TestPublicAssignRetriesReuseRequestID(t *testing.T) {
	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "0")
	fake := fakecolab.New()
	defer fake.Close()
	// Reconciliation must not short-circuit: GET says the runtime is absent,
	// so the client really performs three POSTs (two 502s then success).
	fake.SetPublicGetRuntime(fakecolab.Runtime{}, http.StatusNotFound)
	fake.SetFault(fakecolab.Fault{
		Method: http.MethodPost, PathContains: "/v1beta/runtimes",
		Status: http.StatusBadGateway, Times: 2,
	})
	fake.SetPublicAssign(fakecolab.PublicOperationSpec{
		Runtime: fakecolab.Runtime{
			Name: "runtimes/r-retry", Endpoint: "m-s-retry", URL: "https://retry.example",
			Token: "retry-token", Variant: "VARIANT_CPU", Accelerator: "NONE", Shape: "SHAPE_STANDARD",
		},
	})

	res, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.NoError(t, err)
	require.Equal(t, "m-s-retry", res.Endpoint)

	var ids []string
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPost && r.Path == "/v1beta/runtimes" {
			ids = append(ids, r.Query.Get("requestId"))
		}
	}
	require.Len(t, ids, 3, "two transient failures then success")
	require.NotEmpty(t, ids[0])
	for i, id := range ids {
		require.Equal(t, ids[0], id, "POST attempt %d used a different requestId", i)
	}
}

// §3.19: ALREADY_EXISTS (operation code 6) is an idempotent reattach only when
// the existing runtime has the requested hardware. Attaching a `run --gpu`
// request to a differently-priced CPU/other-accelerator runtime is unsafe.
func TestPublicAssignAlreadyExistsHardwareMismatch(t *testing.T) {
	gpuRuntime := func(acc, shape string) fakecolab.Runtime {
		return fakecolab.Runtime{
			Name: "runtimes/r-existing", Endpoint: "m-s-existing", URL: "https://existing.example",
			Token: "existing-token", Variant: "VARIANT_GPU", Accelerator: acc, Shape: shape,
		}
	}
	req := func(v colabapi.Variant, acc colabapi.Accelerator, shape colabapi.Shape) colabapi.AssignRequest {
		return colabapi.AssignRequest{NotebookHash: fixedHash(t), Variant: v, Accelerator: acc, Shape: shape}
	}

	cases := []struct {
		name     string
		existing fakecolab.Runtime
		request  colabapi.AssignRequest
		wantErr  bool
	}{
		{"accelerator mismatch", gpuRuntime("T4", "SHAPE_HIGHMEM"),
			req(colabapi.VariantGPU, colabapi.AccelL4, colabapi.ShapeHighRAM), true},
		{"variant mismatch", gpuRuntime("T4", "SHAPE_HIGHMEM"),
			req(colabapi.VariantTPU, colabapi.AccelT4, colabapi.ShapeHighRAM), true},
		{"shape mismatch", gpuRuntime("T4", "SHAPE_STANDARD"),
			req(colabapi.VariantGPU, colabapi.AccelT4, colabapi.ShapeHighRAM), true},
		{"matching hardware reattaches", gpuRuntime("T4", "SHAPE_HIGHMEM"),
			req(colabapi.VariantGPU, colabapi.AccelT4, colabapi.ShapeHighRAM), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := fakecolab.New()
			defer fake.Close()
			fake.SetPublicAssign(fakecolab.PublicOperationSpec{ErrorCode: 6, ErrorMessage: "exists"})
			fake.SetPublicGetRuntime(tc.existing, 0)

			res, err := publicClient(t, fake).Assign(context.Background(), tc.request)
			if tc.wantErr {
				require.Error(t, err, "mismatched hardware must not be silently reattached")
				require.Nil(t, res)
				require.True(t, errors.Is(err, colabapi.ErrRuntimeHardwareMismatch), "got %v", err)
				var mismatch *colabapi.HardwareMismatchError
				require.True(t, errors.As(err, &mismatch), "got %v", err)
				require.Contains(t, err.Error(), "requested")
				require.Contains(t, err.Error(), "accelerator")
				require.NotContains(t, err.Error(), "existing-token", "error must not leak credentials")
			} else {
				require.NoError(t, err)
				require.NotNil(t, res)
				require.True(t, res.Reattached)
			}
		})
	}
}

// §3.19: the same hardware guard applies when a lost POST response is
// reconciled by GET before the retry loop (§3.18's "operation created, :wait
// lost" case). A matching runtime still reattaches.
func TestPublicAssignReconcileRejectsHardwareMismatch(t *testing.T) {
	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "0")
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetFault(fakecolab.Fault{
		Method: http.MethodPost, PathContains: "/v1beta/runtimes",
		Status: http.StatusBadGateway, Times: 1,
	})
	fake.SetPublicGetRuntime(fakecolab.Runtime{
		Name: "runtimes/r-other", Endpoint: "m-s-other", URL: "https://other.example",
		Token: "other-token", Variant: "VARIANT_CPU", Accelerator: "NONE", Shape: "SHAPE_STANDARD",
	}, 0)

	res, err := publicClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: fixedHash(t), Variant: colabapi.VariantGPU,
		Accelerator: colabapi.AccelT4, Shape: colabapi.ShapeHighRAM,
	})
	require.Error(t, err)
	require.Nil(t, res)
	require.True(t, errors.Is(err, colabapi.ErrRuntimeHardwareMismatch), "got %v", err)
	require.Contains(t, err.Error(), "requested")
	require.NotContains(t, err.Error(), "other-token")
}

// Backward compatibility: Unassign on the tunnel path is unchanged.
func TestTunnelUnassignStillWorksUnderPublicClientFlag(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	c, err := colabapi.New(colabapi.Config{Domain: fake.TunURL(), PublicAPI: fake.PublicURL(), HTTPClient: fake.Client()})
	require.NoError(t, err)
	res, err := c.Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.NoError(t, err)
	require.NoError(t, c.Unassign(context.Background(), res.Endpoint))
	listed, err := c.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, listed)
}

// Env helpers port tests/test_client.py's env-var cases; defaults are documented
// in internal/colabapi/env.go.
func TestEnvHelpers(t *testing.T) {
	t.Setenv("COLAB_CLI_REQUEST_TIMEOUT", "300")
	require.Equal(t, 300*time.Second, colabapi.EnvRequestTimeout())
	t.Setenv("COLAB_CLI_REQUEST_TIMEOUT", "0")
	require.Equal(t, time.Second, colabapi.EnvRequestTimeout(), "0 clamps to the 1s minimum (no unbounded requests)")
	t.Setenv("COLAB_CLI_REQUEST_TIMEOUT", "999999")
	require.Equal(t, 24*time.Hour, colabapi.EnvRequestTimeout())
	t.Setenv("COLAB_CLI_REQUEST_TIMEOUT", "not-a-number")
	require.Equal(t, 60*time.Second, colabapi.EnvRequestTimeout())

	t.Setenv("COLAB_CLI_ASSIGN_RETRIES", "7")
	require.Equal(t, 7, colabapi.EnvAssignRetries())
	t.Setenv("COLAB_CLI_ASSIGN_RETRIES", "0")
	require.Equal(t, 0, colabapi.EnvAssignRetries())
	t.Setenv("COLAB_CLI_ASSIGN_RETRIES", "100")
	require.Equal(t, 10, colabapi.EnvAssignRetries())
	t.Setenv("COLAB_CLI_ASSIGN_RETRIES", "x")
	require.Equal(t, colabapi.DefaultRetries, colabapi.EnvAssignRetries())

	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "1.5")
	require.Equal(t, 1500*time.Millisecond, colabapi.EnvRetryBackoff())
	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "0")
	require.Equal(t, time.Duration(0), colabapi.EnvRetryBackoff())
	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "99999")
	require.Equal(t, 5*time.Minute, colabapi.EnvRetryBackoff())
}

// COLAB_CLI_ASSIGN_RETRIES=0 disables retries end-to-end (upstream
// test_assign_retries_can_be_disabled).
func TestEnvAssignRetriesDisabled(t *testing.T) {
	t.Setenv("COLAB_CLI_ASSIGN_RETRIES", "0")
	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "0")
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/tun/m/assign", Status: http.StatusBadGateway, Times: -1})

	c, err := colabapi.New(colabapi.Config{Domain: fake.TunURL(), HTTPClient: fake.Client()})
	require.NoError(t, err)
	_, err = c.Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.Error(t, err)
	posts := 0
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPost {
			posts++
		}
	}
	require.Equal(t, 1, posts, "no retries when COLAB_CLI_ASSIGN_RETRIES=0")
}

// COLAB_CLI_REQUEST_TIMEOUT must override the auth layer's 60 s client default
// (upstream test_request_timeout_override_and_explicit_precedence).
func TestEnvRequestTimeoutOverridesAuthDefault(t *testing.T) {
	t.Setenv("COLAB_CLI_REQUEST_TIMEOUT", "1")
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetFault(fakecolab.Fault{Method: http.MethodGet, PathContains: "/tun/m/assignments", Delay: 3 * time.Second})

	cli := fake.Client()
	cli.Timeout = 60 * time.Second // auth's default, not an explicit choice
	c, err := colabapi.New(colabapi.Config{Domain: fake.TunURL(), HTTPClient: cli})
	require.NoError(t, err)

	start := time.Now()
	_, err = c.List(context.Background())
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second, "env timeout must cut the 3 s server delay short")
}

// Env-driven retries/backoff drive the real retry path (upstream
// test_assign_refreshes_token_and_uses_bounded_backoff).
func TestEnvRetriesAndBackoffAreHonoured(t *testing.T) {
	t.Setenv("COLAB_CLI_ASSIGN_RETRIES", "2")
	t.Setenv("COLAB_CLI_RETRY_BACKOFF", "0.01")
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/tun/m/assign", Status: http.StatusBadGateway, Times: 2})

	c, err := colabapi.New(colabapi.Config{Domain: fake.TunURL(), HTTPClient: fake.Client()})
	require.NoError(t, err)
	res, err := c.Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.NoError(t, err)
	require.NotEmpty(t, res.Endpoint)
	posts := 0
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPost {
			posts++
		}
	}
	require.Equal(t, 3, posts, "first two POSTs fail, third succeeds")
}
