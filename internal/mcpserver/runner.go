package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/20Youssef10/golab/internal/agentout"
	"github.com/20Youssef10/golab/internal/logx"
)

// Limits keep a single tool result bounded without dropping the beginning of
// a diagnostic, which is where the actionable part normally lives.
const (
	maxOutputBytes = 64 << 10
	maxDiagBytes   = 4 << 10
)

// invocation is a pre-validated argv fragment. Values are always separate
// argv elements ("--flag=value" or a positional after "--"); no shell is
// involved and no user string is ever spliced into code executed on a VM.
type invocation struct {
	command    string
	flags      []string
	positional []string
	stdin      string
}

// ToolOutput is the typed, structured result shared by every tool. It never
// contains the raw argv (which could carry a secret name) — only the tool
// name, the exit status and redacted text from the two subprocess channels.
type ToolOutput struct {
	Command     string `json:"command" jsonschema:"the MCP tool name that was executed"`
	ExitCode    int    `json:"exit_code" jsonschema:"exit status of the golab subprocess; zero on success"`
	Output      string `json:"output" jsonschema:"redacted text emitted by golab on stdout"`
	Diagnostics string `json:"diagnostics,omitempty" jsonschema:"redacted [colab] diagnostics emitted on stderr"`
}

// Runner executes the golab binary as a short-lived subprocess. It deliberately
// does not import internal/cli: architecture requires that only cmd/golab does.
type Runner struct {
	bin string
}

// NewRunner returns a runner for bin. An empty bin defaults to "golab" looked
// up on PATH at call time, so listing tools works without golab installed.
func NewRunner(bin string) *Runner {
	if strings.TrimSpace(bin) == "" {
		bin = "golab"
	}
	return &Runner{bin: bin}
}

func (r *Runner) resolve() (string, error) {
	path, err := exec.LookPath(r.bin)
	if err != nil {
		return "", fmt.Errorf("golab executable %q not found: %w", r.bin, err)
	}
	return path, nil
}

// Run invokes the CLI and converts its agentout document into a typed result.
// The subprocess never inherits this process's stdout: in stdio MCP mode that
// stream is the JSON-RPC channel and any stray byte would corrupt the session.
func (r *Runner) Run(ctx context.Context, tool string, inv invocation) (ToolOutput, error) {
	out := ToolOutput{Command: tool}
	bin, err := r.resolve()
	if err != nil {
		return out, err
	}
	if err := validateInvocation(inv); err != nil {
		return out, err
	}

	argv := make([]string, 0, 2+len(inv.flags)+len(inv.positional)+2)
	argv = append(argv, "--output", "json", inv.command)
	argv = append(argv, inv.flags...)
	if len(inv.positional) > 0 {
		argv = append(argv, "--")
		argv = append(argv, inv.positional...)
	}

	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Env = os.Environ() // pass auth/credentials through; never dump them
	if inv.stdin != "" {
		cmd.Stdin = strings.NewReader(inv.stdin)
	}
	cmd.WaitDelay = 2 * time.Second // do not hang on pipes after context kill

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	diagnostics := truncate(logx.Redact(stderr.String()), maxDiagBytes)
	out.Diagnostics = diagnostics
	text, parseErr := decodeEvents(stdout.Bytes())
	if parseErr != nil {
		return out, fmt.Errorf("golab %s: malformed structured output: %w", tool, parseErr)
	}
	out.Output = truncate(text, maxOutputBytes)

	if ctxErr := ctx.Err(); ctxErr != nil {
		return out, fmt.Errorf("golab %s: cancelled: %w", tool, ctxErr)
	}
	if runErr == nil {
		return out, nil
	}

	code := 1
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		code = ee.ExitCode()
	}
	out.ExitCode = code
	msg := strings.TrimSpace(diagnostics)
	if msg == "" {
		msg = strings.TrimSpace(out.Output)
	}
	if msg == "" {
		msg = runErr.Error()
	}
	return out, fmt.Errorf("golab %s exited with status %d: %s", tool, code, truncate(msg, maxDiagBytes))
}

// decodeEvents parses the documented agentout "golab/1" document and joins
// its output/info events. Free-form text gets a defensive redaction pass even
// though agentout already redacts it at the CLI boundary.
func decodeEvents(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", nil
	}
	var doc struct {
		Schema string           `json:"schema"`
		Events []agentout.Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("invalid agentout JSON: %w", err)
	}
	if doc.Schema != "" && doc.Schema != agentout.Schema {
		return "", fmt.Errorf("unexpected schema %q", doc.Schema)
	}
	parts := make([]string, 0, len(doc.Events))
	for _, ev := range doc.Events {
		switch ev.Type {
		case "output", "info":
			parts = append(parts, logx.Redact(ev.Text))
		case "error":
			return strings.Join(parts, "\n"), errors.New(logx.Redact(ev.Text))
		}
	}
	return strings.Join(parts, "\n"), nil
}

func validateInvocation(inv invocation) error {
	tokens := append([]string{inv.command}, inv.flags...)
	tokens = append(tokens, inv.positional...)
	for _, token := range tokens {
		if token == "" {
			return errors.New("empty argv element")
		}
		if strings.ContainsRune(token, 0) {
			return errors.New("NUL byte in argument")
		}
	}
	if strings.ContainsRune(inv.stdin, 0) {
		return errors.New("NUL byte in stdin")
	}
	return nil
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "\n[truncated]"
}
