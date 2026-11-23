// Ported upstream tests (google-colab-cli HEAD):
//
//	tests/test_client.py::test_client_assign_new
//	tests/test_client.py::test_client_assign_existing
//	tests/test_client.py::test_assign_recovers_before_another_post
//	tests/test_client.py::test_assign_refreshes_token_and_uses_bounded_backoff
//	tests/test_client.py::test_client_assign_412_raises_too_many_assignments
//	tests/test_client.py::test_client_unassign
//	tests/test_client.py::test_client_list_assignments
//	tests/test_client.py::test_public_client_list_assignments
//	tests/test_client.py::test_check_api_access_caches_results
//	tests/test_consumption_client.py::test_get_consumption_user_info_from_ccu_info
//	tests/test_consumption.py::test_format_consumption_status
package colabapi_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

func testClient(t *testing.T, fake *fakecolab.Server) colabapi.Client {
	t.Helper()
	c, err := colabapi.New(colabapi.Config{
		Domain:      fake.TunURL(),
		PublicAPI:   fake.PublicURL(),
		HTTPClient:  fake.Client(),
		ClientAgent: "colab-go-test",
		Retries:     1,
		Backoff:     0,
	})
	require.NoError(t, err)
	return c
}

func fixedHash(t *testing.T) colabapi.NotebookHash {
	t.Helper()
	h := colabapi.NewNotebookHash(uuid.MustParse("12345678-1234-5678-1234-567812345678"))
	require.Len(t, string(h), 44)
	return h
}

// Ports docs/public_api_migration_plan.md §5's runtimeId derivation rule.
func TestNotebookHashRuntimeID(t *testing.T) {
	h := colabapi.NewNotebookHash(uuid.MustParse("12345678-1234-5678-1234-567812345678"))
	require.Equal(t, "r-12345678-1234-5678-1234-567812345678", h.RuntimeID())
	require.Equal(t, h.RuntimeID(), h.RuntimeID(), "must be deterministic for reconciliation")
	// A malformed hash still yields a syntactically valid runtime id.
	require.Regexp(t, `^r-[a-z0-9-]+$`, colabapi.NotebookHash("garbage").RuntimeID())
}

// Ports docs/02 §2.1 (uuid_to_web_safe_base64).
func TestNotebookHashEncoding(t *testing.T) {
	h := colabapi.NewNotebookHash(uuid.MustParse("12345678-1234-5678-1234-567812345678"))
	require.Equal(t, "12345678_1234_5678_1234_567812345678........", string(h))
	require.Equal(t, 44, len(h))
	require.True(t, h.Valid())

	random, err := colabapi.RandomNotebookHash()
	require.NoError(t, err)
	require.True(t, random.Valid())
	require.NotEqual(t, h, random)
	require.False(t, colabapi.NotebookHash("bad").Valid())
}

// Ports tests/test_client.py::test_client_assign_new (XSSI + XSRF + headers).
func TestAssignXSSIXSRFAndHeaders(t *testing.T) {
	fake := fakecolab.New(fakecolab.WithXSSI(")]}'\n"))
	defer fake.Close()
	c := testClient(t, fake)

	res, err := c.Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: fixedHash(t),
		Variant:      colabapi.VariantDefault,
		Accelerator:  colabapi.AccelNone,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Endpoint)
	require.Contains(t, res.RuntimeProxyInfo.URL, "runtime-")
	require.Equal(t, 3600, res.RuntimeProxyInfo.TokenExpiresInSeconds)
	require.False(t, res.Reattached)

	log := fake.Requests()
	require.Len(t, log, 2)
	require.Equal(t, http.MethodGet, log[0].Method)
	require.Equal(t, http.MethodPost, log[1].Method)
	require.Equal(t, fake.LastXSRFToken(), log[1].Headers.Get("X-Goog-Colab-Token"))
	for _, r := range log {
		require.Equal(t, "application/json", r.Headers.Get("Accept"))
		require.Equal(t, "colab-go-test", r.Headers.Get("X-Colab-Client-Agent"))
		require.Equal(t, "0", r.Query.Get("authuser"))
	}
}

