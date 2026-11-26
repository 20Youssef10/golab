package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/app"
)

// ttlNow is a test seam with a time.Now default. The TTL check is deliberately
// LOCAL: it reads sessions.json only, starts no daemon and installs no timer.
var ttlNow = time.Now

const (
	ttlActionWarn = "warn"
	ttlActionStop = "stop"
)

// parseTTLAction validates the additive --ttl-action flag before allocation.
func parseTTLAction(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return ttlActionWarn, nil
	}
	switch v {
	case ttlActionWarn, ttlActionStop:
		return v, nil
	default:
		return "", usagef("[colab] Invalid --ttl-action %q: expected warn or stop.", value)
	}
}

// ttlExpiry turns a --ttl duration into an absolute expiry. A non-positive
// duration means "no expiry" and is rejected by the command before this point.
func ttlExpiry(ttl time.Duration) *time.Time {
	if ttl <= 0 {
		return nil
	}
	v := ttlNow().Add(ttl).UTC()
	return &v
}

// humanDuration renders an elapsed duration the way the warning reads:
// "12m ago", "2h3m ago", "45s ago". No fractional seconds leak into output.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	d = d.Round(time.Minute)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// checkExpiredSessions runs on every command invocation (from the root's
// PersistentPreRunE) after app.Deps has been resolved. It never calls the
// control plane to discover sessions: only local records carry ExpiresAt.
func checkExpiredSessions(ctx context.Context, d *app.Deps) {
	if d == nil || d.Store == nil {
		return
	}
	local, err := d.Store.List()
	if err != nil || len(local) == 0 {
		return
	}
	now := ttlNow()
	for name, s := range local {
		if s.ExpiresAt == nil || !now.After(*s.ExpiresAt) {
			continue
		}
		fmt.Fprintf(d.Stderr, "[colab] session %s expired %s ago.\n", name, humanDuration(now.Sub(*s.ExpiresAt)))
		if s.TTLAction != ttlActionStop {
			continue
		}
		// Best-effort: an expired credential may already be gone, and a local
		// check must never make an unrelated command fail. The local record is
		// removed either way so the warning is not repeated forever.
		if client, err := d.ColabClient(ctx); err == nil {
			_ = client.UnassignRuntime(ctx, s.Endpoint, s.RuntimeName)
		}
		_ = d.Store.Delete(name)
	}
}
