package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Each input type is also its tool's JSON Schema. Fields without omitempty
// are required; jsonschema tags become property descriptions (the SDK infers
// the schema with github.com/google/jsonschema-go).
type noInput struct{}

type sessionsInput struct{}

type statusInput struct {
	Session *string `json:"session,omitempty" jsonschema:"Optional session name; omit to resolve the current session"`
}

type usageInput struct{}

type costInput struct {
	Since *string `json:"since,omitempty" jsonschema:"Optional Go duration window for the report, e.g. 24h or 30m"`
}

type reapInput struct {
	OlderThan *string `json:"older_than,omitempty" jsonschema:"Prune local records older than this Go duration; CLI default 2h"`
	DryRun    *bool   `json:"dry_run,omitempty" jsonschema:"Preview only (default true). Set false to actually delete records; then confirm is required"`
	Confirm   bool    `json:"confirm,omitempty" jsonschema:"Required to perform mutations; set true only when dry_run is false"`
}

func (in reapInput) isConfirmed() bool {
	return in.Confirm || in.DryRun == nil || *in.DryRun
}

type newInput struct {
	Session      *string `json:"session,omitempty" jsonschema:"Local name for the new runtime"`
	GPU          *string `json:"gpu,omitempty" jsonschema:"GPU accelerator variant: T4, L4, G4, H100 or A100"`
	TPU          *string `json:"tpu,omitempty" jsonschema:"TPU accelerator variant: V5E1 or V6E1"`
	HighMem      bool    `json:"high_mem,omitempty" jsonschema:"Request a high-RAM machine shape"`
	TTL          *string `json:"ttl,omitempty" jsonschema:"Local session lifetime as a Go duration, e.g. 2h"`
	TTLAction    *string `json:"ttl_action,omitempty" jsonschema:"On expiry: warn (default) or stop"`
	WaitCapacity *string `json:"wait_capacity,omitempty" jsonschema:"Retry capacity refusals for at most this Go duration"`
	Confirm      bool    `json:"confirm,omitempty" jsonschema:"Required: allocating a VM is billable and changes external state"`
}

func (in newInput) isConfirmed() bool { return in.Confirm }

type execInput struct {
	Session *string  `json:"session,omitempty" jsonschema:"Session to attach to; omit to resolve the current session"`
	Code    string   `json:"code,omitempty" jsonschema:"Python source sent to the kernel over stdin, never through argv"`
	File    *string  `json:"file,omitempty" jsonschema:"Optional local .py or .ipynb file; mutually exclusive with code"`
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"Seconds without kernel output before failing; 0 disables"`
	Env     []string `json:"env,omitempty" jsonschema:"Environment variables in KEY=VALUE form set in the kernel; values are sent as-is, do not put secrets here"`
	Secret  []string `json:"secret,omitempty" jsonschema:"Credential NAMES injected from the process environment or keyring; values are never echoed"`
	EnvFile []string `json:"env_file,omitempty" jsonschema:"Paths to mode-0600 KEY=VALUE files loaded into the kernel"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"Required: executing code changes remote state and may incur cost"`
}

func (in execInput) isConfirmed() bool { return in.Confirm }

type runInput struct {
	Script     string   `json:"script" jsonschema:"Local Python script or .ipynb path to execute on a fresh VM"`
	Session    *string  `json:"session,omitempty" jsonschema:"Ephemeral session name"`
	GPU        *string  `json:"gpu,omitempty" jsonschema:"GPU accelerator variant"`
	TPU        *string  `json:"tpu,omitempty" jsonschema:"TPU accelerator variant"`
	HighMem    bool     `json:"high_mem,omitempty" jsonschema:"Request a high-RAM machine shape"`
	Keep       bool     `json:"keep,omitempty" jsonschema:"Keep the VM after the script finishes (the default tears it down)"`
	Timeout    *float64 `json:"timeout,omitempty" jsonschema:"Seconds without kernel output before failing"`
	Env        []string `json:"env,omitempty" jsonschema:"Environment variables in KEY=VALUE form set in the kernel; do not put secrets here"`
	Param      []string `json:"param,omitempty" jsonschema:"Notebook parameters in NAME=VALUE form (repeatable)"`
	ParamsFile *string  `json:"params_file,omitempty" jsonschema:"JSON/YAML notebook params file"`
	Lockfile   *string  `json:"lockfile,omitempty" jsonschema:"Environment lockfile verified before allocation"`
	Bundle     bool     `json:"bundle,omitempty" jsonschema:"Record a provenance manifest for this run"`
	Out        *string  `json:"out,omitempty" jsonschema:"Provenance manifest path when bundle is true"`
	MaxCU      *float64 `json:"max_cu,omitempty" jsonschema:"Minimum compute-unit balance to maintain; 0 disables"`
	Enforce    bool     `json:"enforce,omitempty" jsonschema:"Interrupt and stop the run when max_cu is breached"`
	Secret     []string `json:"secret,omitempty" jsonschema:"Credential NAMES injected from the environment or keyring"`
	EnvFile    []string `json:"env_file,omitempty" jsonschema:"Paths to mode-0600 KEY=VALUE files"`
	Confirm    bool     `json:"confirm,omitempty" jsonschema:"Required: run allocates a billable VM"`
}

func (in runInput) isConfirmed() bool { return in.Confirm }

type stopInput struct {
	Session *string `json:"session,omitempty" jsonschema:"Session to release; omit to resolve the current session"`
	All     bool    `json:"all,omitempty" jsonschema:"Stop every active session (broader effect than session)"`
	Verify  bool    `json:"verify,omitempty" jsonschema:"Verify after stopping that no assignment remains"`
	Confirm bool    `json:"confirm,omitempty" jsonschema:"Required: stopping releases a runtime and destroys its state"`
}

func (in stopInput) isConfirmed() bool { return in.Confirm }

type psInput struct {
	Session *string  `json:"session,omitempty" jsonschema:"Session whose kernel processes are listed"`
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"Seconds without output before failing"`
}