// Ports tests/test_client.py::test_client_assign_existing (idempotent re-GET).
func TestAssignIdempotentReattach(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	c := testClient(t, fake)
	req := colabapi.AssignRequest{NotebookHash: fixedHash(t), Variant: colabapi.VariantGPU, Accelerator: colabapi.AccelT4}

	first, err := c.Assign(context.Background(), req)
	require.NoError(t, err)
	second, err := c.Assign(context.Background(), req)
	require.NoError(t, err)

	require.Equal(t, first.Endpoint, second.Endpoint)
	require.Equal(t, first.RuntimeProxyInfo.Token, second.RuntimeProxyInfo.Token)
	require.True(t, second.Reattached)
	posts := 0
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPost {
			posts++
		}
	}
	require.Equal(t, 1, posts)
}

// Ports tests/test_client.py::test_assign_recovers_before_another_post.
// The first POST lands in the fake but its response is lost (timeout); the
// reconcile GET must discover the assignment and avoid a second POST.
func TestAssignRetryReconcilesAfterTimeout(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetFault(fakecolab.Fault{
		Method:       http.MethodPost,
		PathContains: "/tun/m/assign",
		Status:       http.StatusBadGateway,
		Hang:         true,
		ApplyHandler: true,
	})
	cli := fake.Client()
	cli.Timeout = 400 * time.Millisecond
	c, err := colabapi.New(colabapi.Config{
		Domain: fake.TunURL(), HTTPClient: cli, ClientAgent: "colab-go-test",
		Retries: 2, Backoff: time.Millisecond,
	})
	require.NoError(t, err)

	res, err := c.Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.NoError(t, err)
	require.NotEmpty(t, res.Endpoint)
	require.True(t, res.Reattached)

	posts, gets := 0, 0
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/assign") {
			posts++
		}
		if r.Method == http.MethodGet && strings.Contains(r.Path, "/assign") {
			gets++
		}
	}
	require.Equal(t, 1, posts, "no double-billing")
	require.Equal(t, 2, gets, "initial GET + reconcile GET")
}

// Ports tests/test_client.py::test_assign_refreshes_token_and_uses_bounded_backoff.
func TestAssignBoundedBackoffAndFreshXSRF(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	// The first two POST responses are lost. The reconcile GET keeps returning
	// the pending shape, minting a fresh XSRF token each time.
	fake.SetFault(fakecolab.Fault{
		Method:       http.MethodPost,
		PathContains: "/tun/m/assign",
		Status:       http.StatusBadGateway,
		Times:        2,
	})
	c, err := colabapi.New(colabapi.Config{
		Domain: fake.TunURL(), HTTPClient: fake.Client(), ClientAgent: "colab-go-test",
		Retries: 2, Backoff: 40 * time.Millisecond,
	})
	require.NoError(t, err)

	start := time.Now()
	_, err = c.Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.NoError(t, err)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 110*time.Millisecond, "backoff must grow 40ms then 80ms")

	var tokens []string
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPost {
			tokens = append(tokens, r.Headers.Get("X-Goog-Colab-Token"))
		}
	}
	require.Len(t, tokens, 3)
	require.NotEqual(t, tokens[0], tokens[1], "reconcile must refresh the XSRF token")
	require.NotEqual(t, tokens[1], tokens[2])
}

// Ports tests/test_client.py::test_client_assign_412_raises_too_many_assignments
// and commands/session.py::new's "400 with accelerator" friendly error.
func TestAssignTypedErrors(t *testing.T) {
	t.Run("412", func(t *testing.T) {
		fake := fakecolab.New()
		defer fake.Close()
		fake.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/tun/m/assign", Status: http.StatusPreconditionFailed})
		_, err := testClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
		require.Error(t, err)
		require.True(t, errors.Is(err, colabapi.ErrTooManyAssignments), "got %v", err)
	})

	t.Run("400 with accelerator", func(t *testing.T) {
		fake := fakecolab.New()
		defer fake.Close()
		fake.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/tun/m/assign", Status: http.StatusBadRequest, Body: `{"error":"no quota"}`})
		_, err := testClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{
			NotebookHash: fixedHash(t), Accelerator: colabapi.AccelA100,
		})
		require.Error(t, err)
		require.True(t, errors.Is(err, colabapi.ErrNoQuota), "got %v", err)
		var se *colabapi.StatusError
		require.True(t, errors.As(err, &se))
		require.Equal(t, 400, se.Code)
	})

	t.Run("400 without accelerator is a plain StatusError", func(t *testing.T) {
		fake := fakecolab.New()
		defer fake.Close()
		fake.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/tun/m/assign", Status: http.StatusBadRequest, Body: `{"error":"bad request"}`})
		_, err := testClient(t, fake).Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
		require.Error(t, err)
		require.False(t, errors.Is(err, colabapi.ErrNoQuota))
		var se *colabapi.StatusError
		require.True(t, errors.As(err, &se))
		require.Equal(t, 400, se.Code)
		require.Contains(t, se.Body, "bad request")
	})
}

