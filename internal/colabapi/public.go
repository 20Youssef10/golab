package colabapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Public API long-running operation models (client.py::Status / Operation).
type publicStatus struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Details []map[string]any `json:"details,omitempty"`
}

type publicOperation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Metadata map[string]any  `json:"metadata,omitempty"`
	Error    *publicStatus   `json:"error,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type publicRuntimeSpec struct {
	Variant     string `json:"variant"`
	Accelerator string `json:"accelerator"`
	Shape       string `json:"shape"`
}

// maxPublicAssignPolls bounds the LRO wait loop. Each :wait call may block for
// 30 s server-side, so this is at most ~5 minutes of wall time; the caller's
// context and the client's explicit per-request timeout still apply.
const maxPublicAssignPolls = 10

func publicVariant(v Variant) string {
	switch v {
	case VariantGPU:
		return "VARIANT_GPU"
	case VariantTPU:
		return "VARIANT_TPU"
	default:
		return "VARIANT_CPU"
	}
}

func publicShape(s Shape) string {
	if s == ShapeHighRAM {
		return "SHAPE_HIGHMEM"
	}
	return "SHAPE_STANDARD"
}

// runtimeNamePath normalises a runtime name or bare id into the Public API
// resource path. Accepts "runtimes/r-…", "r-…" and "/v1beta/runtimes/r-…".
func runtimeNamePath(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	name = strings.TrimPrefix(name, "v1beta/")
	if strings.HasPrefix(name, "runtimes/") {
		return "/v1beta/" + name
	}
	return "/v1beta/runtimes/" + name
}

// assignPublic implements docs/public_api_migration_plan.md §5:
// POST /v1beta/runtimes?runtimeId=…&requestId=… with {"runtimeSpec": …},
// followed by GET /v1/{op}:wait?timeout=30s until the Operation is done.
func (c *client) assignPublic(ctx context.Context, req AssignRequest) (*AssignmentResponse, error) {
	runtimeID := req.NotebookHash.RuntimeID()
	body, err := json.Marshal(struct {
		RuntimeSpec publicRuntimeSpec `json:"runtimeSpec"`
	}{RuntimeSpec: publicRuntimeSpec{
		Variant:     publicVariant(req.Variant),
		Accelerator: string(req.Accelerator),
		Shape:       publicShape(req.Shape),
	}})
	if err != nil {
		return nil, err
	}
	var op publicOperation
	for attempt := 0; ; attempt++ {
		u := c.publicURL("/v1beta/runtimes", url.Values{
			"runtimeId": {runtimeID},
			"requestId": {uuid.NewString()},
		})
		err := c.doRequest(ctx, http.MethodPost, u, requestOptions{
			contentType: "application/json",
			body:        bytes.NewReader(body),
			parse:       func(b []byte) error { return json.Unmarshal(b, &op) },
		})
		if err == nil {
			break
		}
		if !isPublicTransient(err) {
			return nil, mapAssignError(err, req)
		}
		if attempt >= c.retries {
			return nil, err
		}
		if err := c.sleep(ctx, backoffDelay(c.backoff, attempt)); err != nil {
			return nil, err
		}
		// An uncertain POST may have created the runtime. The id is derived
		// from the notebook hash, so a GET reconciles before we POST again.
		if rt, getErr := c.getRuntimePublic(ctx, runtimeID); getErr == nil && rt.Name != "" {
			out := mapPublicRuntime(rt, time.Now())
			return &AssignmentResponse{
				Accelerator: out.Accelerator, Endpoint: out.Endpoint,
				RuntimeName: out.RuntimeName, RuntimeProxyInfo: out.RuntimeProxyInfo,
				Variant: out.Variant, Reattached: true,
			}, nil
		}
	}
	return c.waitPublicOperation(ctx, req, &op)
}

func (c *client) waitPublicOperation(ctx context.Context, req AssignRequest, op *publicOperation) (*AssignmentResponse, error) {
	transient := 0
	for polls := 0; polls <= maxPublicAssignPolls; polls++ {
		if op.Done {
			if op.Error != nil {
				return c.mapOperationError(ctx, req, op.Error)
			}
			var rt publicRuntime
			if len(op.Response) > 0 {
				if err := json.Unmarshal(op.Response, &rt); err != nil {
					return nil, fmt.Errorf("%w: decode operation response: %w", ErrUnexpectedResponse, err)
				}
			}
			if rt.Name == "" {
				return nil, fmt.Errorf("%w: operation response has no runtime", ErrUnexpectedResponse)
			}
			out := mapPublicRuntime(rt, time.Now())
			return &AssignmentResponse{
				Accelerator:      out.Accelerator,
				Endpoint:         out.Endpoint,
				RuntimeName:      out.RuntimeName,
				RuntimeProxyInfo: out.RuntimeProxyInfo,
				Variant:          out.Variant,
			}, nil
		}
		if op.Name == "" {
			return nil, fmt.Errorf("%w: operation has no name", ErrUnexpectedResponse)
		}
		name := strings.TrimPrefix(op.Name, "/")
		u := c.publicURL("/v1/"+name+":wait", url.Values{"timeout": {"30s"}})
		var next publicOperation
		err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
			parse: func(b []byte) error { return json.Unmarshal(b, &next) },
		})
		if err != nil {
			if !isPublicTransient(err) {
				return nil, mapAssignError(err, req)
			}
			if transient >= c.retries {
				return nil, err
			}
			if err := c.sleep(ctx, backoffDelay(c.backoff, transient)); err != nil {
				return nil, err
			}
			transient++
			continue
		}
		transient = 0
		op = &next
	}
	return nil, fmt.Errorf("%w: public assign operation %q did not complete after %d polls",
		ErrUnexpectedResponse, op.Name, maxPublicAssignPolls)
}

func isPublicTransient(err error) bool {
	if se := statusOf(err); se != nil {
		return retryableStatus(se.Code)
	}
	return isTimeout(err)
}

// mapOperationError maps the LRO error codes from the migration plan:
// 9 → too many assignments, 6 → already-exists recovery, 3/7 → 400/403.
func (c *client) mapOperationError(ctx context.Context, req AssignRequest, st *publicStatus) (*AssignmentResponse, error) {
	msg := st.Message
	switch st.Code {
	case 9: // FAILED_PRECONDITION
		return nil, fmt.Errorf("%w: %s", ErrTooManyAssignments, msg)
	case 6: // ALREADY_EXISTS
		rt, err := c.getRuntimePublic(ctx, req.NotebookHash.RuntimeID())
		if err != nil {
			return nil, err
		}
		out := mapPublicRuntime(rt, time.Now())
		return &AssignmentResponse{
			Accelerator:      out.Accelerator,
			Endpoint:         out.Endpoint,
			RuntimeName:      out.RuntimeName,
			RuntimeProxyInfo: out.RuntimeProxyInfo,
			Variant:          out.Variant,
			Reattached:       true,
		}, nil
	case 3: // INVALID_ARGUMENT → HTTP 400 (entitlement handlers expect this)
		return nil, mapAssignError(&StatusError{
			Code: http.StatusBadRequest, Body: msg,
			Method: http.MethodPost, Path: "/v1beta/runtimes",
		}, req)
	case 7: // PERMISSION_DENIED → HTTP 403
		return nil, &StatusError{
			Code: http.StatusForbidden, Body: msg,
			Method: http.MethodPost, Path: "/v1beta/runtimes",
		}
	default:
		return nil, fmt.Errorf("%w: public assign operation failed (code %d): %s",
			ErrUnexpectedResponse, st.Code, msg)
	}
}

func (c *client) getRuntimePublic(ctx context.Context, runtimeID string) (publicRuntime, error) {
	u := c.publicURL(runtimeNamePath(runtimeID), nil)
	var rt publicRuntime
	err := c.doRequest(ctx, http.MethodGet, u, requestOptions{
		parse: func(b []byte) error { return json.Unmarshal(b, &rt) },
	})
	return rt, err
}

// unassignPublic implements DELETE /v1beta/{name}?allowMissing=true. The
// Public API identifies a runtime by its resource name, not the tunnel
// endpoint, so we resolve the name first when the caller did not supply it.
func (c *client) unassignPublic(ctx context.Context, endpoint, runtimeName string) error {
	name := runtimeName
	if name == "" {
		if strings.HasPrefix(endpoint, "runtimes/") || strings.HasPrefix(endpoint, "r-") {
			name = endpoint
		} else {
			listed, err := c.listPublic(ctx)
			if err != nil {
				return err
			}
			for _, a := range listed {
				if endpoint != "" && a.Endpoint == endpoint {
					name = a.RuntimeName
					break
				}
			}
		}
	}
	if name == "" {
		// Not found: allowMissing=true means the runtime is already gone.
		return nil
	}
	u := c.publicURL(runtimeNamePath(name), url.Values{"allowMissing": {"true"}})
	return c.doRequest(ctx, http.MethodDelete, u, requestOptions{})
}
