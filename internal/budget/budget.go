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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/20Youssef10/golab/internal/colabapi"
	"go.yaml.in/yaml/v3"
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

// versionField accepts schema version 1 in either plain or quoted form.
// Anything else (including a missing version) is an unsupported version.
type versionField int

func (v *versionField) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: expected a scalar on line %d", ErrUnsupportedVersion, n.Line)
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(n.Value))
	if err != nil || parsed != 1 {
		return fmt.Errorf("%w: %q on line %d", ErrUnsupportedVersion, n.Value, n.Line)
	}
	*v = versionField(parsed)
	return nil
}

// positiveFloat is a CU threshold. Native YAML numbers, quoted scalars and
// block scalars are all accepted, but the value must still be a positive,
// finite number. `set` distinguishes an absent key from a valid threshold.
type positiveFloat struct {
	value float64
	set   bool
}

func (p *positiveFloat) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: expected a scalar on line %d", ErrInvalidThreshold, n.Line)
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(n.Value), 64)
	if err != nil || parsed <= 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return fmt.Errorf("%w: %q on line %d", ErrInvalidThreshold, n.Value, n.Line)
	}
	p.value, p.set = parsed, true
	return nil
}

// strictBool records whether `enforce` was present so an explicit false can
// override the defaults value during named-profile inheritance.
type strictBool struct {
	value bool
	set   bool
}

func (b *strictBool) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: expected a scalar on line %d", ErrInvalidBoolean, n.Line)
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(n.Value))
	if err != nil {
		return fmt.Errorf("%w: %q on line %d", ErrInvalidBoolean, n.Value, n.Line)
	}
	b.value, b.set = parsed, true
	return nil
}

// profileFile is the complete, strict schema of the budget portion of
// golab.yaml. Decoding with KnownFields(true) rejects any key outside it.
type profileFile struct {
	Version  versionField             `yaml:"version"`
	Defaults profileConfig            `yaml:"defaults"`
	Profiles map[string]profileConfig `yaml:"profiles"`
}

type profileConfig struct {
	Budget budgetConfig `yaml:"budget"`
}

type budgetConfig struct {
	MaxBalance positiveFloat `yaml:"max_balance"`
	MaxHourly  positiveFloat `yaml:"max_hourly"`
	Enforce    strictBool    `yaml:"enforce"`
}

// LoadProfile reads the strict YAML schema that describes a budget:
// `version: 1`, `defaults.budget` and named `profiles.<name>.budget` blocks with
// `max_balance` / `max_hourly` / `enforce` only. `max_balance` is the legacy
// key name for the minimum reserve floor and is kept for backward
// compatibility. A named profile overrides defaults, including an explicit
// `enforce: false`. Paths are local; price/rate tables are rejected.
func LoadProfile(ctx context.Context, path, profile string) (Policy, error) {
	if err := ctx.Err(); err != nil {
		return Policy{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	if err := ctx.Err(); err != nil {
		return Policy{}, err
	}

	// Parse the document once to reject price tables and valueless keys by
	// name regardless of where they appear. yaml.v3 does all of the actual
	// YAML parsing here (document markers, quoting, block and flow styles).
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return Policy{}, fmt.Errorf("%w: %w", ErrUnknownKey, err)
	}
	if err := validateProfileNodes(&doc); err != nil {
		return Policy{}, err
	}

	// Strict decoding into the typed schema rejects unknown keys and duplicate
	// keys. The scalar types above preserve the package's typed value errors.
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var cfg profileFile
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Policy{}, fmt.Errorf("%w: missing version", ErrUnsupportedVersion)
		}
		return Policy{}, mapDecodeError(err)
	}
	if cfg.Version != 1 {
		return Policy{}, fmt.Errorf("%w: missing version", ErrUnsupportedVersion)
	}

	defaults := policyFrom(cfg.Defaults.Budget)
	if profile == "" {
		return defaults, nil
	}
	named, ok := cfg.Profiles[profile]
	if !ok {
		return Policy{}, fmt.Errorf("%w: %q", ErrProfileNotFound, profile)
	}
	got := policyFrom(named.Budget)
	if !named.Budget.MaxBalance.set {
		got.MaxBalance = defaults.MaxBalance
	}
	if !named.Budget.MaxHourly.set {
		got.MaxHourly = defaults.MaxHourly
	}
	if !named.Budget.Enforce.set {
		got.Enforce = defaults.Enforce
	}
	return got, nil
}

func policyFrom(b budgetConfig) Policy {
	return Policy{
		MaxBalance: b.MaxBalance.value,
		MaxHourly:  b.MaxHourly.value,
		Enforce:    b.Enforce.value,
	}
}

// mapDecodeError converts yaml.v3's structural errors (unknown fields,
// duplicate keys and type mismatches) into the package's ErrUnknownKey, while
// leaving the typed errors raised by the scalar unmarshalers untouched.
func mapDecodeError(err error) error {
	for _, sentinel := range []error{
		ErrUnknownKey, ErrPriceTable, ErrUnsupportedVersion, ErrProfileNotFound,
		ErrInvalidThreshold, ErrInvalidBoolean,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		return fmt.Errorf("%w: %s", ErrUnknownKey, strings.Join(typeErr.Errors, "; "))
	}
	return fmt.Errorf("%w: %w", ErrUnknownKey, err)
}

// validateProfileNodes preserves two semantics of the old strict subset:
// price/rate/cost keys are never accepted anywhere, and a scalar key that
// requires a value cannot be left empty.
func validateProfileNodes(n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			if err := validateProfileNodes(child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			name := strings.ToLower(key.Value)
			if isPriceKey(name) {
				return fmt.Errorf("%w: %q on line %d", ErrPriceTable, key.Value, key.Line)
			}
			if requiresValue(name) && value.ShortTag() == "!!null" {
				return fmt.Errorf("%w: %q requires a value on line %d", ErrUnknownKey, key.Value, key.Line)
			}
			if err := validateProfileNodes(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func isPriceKey(name string) bool {
	return strings.Contains(name, "price") ||
		strings.Contains(name, "rate") ||
		strings.Contains(name, "cost")
}

func requiresValue(name string) bool {
	switch name {
	case "version", "max_balance", "max_hourly", "enforce":
		return true
	}
	return false
}
