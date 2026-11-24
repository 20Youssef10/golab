package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/runtime"
)

// secretKeyring is the narrow slice of auth.KeyringBackend this bridge needs.
// A fake can be injected with WithSecretKeyring so tests never touch the real
// OS keyring (D-Bus, macOS `security`, Windows Credential Manager).
type secretKeyring interface {
	Get(service, user string) (string, error)
}

type secretKeyringContextKey struct{}

// WithSecretKeyring injects a keyring reader for the current command context.
func WithSecretKeyring(ctx context.Context, kr secretKeyring) context.Context {
	return context.WithValue(ctx, secretKeyringContextKey{}, kr)
}

func secretKeyringFromContext(ctx context.Context) secretKeyring {
	if kr, ok := ctx.Value(secretKeyringContextKey{}).(secretKeyring); ok && kr != nil {
		return kr
	}
	return osSecretKeyring{}
}

// osSecretKeyring is the production adapter. The Get call is bounded so a
// locked keyring cannot stall `exec` before it even reaches the kernel.
type osSecretKeyring struct{}

func (osSecretKeyring) Get(service, user string) (string, error) {
	type answer struct {
		value string
		err   error
	}
	ch := make(chan answer, 1)
	go func() {
		v, err := keyring.Get(service, user)
		ch <- answer{value: v, err: err}
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case a := <-ch:
		return a.value, a.err
	case <-timer.C:
		return "", errors.New("OS keyring lookup timed out")
	}
}

// resolveSecretValue resolves NAME from the process environment first, then
// from the existing keyring backend under the upstream service name.
func resolveSecretValue(ctx context.Context, name string) (logx.Secret, error) {
	if !envKeyRE.MatchString(name) {
		return logx.Secret{}, usagef("[colab] Invalid --secret name %q. Expected a valid environment variable name.", name)
	}
	if v, ok := os.LookupEnv(name); ok {
		return logx.NewSecret(v), nil
	}
	v, err := secretKeyringFromContext(ctx).Get(auth.KeyringService, name)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return logx.Secret{}, usagef("[colab] Secret %q was not found in the environment or OS keyring.", name)
		}
		return logx.Secret{}, usagef("[colab] Could not read secret %q from OS keyring: %s", name, logx.Redact(err.Error()))
	}
	return logx.NewSecret(v), nil
}

// injectedSecret is a --secret entry (or a sensitive-looking env-file entry)
// whose value is always held as a typed secret.
type injectedSecret struct {
	name  string
	value logx.Secret
}

var errEnvFileMode = errors.New("--env-file must be mode 0600 (owner read/write only)")

var sensitiveKeyNameRE = regexp.MustCompile(`(?i)(secret|token|password|passwd|credential|api[_-]?key|private[_-]?key)`)

// isSensitiveEnvKey tags env-file values that must be redacted on every output
// surface. It never matches generic keys containing "key" (e.g. MONKEY).
func isSensitiveEnvKey(key string) bool {
	return sensitiveKeyNameRE.MatchString(key)
}

// parseEnvFile reads a 0600 KEY=VALUE file. Comments and blank lines are
// ignored, values may contain '=', and only the key appears in error messages.
func parseEnvFile(path string) ([]envVar, []injectedSecret, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, exitf(2, "[colab] Cannot read --env-file %q: %s", path, logx.Redact(err.Error()))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, exitf(2, "[colab] Cannot stat --env-file %q: %s", path, logx.Redact(err.Error()))
	}
	if !info.Mode().IsRegular() {
		return nil, nil, usagef("[colab] --env-file %q is not a regular file.", path)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, nil, exitCausef(2, errEnvFileMode, "[colab] %v: %s", errEnvFileMode, path)
	}
	var vars []envVar
	var secrets []injectedSecret
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSuffix(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		i := strings.IndexByte(text, '=')
		if i < 0 {
			return nil, nil, usagef("[colab] Invalid --env-file line %d: expected KEY=VALUE.", line)
		}
		key, value := text[:i], text[i+1:]
		if !envKeyRE.MatchString(key) {
			return nil, nil, usagef("[colab] Invalid --env-file key %q at line %d. Expected a valid environment variable name.", key, line)
		}
		vars = append(vars, envVar{key: key, value: value})
		if isSensitiveEnvKey(key) {
			secrets = append(secrets, injectedSecret{name: key, value: logx.NewSecret(value)})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, exitf(2, "[colab] Cannot read --env-file %q: %s", path, logx.Redact(err.Error()))
	}
	return vars, secrets, nil
}