type catInput struct {
	Session *string `json:"session,omitempty" jsonschema:"Session containing the remote file"`
	Path    string  `json:"path" jsonschema:"Remote Linux path, usually under /content"`
}

type tailInput struct {
	Session *string  `json:"session,omitempty" jsonschema:"Session containing the remote file"`
	Path    string   `json:"path" jsonschema:"Remote Linux path to read"`
	Lines   *int     `json:"lines,omitempty" jsonschema:"Number of trailing lines for the initial read; CLI default 10"`
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"Seconds without output before failing"`
}

type grepInput struct {
	Session    *string `json:"session,omitempty" jsonschema:"Session to search"`
	Pattern    string  `json:"pattern" jsonschema:"Search pattern (a literal or regular expression as supported by the CLI)"`
	Path       string  `json:"path" jsonschema:"Remote path or directory to search"`
	IgnoreCase bool    `json:"ignore_case,omitempty" jsonschema:"Case-insensitive matching"`
	Glob       *string `json:"glob,omitempty" jsonschema:"Only search files matching this glob, e.g. *.py"`
	MaxMatches *int    `json:"max_matches,omitempty" jsonschema:"Maximum matching lines returned; CLI default 200"`
}

type duInput struct {
	Session    *string `json:"session,omitempty" jsonschema:"Session to inspect"`
	Path       string  `json:"path" jsonschema:"Remote path whose sizes are reported"`
	MaxEntries *int    `json:"max_entries,omitempty" jsonschema:"Maximum entries reported; CLI default 200"`
}

type findInput struct {
	Session    *string `json:"session,omitempty" jsonschema:"Session to search"`
	Name       string  `json:"name" jsonschema:"File name pattern to locate"`
	MaxResults *int    `json:"max_results,omitempty" jsonschema:"Maximum paths returned; CLI default 200"`
}

type fetchInput struct {
	Session *string  `json:"session,omitempty" jsonschema:"Session receiving the file"`
	URL     string   `json:"url" jsonschema:"HTTPS URL to download on the VM"`
	Dest    *string  `json:"dest,omitempty" jsonschema:"Optional remote destination path"`
	SHA256  *string  `json:"sha256,omitempty" jsonschema:"Expected SHA-256 hex digest of the download"`
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"Download timeout in seconds; CLI default 600"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"Required: fetch writes to the VM and can incur network cost"`
}

func (in fetchInput) isConfirmed() bool { return in.Confirm }

type resumeInput struct {
	Session *string  `json:"session,omitempty" jsonschema:"Session holding the interrupted execution"`
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"Seconds without kernel output before failing"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"Required: resuming re-executes code on the VM"`
}

func (in resumeInput) isConfirmed() bool { return in.Confirm }

type logsInput struct {
	ID string `json:"id" jsonschema:"Detached run ID printed by jobs; this tool does not follow the log"`
}

type waitInput struct {
	Session *string  `json:"session,omitempty" jsonschema:"Session to wait on"`
	For     *string  `json:"for,omitempty" jsonschema:"Wait condition: kernel, idle or ready (default ready)"`
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"Maximum seconds to wait; CLI default 300"`
}

