package colabapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/20Youssef10/golab/internal/logx"
)

// Variant is the user-facing runtime variant sent on /tun/m/assign.
type Variant string

const (
	VariantDefault Variant = "DEFAULT"
	VariantGPU     Variant = "GPU"
	VariantTPU     Variant = "TPU"
)

// Accelerator is the hardware requested on /tun/m/assign.
type Accelerator string

const (
	AccelNone Accelerator = "NONE"
	AccelG4   Accelerator = "G4"
	AccelT4   Accelerator = "T4"
	AccelL4   Accelerator = "L4"
	AccelA100 Accelerator = "A100"
	AccelH100 Accelerator = "H100"
	AccelV5E1 Accelerator = "V5E1"
	AccelV6E1 Accelerator = "V6E1"
)

// AssignmentVariant is the integer variant returned by the backend.
type AssignmentVariant int

const (
	AssignmentDefault AssignmentVariant = 0
	AssignmentGPU     AssignmentVariant = 1
	AssignmentTPU     AssignmentVariant = 2
)

// String matches upstream's AssignmentVariant.name (used in `sessions`).
func (v AssignmentVariant) String() string {
	switch v {
	case AssignmentDefault:
		return "DEFAULT"
	case AssignmentGPU:
		return "GPU"
	case AssignmentTPU:
		return "TPU"
	default:
		return fmt.Sprintf("AssignmentVariant(%d)", int(v))
	}
}

// Shape is the machine shape. The wire request uses "hm" for HIGH_RAM.
type Shape int

const (
	ShapeStandard Shape = 0
	ShapeHighRAM  Shape = 1
)

// String matches upstream's Shape.name.
func (s Shape) String() string {
	if s == ShapeHighRAM {
		return "HIGH_RAM"
	}
	return "STANDARD"
}

// NotebookHash is Colab's 44-character notebook identifier: a UUID with
// hyphens replaced by underscores followed by eight dots (docs/02 §2.1).
type NotebookHash string

const NotebookHashLength = 44

var nbhPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}_[0-9a-fA-F]{4}_[0-9a-fA-F]{4}_[0-9a-fA-F]{4}_[0-9a-fA-F]{12}\.{8}$`)

// NewNotebookHash encodes id exactly as upstream's uuid_to_web_safe_base64.
func NewNotebookHash(id uuid.UUID) NotebookHash {
	core := strings.ReplaceAll(id.String(), "-", "_")
	return NotebookHash(core + strings.Repeat(".", NotebookHashLength-len(core)))
}

// RandomNotebookHash generates a fresh hash for each `new`.
func RandomNotebookHash() (NotebookHash, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return NewNotebookHash(id), nil
}

func (h NotebookHash) String() string { return string(h) }

// Valid reports whether h has the exact upstream encoding.
func (h NotebookHash) Valid() bool { return nbhPattern.MatchString(string(h)) }

// RuntimeProxyInfo is the data-plane credential returned by the control plane.
// Token is a Secret: it never appears in logs or error messages.
type RuntimeProxyInfo struct {
	Token                 logx.Secret `json:"token"`
	TokenExpiresInSeconds int         `json:"tokenExpiresInSeconds"`
	URL                   string      `json:"url"`
}

// ExpiresAt computes the absolute expiry. Call it at receipt, as upstream does.
func (p RuntimeProxyInfo) ExpiresAt(now time.Time) time.Time {
	return now.Add(time.Duration(p.TokenExpiresInSeconds) * time.Second)
}

// Assignment is the "already assigned" shape returned by GET /assign and
// upstream's Assignment model (idempotent reattach).
type Assignment struct {
	Endpoint         string           `json:"endpoint"`
	RuntimeProxyInfo RuntimeProxyInfo `json:"runtimeProxyInfo"`
}

// AssignmentResponse is the POST /assign result. Reattached is true when the
// GET phase discovered an existing assignment and no POST was needed.
type AssignmentResponse struct {
	Accelerator      Accelerator       `json:"accelerator"`
	Endpoint         string            `json:"endpoint"`
	RuntimeProxyInfo RuntimeProxyInfo  `json:"runtimeProxyInfo"`
	Variant          AssignmentVariant `json:"variant"`
	Reattached       bool              `json:"-"`
}

// ListedAssignment is one entry of GET /assignments, from either API path.
type ListedAssignment struct {
	Accelerator      Accelerator       `json:"accelerator"`
	Endpoint         string            `json:"endpoint"`
	Variant          AssignmentVariant `json:"variant"`
	MachineShape     Shape             `json:"machineShape"`
	RuntimeProxyInfo RuntimeProxyInfo  `json:"runtimeProxyInfo"`
}

// Consumption mirrors upstream's ConsumptionUserInfo (colab usage).
type Consumption struct {
	PaidComputeUnitsBalance float64
	ConsumptionRateHourly   float64
	AssignmentsCount        int
}

// String reproduces upstream's format_consumption_status exactly.
func (c Consumption) String() string {
	return fmt.Sprintf(
		"Current balance: %.2f compute units\nUsage rate: %.2f/hr\nActive assignments: %.0f",
		c.PaidComputeUnitsBalance, c.ConsumptionRateHourly, float64(c.AssignmentsCount))
}

// CredProp is the result of C7/C8 (credentials propagation). Confirm performs
// the final dryrun=false POST after the user has approved the redirect URI;
// it is useful for the Drive-mount flow in internal/runtime.
type CredProp struct {
	Success                 bool
	UnauthorizedRedirectURI string
	Token                   logx.Secret
	Confirm                 func(context.Context) (*CredProp, error)
}

// AssignRequest holds the validated CLI intent for a new runtime.
type AssignRequest struct {
	NotebookHash NotebookHash
	Variant      Variant
	Accelerator  Accelerator
	Shape        Shape
}

var highMemOnly = map[Accelerator]bool{
	AccelL4: true, AccelV5E1: true, AccelV6E1: true,
}

// ResolveAssignShape maps CLI intent to the optional shape=hm query param.
// It mirrors upstream's resolve_assign_shape: high memory is ignored for the
// accelerators that only offer a single (high-memory) shape.
func ResolveAssignShape(accelerator Accelerator, highMem bool) *Shape {
	if !highMem || highMemOnly[accelerator] {
		return nil
	}
	shape := ShapeHighRAM
	return &shape
}

func variantToAssignment(v Variant) AssignmentVariant {
	switch v {
	case VariantGPU:
		return AssignmentGPU
	case VariantTPU:
		return AssignmentTPU
	default:
		return AssignmentDefault
	}
}
