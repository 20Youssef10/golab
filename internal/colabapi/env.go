package colabapi

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment overrides honoured by New. Upstream (client.py:300-304, 442-443)
// reads these three variables; we keep the same names and defaults but clamp
// every value so AGENTS.md #5 ("explicit timeouts, bounded retries") holds even
// for a misconfigured environment. The values are never logged.
const (
	// envRequestTimeoutDefault is the explicit per-request timeout when
	// COLAB_CLI_REQUEST_TIMEOUT is unset. Upstream leaves requests unbounded;
	// we always install a finite timeout.
	envRequestTimeoutDefault = defaultHTTPTimeout // 60 s
	envRequestTimeoutMin     = time.Second
	envRequestTimeoutMax     = 24 * time.Hour

	// Defaults mirror client.py's COLAB_CLI_ASSIGN_RETRIES (3) and
	// COLAB_CLI_RETRY_BACKOFF (10 s).
	envRetriesMax = 10
	envBackoffMax = 5 * time.Minute
)

// EnvRequestTimeout returns the COLAB_CLI_REQUEST_TIMEOUT override in seconds
// (parsed as a float, as upstream does). Unset, invalid, non-positive or
// overflowing values fall back to 60 s; values are clamped to [1 s, 24 h] so
// a request can never be unbounded (AGENTS.md #5).
func EnvRequestTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("COLAB_CLI_REQUEST_TIMEOUT"))
	if raw == "" {
		return envRequestTimeoutDefault
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return envRequestTimeoutDefault
	}
	// Upstream treats 0 as "no timeout"; AGENTS.md #5 forbids unbounded
	// requests, so non-positive values clamp to the 1 s minimum instead.
	if v <= 0 {
		return envRequestTimeoutMin
	}
	if v < envRequestTimeoutMin.Seconds() {
		return envRequestTimeoutMin
	}
	if v > envRequestTimeoutMax.Seconds() {
		return envRequestTimeoutMax
	}
	return time.Duration(v * float64(time.Second))
}

// EnvAssignRetries returns the COLAB_CLI_ASSIGN_RETRIES override. Unset or
// invalid values use DefaultRetries (3); 0 disables retries; the upper bound is
// 10 to keep the retry loop bounded.
func EnvAssignRetries() int {
	raw := strings.TrimSpace(os.Getenv("COLAB_CLI_ASSIGN_RETRIES"))
	if raw == "" {
		return DefaultRetries
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return DefaultRetries
	}
	if v < 0 {
		return 0
	}
	if v > envRetriesMax {
		return envRetriesMax
	}
	return v
}

// EnvRetryBackoff returns the COLAB_CLI_RETRY_BACKOFF override in seconds.
// Unset or invalid values use DefaultBackoff (10 s); 0 disables the delay;
// values are clamped to [0, 5 min].
func EnvRetryBackoff() time.Duration {
	raw := strings.TrimSpace(os.Getenv("COLAB_CLI_RETRY_BACKOFF"))
	if raw == "" {
		return DefaultBackoff
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return DefaultBackoff
	}
	// Upstream clamps negative values to zero (max(0.0, float(...))).
	if v <= 0 {
		return 0
	}
	if v > envBackoffMax.Seconds() {
		return envBackoffMax
	}
	return time.Duration(v * float64(time.Second))
}

// ConfigFromEnv returns a Config with the three environment-driven fields
// populated. Callers may override any field before passing it to New; New also
// applies the same helpers itself when a field is left at its zero value.
func ConfigFromEnv() Config {
	return Config{
		RequestTimeout: EnvRequestTimeout(),
		Retries:        EnvAssignRetries(),
		Backoff:        EnvRetryBackoff(),
	}
}