// Ports tests/test_client.py::test_client_unassign.
func TestUnassignHandshake(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	c := testClient(t, fake)
	req := colabapi.AssignRequest{NotebookHash: fixedHash(t)}
	res, err := c.Assign(context.Background(), req)
	require.NoError(t, err)

	require.NoError(t, c.Unassign(context.Background(), res.Endpoint))
	listed, err := c.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, listed)

	log := fake.Requests()
	require.GreaterOrEqual(t, len(log), 4)
	require.Equal(t, http.MethodGet, log[2].Method)
	require.Equal(t, http.MethodPost, log[3].Method)
	require.Contains(t, log[3].Path, "/tun/m/unassign/")
	require.NotEmpty(t, log[3].Headers.Get("X-Goog-Colab-Token"))
}

// Ports tests/test_client.py::test_client_list_assignments.
func TestListTunnel(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	c := testClient(t, fake)
	res, err := c.Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: fixedHash(t), Variant: colabapi.VariantTPU,
		Accelerator: colabapi.AccelV5E1, Shape: colabapi.ShapeHighRAM,
	})
	require.NoError(t, err)

	listed, err := c.List(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, res.Endpoint, listed[0].Endpoint)
	require.Equal(t, colabapi.AccelV5E1, listed[0].Accelerator)
	require.Equal(t, colabapi.AssignmentTPU, listed[0].Variant)
	require.Equal(t, colabapi.ShapeHighRAM, listed[0].MachineShape)
}

// Ports tests/test_client.py::test_public_client_list_assignments plus the
// expireTime/endpoint-fallback rules in client.py::_list_assignments_public.
func TestListPublicMapping(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetRuntimes([]fakecolab.Runtime{
		{
			Name: "runtimes/fallback-id", ExpireTime: time.Now().UTC().Add(2 * time.Hour),
			URL: "https://runtime.example", Token: "proxy-token",
			Variant: "VARIANT_GPU", Accelerator: "T4", Shape: "SHAPE_HIGHMEM",
		},
		{
			Name: "runtimes/expired", Endpoint: "explicit-endpoint",
			ExpireTime: time.Now().UTC().Add(-time.Hour),
			URL:        "https://expired.example", Token: "expired-token",
			Variant: "VARIANT_TPU", Accelerator: "NOT_AN_ACCEL", Shape: "SHAPE_STANDARD",
		},
	})
	c, err := colabapi.New(colabapi.Config{
		Domain: fake.TunURL(), PublicAPI: fake.PublicURL(),
		HTTPClient: fake.Client(), ClientAgent: "colab-go-test", UsePublicAPI: true,
	})
	require.NoError(t, err)

	listed, err := c.List(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, "fallback-id", listed[0].Endpoint)
	require.Equal(t, colabapi.AccelT4, listed[0].Accelerator)
	require.Equal(t, colabapi.AssignmentGPU, listed[0].Variant)
	require.Equal(t, colabapi.ShapeHighRAM, listed[0].MachineShape)
	require.InDelta(t, 7200, listed[0].RuntimeProxyInfo.TokenExpiresInSeconds, 5)

	require.Equal(t, "explicit-endpoint", listed[1].Endpoint)
	require.Equal(t, colabapi.AccelNone, listed[1].Accelerator)
	require.Equal(t, colabapi.AssignmentTPU, listed[1].Variant)
	require.Equal(t, colabapi.ShapeStandard, listed[1].MachineShape)
	require.Equal(t, 0, listed[1].RuntimeProxyInfo.TokenExpiresInSeconds)

	// Public API requests must not carry authuser=0.
	for _, r := range fake.Requests() {
		require.Empty(t, r.Query.Get("authuser"))
	}
}

