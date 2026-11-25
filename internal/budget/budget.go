// Package budget implements an advisory compute-unit cost guard.
//
// It never invents a price and never converts compute units into money.
// Thresholds are expressed in CU and CU/hour, sourced only from the existing
// GET /tun/m/ccu-info call. A tripped threshold is a warning unless the caller
// explicitly sets Policy.Enforce (wired to --enforce, or read from a profile's
// `enforce` key). The balance threshold is a minimum reserve floor (refuse at
// or below it); the hourly threshold is a rate ceiling (refuse at or above it).
package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/20Youssef10/golab/internal/colabapi"
)

var (
	// ErrUnknownKey means the profile file contains a key outside the small
	// supported subset.
	ErrUnknownKey = errors.New("budget: unknown profile key")
	// ErrPriceTable means the file tried to supply unsourced prices.
	ErrPriceTable = errors.New("budget: price/rate tables are not supported")
	// ErrUnsupportedVersion means the profile schema version is not 1.
	ErrUnsupportedVersion = errors.New("budget: unsupported profile version")
	// ErrProfileNotFound means the requested named profile is absent.
	ErrProfileNotFound = errors.New("budget: profile not found")
	// ErrInvalidThreshold means a threshold is not a positive finite number.
	ErrInvalidThreshold = errors.New("budget: threshold must be a positive finite number")
	// ErrInvalidBoolean means `enforce` is not a boolean value.
	ErrInvalidBoolean = errors.New("budget: enforce must be a boolean")
)

// Policy holds advisory thresholds in compute units only. Enforce must be set
// by the caller when it actually intends to refuse an allocation.
type Policy struct {
	// MaxBalance is a MINIMUM RESERVE FLOOR: do not allocate when the remaining
	// paid compute-unit balance is at or below it. The name is legacy; the
	// config key and flag are still `max_balance` / `--max-balance`.
	MaxBalance float64
	// MaxHourly is a rate ceiling: refuse when the hourly rate is at or above it.
	MaxHourly float64
	Enforce   bool
}

// Decision is the result of a budget check. Reasons are human-readable and
// always state the unit; no monetary estimate is ever included.
type Decision struct {
	Allowed  bool     `json:"allowed"`
	Enforced bool     `json:"enforced"`
	Unknown  bool     `json:"unknown,omitempty"`
	Reasons  []string `json:"reasons"`
	Source   string   `json:"source"`
}

const source = "GET /tun/m/ccu-info (compute units only)"

// Check evaluates usage against policy. A nil usage means ccu-info was
// unavailable: that is reported as unknown, never as "safe to spend".
func Check(ctx context.Context, usage *colabapi.Consumption, policy Policy) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	d := Decision{
		Allowed: true, Enforced: policy.Enforce, Reasons: []string{}, Source: source,
	}
	if usage == nil {
		d.Unknown = true
		d.Reasons = append(d.Reasons, "ccu-info unavailable: usage is unknown")
		if policy.Enforce {
			d.Allowed = false
			d.Reasons = append(d.Reasons, "denied: usage is unknown and enforcement is enabled")
		}
		return d, nil
	}
	balance, hourly := usage.PaidComputeUnitsBalance, usage.ConsumptionRateHourly
	if policy.MaxBalance > 0 {
		detail := fmt.Sprintf("balance %.2f compute units (minimum reserve floor %.2f)", balance, policy.MaxBalance)
		if balance <= policy.MaxBalance {
			d.Reasons = append(d.Reasons, "reserve floor reached: "+detail)
			if policy.Enforce {
				d.Allowed = false
			}
		} else {
			d.Reasons = append(d.Reasons, "above reserve floor: "+detail)
		}
	}
	if policy.MaxHourly > 0 {
		detail := fmt.Sprintf("usage rate %.2f compute units/hour (advisory max %.2f)", hourly, policy.MaxHourly)
		if hourly >= policy.MaxHourly {
			d.Reasons = append(d.Reasons, "threshold tripped: "+detail)
			if policy.Enforce {
				d.Allowed = false
			}
		} else {
			d.Reasons = append(d.Reasons, "within threshold: "+detail)
		}
	}
	if len(d.Reasons) == 0 {
		d.Reasons = append(d.Reasons, "no advisory thresholds configured")
	}
	if !d.Allowed {
		d.Reasons = append(d.Reasons, "allocation refused because --enforce was supplied")
	}
	return d, nil
}