// injectedPrelude builds Python source that sets every injected value with
// pyRepr quoting. USER TEXT IS NEVER SPLICED: values are emitted as JSON-
// compatible Python string literals. Entries already present in vars are not
// emitted twice (sensitive env-file values appear in both slices).
func injectedPrelude(vars []envVar, secrets []injectedSecret) string {
	if len(vars) == 0 && len(secrets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("import os\n")
	emitted := make(map[string]bool, len(vars))
	for _, v := range vars {
		fmt.Fprintf(&b, "os.environ[%s] = %s\n", pyRepr(v.key), pyRepr(v.value))
		emitted[v.key] = true
	}
	for _, s := range secrets {
		if emitted[s.name] {
			continue
		}
		fmt.Fprintf(&b, "os.environ[%s] = %s\n", pyRepr(s.name), pyRepr(s.value.Reveal()))
	}
	return b.String()
}

// execRedactor scrubs values injected into the kernel (--secret, and
// sensitive-looking --env-file entries) from every user-visible surface:
// stdout records, stderr diagnostics, history events and notebook outputs.
// It carries no package-level state: each exec builds its own instance.
type execRedactor struct {
	values []logx.Secret
}

func newExecRedactor(values ...logx.Secret) *execRedactor {
	return &execRedactor{values: values}
}

// Redact replaces every injected secret value with REDACTED, then applies the
// shared defensive pass for token-shaped free-form text.
func (r *execRedactor) Redact(s string) string {
	if r == nil {
		return logx.Redact(s)
	}
	for _, v := range r.values {
		raw := v.Reveal()
		if raw == "" {
			continue
		}
		s = strings.ReplaceAll(s, raw, "REDACTED")
		// The prelude contains the pyRepr-escaped literal, not the raw value:
		// quotes, backslashes and newlines must be scrubbed there too, or a
		// history event would still carry the credential.
		literal := pyRepr(raw)
		s = strings.ReplaceAll(s, literal, "REDACTED")
		inner := strings.Trim(literal, `"`)
		if inner != raw {
			s = strings.ReplaceAll(s, inner, "REDACTED")
		}
	}
	return logx.Redact(s)
}

// Any recursively redacts a decoded JSON value (kernel display-data bundles).
func (r *execRedactor) Any(v any) any {
	switch x := v.(type) {
	case string:
		return r.Redact(x)
	case []any:
		for i := range x {
			x[i] = r.Any(x[i])
		}
		return x
	case map[string]any:
		for k, value := range x {
			x[k] = r.Any(value)
		}
		return x
	default:
		return v
	}
}

// Output returns a redacted copy of an output without mutating the caller's
// value (the original is still needed for exit-code classification).
func (r *execRedactor) Output(o runtime.Output) runtime.Output {
	safe := o
	safe.Text = r.Redact(o.Text)
	safe.EName = r.Redact(o.EName)
	safe.EValue = r.Redact(o.EValue)
	if o.Data != nil {
		safe.Data = make(map[string]any, len(o.Data))
		for mime, value := range o.Data {
			safe.Data[mime] = r.Any(value)
		}
	}
	safe.Metadata = nil
	if o.Metadata != nil {
		safe.Metadata = make(map[string]any, len(o.Metadata))
		for k, value := range o.Metadata {
			safe.Metadata[k] = r.Any(value)
		}
	}
	if o.Traceback != nil {
		safe.Traceback = make([]string, len(o.Traceback))
		for i, line := range o.Traceback {
			safe.Traceback[i] = r.Redact(line)
		}
	}
	return safe
}

// Outputs redacts a slice of outputs.
func (r *execRedactor) Outputs(outs []runtime.Output) []runtime.Output {
	safe := make([]runtime.Output, len(outs))
	for i, o := range outs {
		safe[i] = r.Output(o)
	}
	return safe
}