type doctorInput struct {
	Section  *string `json:"section,omitempty" jsonschema:"Only report one section: config, auth, api, update or wire"`
	Fixtures *string `json:"fixtures,omitempty" jsonschema:"Directory of captured wire fixtures to inventory"`
	Timeout  *string `json:"timeout,omitempty" jsonschema:"Hard deadline for live probes as a Go duration"`
}

type compatInput struct{}

type confirmable interface{ isConfirmed() bool }

func requireConfirm(tool string, in confirmable) error {
	if in.isConfirmed() {
		return nil
	}
	return fmt.Errorf("%s is billable/destructive and changes external state; pass confirm: true to proceed", tool)
}

func registerTools(s *mcp.Server, r *Runner) {
	// Read-only tools: no confirm, readOnlyHint=true, no external mutation.
	register(s, r, &mcp.Tool{
		Name:        "colab_sessions",
		Description: "Read-only: list active Colab sessions. Does not allocate, stop or modify anything.",
		Annotations: readOnly("List active sessions"),
	}, nil, func(sessionsInput) invocation { return invocation{command: "sessions"} })

	register(s, r, &mcp.Tool{
		Name:        "colab_status",
		Description: "Read-only: show the status of one local session, or the current session when name is omitted. No state is changed.",
		Annotations: readOnly("Show session status"),
	}, nil, func(in statusInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		return invocation{command: "status", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_usage",
		Description: "Read-only: show the account compute-unit usage rate and balance. No state is changed.",
		Annotations: readOnly("Show compute-unit usage"),
	}, nil, func(usageInput) invocation { return invocation{command: "usage"} })

	register(s, r, &mcp.Tool{
		Name:        "colab_cost",
		Description: "Read-only: report compute-unit cost samples for the requested window. No state is changed.",
		Annotations: readOnly("Report compute-unit cost"),
	}, nil, func(in costInput) invocation {
		f := newFlags()
		strFlag(&f, "since", ptrStr(in.Since))
		return invocation{command: "cost", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_ps",
		Description: "Read-only: snapshot kernel processes of a session. No state is changed.",
		Annotations: readOnly("List kernel processes"),
	}, nil, func(in psInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		floatFlag(&f, "timeout", in.Timeout)
		boolFlag(&f, "json", true)
		return invocation{command: "ps", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_cat",
		Description: "Read-only: print a remote file from a session. Nothing is written on the VM.",
		Annotations: readOnly("Read a remote file"),
	}, nil, func(in catInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		return invocation{command: "cat", flags: f, positional: []string{in.Path}}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_tail",
		Description: "Read-only: read trailing lines of a remote file. This tool never follows the file, so it terminates.",
		Annotations: readOnly("Tail a remote file"),
	}, nil, func(in tailInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		intFlag(&f, "lines", in.Lines)
		floatFlag(&f, "timeout", in.Timeout)
		return invocation{command: "tail", flags: f, positional: []string{in.Path}}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_grep",
		Description: "Read-only: search files on the VM for a pattern and return matching lines. No state is changed.",
		Annotations: readOnly("Search remote files"),
	}, nil, func(in grepInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		boolFlag(&f, "ignore-case", in.IgnoreCase)
		strFlag(&f, "glob", ptrStr(in.Glob))
		intFlag(&f, "max-matches", in.MaxMatches)
		return invocation{command: "grep", flags: f, positional: []string{in.Pattern, in.Path}}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_du",
		Description: "Read-only: report remote disk usage for a path. No state is changed.",
		Annotations: readOnly("Report remote disk usage"),
	}, nil, func(in duInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		intFlag(&f, "max-entries", in.MaxEntries)
		return invocation{command: "du", flags: f, positional: []string{in.Path}}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_find",
		Description: "Read-only: locate files by name on the VM. No state is changed.",
		Annotations: readOnly("Find remote files"),
	}, nil, func(in findInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		intFlag(&f, "max-results", in.MaxResults)
		return invocation{command: "find", flags: f, positional: []string{in.Name}}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_logs",
		Description: "Read-only: read the log of a detached run. This tool does not follow the log, so it terminates.",
		Annotations: readOnly("Read a detached run log"),
	}, nil, func(in logsInput) invocation {
		return invocation{command: "logs", positional: []string{in.ID}}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_wait",
		Description: "Read-only: wait for a session to reach kernel, idle or ready state. Polls the existing session; does not allocate.",
		Annotations: readOnly("Wait for session state"),
	}, nil, func(in waitInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		strFlag(&f, "for", ptrStr(in.For))
		floatFlag(&f, "timeout", in.Timeout)
		return invocation{command: "wait", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_jobs",
		Description: "Read-only: list detached runs with status, exit code and duration. Starts nothing and holds nothing open.",
		Annotations: readOnly("List detached runs"),
	}, nil, func(noInput) invocation { return invocation{command: "jobs"} })

	register(s, r, &mcp.Tool{
		Name:        "colab_doctor",
		Description: "Read-only local diagnostics: report config, auth, API, update and wire state. This tool never applies fixes, never runs live probes and never edits files.",
		Annotations: readOnly("Run local diagnostics"),
	}, nil, func(in doctorInput) invocation {
		f := newFlags()
		strFlag(&f, "section", ptrStr(in.Section))
		strFlag(&f, "fixtures", ptrStr(in.Fixtures))
		strFlag(&f, "timeout", ptrStr(in.Timeout))
		boolFlag(&f, "json", true)
		return invocation{command: "doctor", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_compat",
		Description: "Read-only: compare the local command surface against the embedded snapshot. Uses a local snapshot only; makes no network claim.",
		Annotations: readOnly("Compare CLI surface to snapshot"),
	}, nil, func(compatInput) invocation {
		return invocation{command: "compat", flags: []string{"report", "--json"}}
	})

	// Mutating tools: explicit confirm:true gate, destructiveHint=true.
	register(s, r, &mcp.Tool{
		Name:        "colab_reap",
		Description: "Prune stale local session records. DRY-RUN BY DEFAULT (dry_run=true): previews changes without mutation. Setting dry_run=false deletes records and requires confirm:true.",
		Annotations: mutating("Prune stale session records"),
	}, func(in reapInput) error { return requireConfirm("colab_reap", in) }, func(in reapInput) invocation {
		f := newFlags()
		strFlag(&f, "older-than", ptrStr(in.OlderThan))
		if in.DryRun == nil || *in.DryRun {
			boolFlag(&f, "dry-run", true)
		}
		boolFlag(&f, "json", true)
		return invocation{command: "reap", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_new",
		Description: "BILLABLE: allocate a new Colab runtime (optionally with GPU/TPU or high memory). Requires confirm:true; the new VM changes external state.",
		Annotations: mutating("Allocate a new runtime"),
	}, func(in newInput) error { return requireConfirm("colab_new", in) }, func(in newInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		strFlag(&f, "gpu", ptrStr(in.GPU))
		strFlag(&f, "tpu", ptrStr(in.TPU))
		boolFlag(&f, "high-mem", in.HighMem)
		strFlag(&f, "ttl", ptrStr(in.TTL))
		strFlag(&f, "ttl-action", ptrStr(in.TTLAction))
		strFlag(&f, "wait-capacity", ptrStr(in.WaitCapacity))
		return invocation{command: "new", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_exec",
		Description: "MUTATING: execute Python code in a session. Code is passed on stdin, never in argv or shell. Requires confirm:true because it runs code on a remote VM.",
		Annotations: mutating("Execute code on a VM"),
	}, func(in execInput) error {
		if in.Code != "" && ptrStr(in.File) != "" {
			return errors.New("code and file are mutually exclusive")
		}
		return requireConfirm("colab_exec", in)
	}, func(in execInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		strFlag(&f, "file", ptrStr(in.File))
		floatFlag(&f, "timeout", in.Timeout)
		arrayFlag(&f, "env", in.Env)
		arrayFlag(&f, "secret", in.Secret)
		arrayFlag(&f, "env-file", in.EnvFile)
		boolFlag(&f, "json", true)
		return invocation{command: "exec", flags: f, stdin: in.Code}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_run",
		Description: "BILLABLE: run a local Python script on a fresh VM, then release it unless keep is set. Requires confirm:true; all flags precede the script path so no user string is forwarded as a flag.",
		Annotations: mutating("Run a script on a fresh runtime"),
	}, func(in runInput) error {
		if strings.TrimSpace(in.Script) == "" {
			return errors.New("script is required")
		}
		return requireConfirm("colab_run", in)
	}, func(in runInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		strFlag(&f, "gpu", ptrStr(in.GPU))
		strFlag(&f, "tpu", ptrStr(in.TPU))
		boolFlag(&f, "high-mem", in.HighMem)
		boolFlag(&f, "keep", in.Keep)
		floatFlag(&f, "timeout", in.Timeout)
		arrayFlag(&f, "env", in.Env)
		arrayFlag(&f, "param", in.Param)
		strFlag(&f, "params-file", ptrStr(in.ParamsFile))
		strFlag(&f, "lockfile", ptrStr(in.Lockfile))
		boolFlag(&f, "bundle", in.Bundle)
		strFlag(&f, "out", ptrStr(in.Out))
		floatFlag(&f, "max-cu", in.MaxCU)
		boolFlag(&f, "enforce", in.Enforce)
		arrayFlag(&f, "secret", in.Secret)
		arrayFlag(&f, "env-file", in.EnvFile)
		boolFlag(&f, "json", true)
		return invocation{command: "run", flags: f, positional: []string{in.Script}}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_stop",
		Description: "DESTRUCTIVE: release a runtime and discard its state, or all sessions when all is set. Requires confirm:true; this cannot be undone.",
		Annotations: mutating("Release a runtime"),
	}, func(in stopInput) error { return requireConfirm("colab_stop", in) }, func(in stopInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		boolFlag(&f, "all", in.All)
		boolFlag(&f, "verify", in.Verify)
		return invocation{command: "stop", flags: f}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_fetch",
		Description: "MUTATING: download an HTTPS URL onto the VM, optionally verifying a SHA-256 digest. Requires confirm:true because it writes remote state.",
		Annotations: mutating("Download a file onto the VM"),
	}, func(in fetchInput) error { return requireConfirm("colab_fetch", in) }, func(in fetchInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		strFlag(&f, "sha256", ptrStr(in.SHA256))
		floatFlag(&f, "timeout", in.Timeout)
		pos := []string{in.URL}
		if in.Dest != nil {
			pos = append(pos, *in.Dest)
		}
		return invocation{command: "fetch", flags: f, positional: pos}
	})

	register(s, r, &mcp.Tool{
		Name:        "colab_resume",
		Description: "MUTATING: resume an interrupted execution in a session (re-runs remote code). Requires confirm:true.",
		Annotations: mutating("Resume an interrupted execution"),
	}, func(in resumeInput) error { return requireConfirm("colab_resume", in) }, func(in resumeInput) invocation {
		f := newFlags()
		strFlag(&f, "session", ptrStr(in.Session))
		floatFlag(&f, "timeout", in.Timeout)
		return invocation{command: "resume", flags: f}
	})
}

// register wires a typed handler: input validation and schema inference are
// performed by the official SDK, confirm gating by us, and errors are returned
// as tool errors so the SDK sets isError with a redacted message.
func register[In any](s *mcp.Server, r *Runner, tool *mcp.Tool, gate func(In) error, build func(In) invocation) {
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, ToolOutput, error) {
		if gate != nil {
			if err := gate(in); err != nil {
				return nil, ToolOutput{}, err
			}
		}
		out, err := r.Run(ctx, tool.Name, build(in))
		return nil, out, err
	})
}

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
}

func mutating(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: false, DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true)}
}

func boolPtr(v bool) *bool { return &v }

func ptrStr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func newFlags() []string { return make([]string, 0, 8) }

// Flag values use the --flag=value form so a value beginning with '-' cannot
// be parsed as another flag, and user strings remain a single argv element.
func strFlag(dst *[]string, name, value string) {
	if value != "" {
		*dst = append(*dst, name+"="+value)
	}
}

func boolFlag(dst *[]string, name string, value bool) {
	if value {
		*dst = append(*dst, "--"+name)
	}
}

func floatFlag(dst *[]string, name string, value *float64) {
	if value != nil {
		*dst = append(*dst, fmt.Sprintf("--%s=%g", name, *value))
	}
}

func intFlag(dst *[]string, name string, value *int) {
	if value != nil {
		*dst = append(*dst, fmt.Sprintf("--%s=%d", name, *value))
	}
}

func arrayFlag(dst *[]string, name string, values []string) {
	for _, v := range values {
		*dst = append(*dst, "--"+name+"="+v)
	}
}