// LoadProfile reads the small, strict subset of golab.yaml that describes a
// budget. It supports `version: 1`, `defaults.budget` and named
// `profiles.<name>.budget` blocks with `max_balance` / `max_hourly` / `enforce`
// only. `max_balance` is the legacy key name for the minimum reserve floor; it
// is kept for backward compatibility. A named profile overrides defaults,
// including an explicit `enforce: false`. Paths are local; the profile never
// contains a price table.
func LoadProfile(ctx context.Context, path, profile string) (Policy, error) {
	if err := ctx.Err(); err != nil {
		return Policy{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	versionSeen := false
	defaults := Policy{}
	profiles := map[string]Policy{}
	// A bool's zero value cannot distinguish "not configured" from an explicit
	// `enforce: false`, so track which named profiles set the key themselves.
	enforceSet := map[string]bool{}
	section, sub, named := "", "", ""
	for i, raw := range strings.Split(string(b), "\n") {
		if err := ctx.Err(); err != nil {
			return Policy{}, err
		}
		line := strings.TrimSpace(strings.ReplaceAll(raw, "\t", "    "))
		if strings.Contains(raw, "\t") {
			return Policy{}, fmt.Errorf("%w: tab indentation on line %d", ErrUnknownKey, i+1)
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if j := strings.Index(line, " #"); j >= 0 {
			line = strings.TrimSpace(line[:j])
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent%2 != 0 {
			return Policy{}, fmt.Errorf("%w: odd indentation on line %d", ErrUnknownKey, i+1)
		}
		key, value, _ := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		hasValue := value != ""
		if strings.Contains(strings.ToLower(key), "price") ||
			strings.Contains(strings.ToLower(key), "rate") ||
			strings.Contains(strings.ToLower(key), "cost") {
			return Policy{}, fmt.Errorf("%w: %q", ErrPriceTable, key)
		}
		if indent == 0 {
			if hasValue && key == "version" {
				v, err := strconv.Atoi(value)
				if err != nil || v != 1 {
					return Policy{}, fmt.Errorf("%w: %q", ErrUnsupportedVersion, value)
				}
				versionSeen = true
				section, sub, named = "", "", ""
				continue
			}
			switch key {
			case "defaults", "profiles":
				if hasValue {
					return Policy{}, fmt.Errorf("%w: %q", ErrUnknownKey, key)
				}
				section, sub, named = key, "", ""
			default:
				return Policy{}, fmt.Errorf("%w: %q", ErrUnknownKey, key)
			}
			continue
		}
		if section == "defaults" && indent == 2 && key == "budget" && !hasValue {
			sub, named = "budget", ""
			continue
		}
		if section == "profiles" && indent == 2 && !hasValue {
			sub, named = "", key
			profiles[key] = Policy{}
			continue
		}
		if section == "profiles" && indent == 4 && key == "budget" && !hasValue {
			sub = "budget"
			continue
		}
		if sub != "budget" || !hasValue {
			return Policy{}, fmt.Errorf("%w: %q on line %d", ErrUnknownKey, key, i+1)
		}
		if key == "enforce" {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return Policy{}, fmt.Errorf("%w: %q on line %d", ErrInvalidBoolean, value, i+1)
			}
			if named != "" {
				p := profiles[named]
				p.Enforce = b
				profiles[named] = p
				enforceSet[named] = true
			} else {
				defaults.Enforce = b
			}
			continue
		}
		if key != "max_balance" && key != "max_hourly" {
			return Policy{}, fmt.Errorf("%w: %q on line %d", ErrUnknownKey, key, i+1)
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return Policy{}, fmt.Errorf("%w: %q", ErrInvalidThreshold, value)
		}
		if named != "" {
			p := profiles[named]
			setThreshold(&p, key, n)
			profiles[named] = p
		} else {
			setThreshold(&defaults, key, n)
		}
	}
	if !versionSeen {
		return Policy{}, fmt.Errorf("%w: missing version", ErrUnsupportedVersion)
	}
	if profile == "" {
		return defaults, nil
	}
	p, ok := profiles[profile]
	if !ok {
		return Policy{}, fmt.Errorf("%w: %q", ErrProfileNotFound, profile)
	}
	if p.MaxBalance == 0 {
		p.MaxBalance = defaults.MaxBalance
	}
	if p.MaxHourly == 0 {
		p.MaxHourly = defaults.MaxHourly
	}
	if !enforceSet[profile] {
		p.Enforce = defaults.Enforce
	}
	return p, nil
}

func setThreshold(p *Policy, key string, n float64) {
	if key == "max_balance" {
		p.MaxBalance = n
	} else {
		p.MaxHourly = n
	}
}