// Ports tests/test_consumption_client.py::test_get_consumption_user_info_from_ccu_info
// and tests/test_consumption.py::test_format_consumption_status.
func TestUsage(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetConsumption(298.53, 0.07, 2)
	c := testClient(t, fake)

	info, err := c.Usage(context.Background())
	require.NoError(t, err)
	require.InDelta(t, 298.53, info.PaidComputeUnitsBalance, 0.0001)
	require.InDelta(t, 0.07, info.ConsumptionRateHourly, 0.0001)
	require.Equal(t, 2, info.AssignmentsCount)
	require.Equal(t,
		"Current balance: 298.53 compute units\nUsage rate: 0.07/hr\nActive assignments: 2",
		info.String())
	require.Equal(t, "0", fake.Requests()[0].Query.Get("authuser"))
}

// Ports tests/test_client.py::test_check_api_access_caches_results.
func TestCheckAPIAccess(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	fake.SetFault(fakecolab.Fault{PathContains: "/v1beta/runtimes", Status: http.StatusForbidden, Body: "denied"})
	c := testClient(t, fake)

	pubOK, tunOK, err := c.CheckAPIAccess(context.Background())
	require.NoError(t, err)
	require.False(t, pubOK)
	require.True(t, tunOK)
}

// Ports C7/C8 from docs/02 §2 and automation.py's drive_propagation_failure_message.
func TestCredentialsPropagation(t *testing.T) {
	fake := fakecolab.New()
	defer fake.Close()
	c := testClient(t, fake)

	prop, err := c.CredentialsPropagation(context.Background(), "endpoint-1")
	require.NoError(t, err)
	require.True(t, prop.Success)
	require.Empty(t, prop.UnauthorizedRedirectURI)

	fake.SetPropagationAuthorizeURL("https://accounts.example/consent")
	prop, err = c.CredentialsPropagation(context.Background(), "endpoint-2")
	require.NoError(t, err)
	require.False(t, prop.Success)
	require.Equal(t, "https://accounts.example/consent", prop.UnauthorizedRedirectURI)
	require.NotNil(t, prop.Confirm)
	confirmed, err := prop.Confirm(context.Background())
	require.NoError(t, err)
	require.True(t, confirmed.Success)
}

// Security golden: tokens must not reach slog output or error strings.
func TestSecretsNeverLeak(t *testing.T) {
	fake := fakecolab.New(
		fakecolab.WithXSRFPrefix("XSRF_SUPERSECRET_"),
		fakecolab.WithProxyTokenPrefix("PROXY_SUPERSECRET_"),
	)
	defer fake.Close()
	var sink bytes.Buffer
	c, err := colabapi.New(colabapi.Config{
		Domain: fake.TunURL(), HTTPClient: fake.Client(), ClientAgent: "colab-go-test",
		Logger: slog.New(logx.NewHandler(&sink, true)),
	})
	require.NoError(t, err)

	_, err = c.Assign(context.Background(), colabapi.AssignRequest{NotebookHash: fixedHash(t)})
	require.NoError(t, err)
	logs := sink.String()
	require.NotContains(t, logs, "XSRF_SUPERSECRET_")
	require.NotContains(t, logs, "PROXY_SUPERSECRET_")

	// Even when a server echoes a token in an error body, the typed error is redacted.
	fake.SetFault(fakecolab.Fault{
		Method: http.MethodPost, PathContains: "/tun/m/assign", Status: http.StatusBadRequest,
		Body: `{"token":"BODY_SUPERSECRET_1234","error":"bad"}`,
	})
	_, err = c.Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: colabapi.NewNotebookHash(uuid.New()),
		Accelerator:  colabapi.AccelT4,
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "BODY_SUPERSECRET_1234")
	var se *colabapi.StatusError
	require.True(t, errors.As(err, &se))
	require.NotContains(t, se.Body, "BODY_SUPERSECRET_1234")
	require.NotContains(t, sink.String(), "BODY_SUPERSECRET_1234")
}
