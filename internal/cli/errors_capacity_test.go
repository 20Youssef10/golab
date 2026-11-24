package cli

// Upstream issues #282/#273/#236: a 503 on /assign is a capacity problem and
// must produce a stable, actionable reason in both text and structured output
// (both modes print the same mapError message on stderr).

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/colabapi"
)

func TestMapErrorCapacityUnavailable(t *testing.T) {
	cause := fmt.Errorf("colab POST /tun/m/assign: HTTP 503: %w", colabapi.ErrCapacity)
	mapped := mapError(cause)
	var ee *exitError
	require.True(t, asExitError(mapped, &ee))
	require.Equal(t, 1, ee.code)
	require.Contains(t, ee.msg, colabapi.ReasonCapacityUnavailable)
	require.Contains(t, ee.msg, "capacity")
	require.Contains(t, ee.msg, "retry")

	// Existing typed paths keep their original messages and exit codes.
	tooMany := mapError(fmt.Errorf("wrapped: %w", colabapi.ErrTooManyAssignments))
	var tm *exitError
	require.True(t, asExitError(tooMany, &tm))
	require.Equal(t, 1, tm.code)
	require.Contains(t, tm.msg, "precondition failed")
	require.NotContains(t, tm.msg, colabapi.ReasonCapacityUnavailable)
}
