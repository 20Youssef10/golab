package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// fakeSecretKeyring implements the narrow keyring reader without touching the
// OS keyring (no D-Bus, no `security` CLI).
type fakeSecretKeyring struct {
	values map[string]string
	err    error
}

func (f *fakeSecretKeyring) Get(service, user string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.values[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func secretKeyringContext(kr secretKeyring) context.Context {
	return WithSecretKeyring(context.Background(), kr)
}

// Ports upstream issue #157's local-secrets use case: process env is the
// first tier, the existing KeyringBackend the second.
func TestResolveSecretValueTiers(t *testing.T) {
	t.Run("env wins over keyring", func(t *testing.T) {
		t.Setenv("GOLAB_TEST_SECRET_A", "from-environment")
		kr := &fakeSecretKeyring{values: map[string]string{
			"google-colab-cli/GOLAB_TEST_SECRET_A": "from-keyring",
		}}
		got, err := resolveSecretValue(secretKeyringContext(kr), "GOLAB_TEST_SECRET_A")
		if err != nil {
			t.Fatal(err)
		}
		if got.Reveal() != "from-environment" || got.String() != "REDACTED" {
			t.Fatalf("secret = %q / %q", got.Reveal(), got.String())
		}
	})
	t.Run("keyring fallback", func(t *testing.T) {
		os.Unsetenv("GOLAB_TEST_SECRET_B")
		kr := &fakeSecretKeyring{values: map[string]string{
			"google-colab-cli/GOLAB_TEST_SECRET_B": "from-keyring-fallback",
		}}
		got, err := resolveSecretValue(secretKeyringContext(kr), "GOLAB_TEST_SECRET_B")
		if err != nil {
			t.Fatal(err)
		}
		if got.Reveal() != "from-keyring-fallback" {
			t.Fatalf("secret = %q", got.Reveal())
		}
	})
	t.Run("missing is a usage error without the value", func(t *testing.T) {
		os.Unsetenv("GOLAB_TEST_SECRET_MISSING")
		kr := &fakeSecretKeyring{err: keyring.ErrNotFound}
		_, err := resolveSecretValue(secretKeyringContext(kr), "GOLAB_TEST_SECRET_MISSING")
		if err == nil {
			t.Fatal("expected error")
		}
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != 2 {
			t.Fatalf("error = %v (%T), want exit 2", err, err)
		}
		if strings.Contains(err.Error(), "GOLAB_TEST_SECRET_MISSING=") {
			t.Fatalf("error echoed a value: %q", err)
		}
	})
}

// env-file must be owner-only on Unix, like every other credential file.
func TestParseEnvFileModeAndKeys(t *testing.T) {
	t.Run("0600 parses comments and values with equals", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vars.env")
		body := "# comment\n\nPLAIN=one\nAPI_TOKEN=supersecret-value\nEMPTY=\nCONNECTION=a=b=c\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		vars, secrets, err := parseEnvFile(path)
		if err != nil {
			t.Fatal(err)
		}
		wantVars := []envVar{
			{key: "PLAIN", value: "one"},
			{key: "API_TOKEN", value: "supersecret-value"},
			{key: "EMPTY", value: ""},
			{key: "CONNECTION", value: "a=b=c"},
		}
		if len(vars) != len(wantVars) {
			t.Fatalf("vars = %+v", vars)
		}
		for i, want := range wantVars {
			if vars[i] != want {
				t.Fatalf("vars[%d] = %+v, want %+v", i, vars[i], want)
			}
		}
		if len(secrets) != 1 || secrets[0].name != "API_TOKEN" ||
			secrets[0].value.Reveal() != "supersecret-value" {
			t.Fatalf("secrets = %+v", secrets)
		}
	})
	t.Run("0644 refused", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("Unix mode bits")
		}
		path := filepath.Join(t.TempDir(), "loose.env")
		if err := os.WriteFile(path, []byte("PLAIN=one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := parseEnvFile(path)
		if !errors.Is(err, errEnvFileMode) {
			t.Fatalf("error = %v, want errEnvFileMode", err)
		}
	})
	t.Run("malformed key is a usage error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.env")
		if err := os.WriteFile(path, []byte("1BAD=supersecret-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := parseEnvFile(path)
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != 2 {
			t.Fatalf("error = %v (%T), want exit 2", err, err)
		}
		if !strings.Contains(err.Error(), "1BAD") || strings.Contains(err.Error(), "supersecret-value") {
			t.Fatalf("error = %q", err)
		}
	})
}

func TestInjectedPreludeUsesSafeRepr(t *testing.T) {
	vars := []envVar{{key: "A", value: `a=b`}}
	secrets := []injectedSecret{{name: "Q", value: logx.NewSecret("it's \"quoted\"\nnext")}}
	code := injectedPrelude(vars, secrets)
	for _, want := range []string{
		"import os\n",
		`os.environ["A"] = "a=b"`,
		`os.environ["Q"] = "it's \"quoted\"\nnext"`,
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("prelude missing %q: %q", want, code)
		}
	}
	if strings.Contains(code, secrets[0].value.Reveal()) {
		t.Fatalf("raw secret in prelude: %q", code)
	}
}

func TestExecRedactorScrubsAllSurfaces(t *testing.T) {
	const secret = "planted-redactor-secret-9999"
	red := newExecRedactor(logx.NewSecret(secret))
	for _, in := range []string{
		"plain " + secret,
		`{"password":"` + secret + `"}`,
	} {
		out := red.Redact(in)
		if strings.Contains(out, secret) {
			t.Fatalf("leak in Redact(%q) = %q", in, out)
		}
	}
	output := red.Output(fakeOutputWith(secret))
	if strings.Contains(output.Text, secret) || strings.Contains(output.EValue, secret) ||
		strings.Contains(strings.Join(output.Traceback, ""), secret) {
		t.Fatalf("leak in redacted output: %+v", output)
	}
	data := red.Any(map[string]any{"text/plain": secret})
	if strings.Contains(data.(map[string]any)["text/plain"].(string), secret) {
		t.Fatalf("leak in redacted data: %v", data)
	}
}

// fakeOutputWith builds an output whose every free-form field echoes secret.
func fakeOutputWith(secret string) runtime.Output {
	return runtime.Output{
		OutputType: "error", EName: "ValueError", EValue: secret,
		Text: secret, Traceback: []string{"line " + secret},
		Data: map[string]any{"text/plain": secret},
	}
}

// The planted secret must exist in the kernel payload and nowhere else.
// Ports upstream issue #157's acceptance criterion.
func TestExecSecretNeverLeaks(t *testing.T) {
	const secret = "planted-exec-secret-4242"
	t.Setenv("GOLAB_TEST_EXEC_SECRET", secret)
	for _, jsonMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonMode], func(t *testing.T) {
			h := newHarness(t)
			h.putSession(t, "s1")
			h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
			h.kernel.Script(
				fakekernel.Stream("stdout", "echo:"+secret+"\n"),
				fakekernel.Stream("stderr", "warn:"+secret+"\n"),
				fakekernel.DisplayData(map[string]any{"text/plain": secret}, nil, ""),
				fakekernel.Error("ValueError", secret, []string{"ValueError: " + secret + "\n"}),
				fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
			)
			args := []string{"exec", "-s", "s1", "--secret", "GOLAB_TEST_EXEC_SECRET"}
			if jsonMode {
				args = append(args, "--json")
			}
			if code := h.run(t, "print(1)\n", args...); code != 1 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}
			execs := h.kernel.Executions()
			if len(execs) < 2 || !strings.Contains(execs[1].Code, pyRepr(secret)) {
				t.Fatalf("secret not injected into kernel payload: %+v", execs)
			}
			assertNoSecret(t, secret, h.stdout.String(), h.stderr.String())
			// History, session state and settings never persist the value.
			for _, path := range []string{
				filepath.Join(h.dir, "s1.jsonl"),
				filepath.Join(h.dir, "sessions.json"),
				filepath.Join(h.dir, "settings.json"),
			} {
				b, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				assertNoSecret(t, secret, string(b))
			}
			events, err := history.NewAt(h.dir, "s1")
			if err == nil {
				all, _ := events.Read()
				if len(all) == 0 {
					t.Fatal("no history event recorded")
				}
			}
		})
	}
}

// A keyring-provided secret follows the same no-leak path as an env secret.
func TestExecSecretFromKeyringNeverLeaks(t *testing.T) {
	const secret = "planted-keyring-secret-7777"
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Stream("stdout", secret+"\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	kr := &fakeSecretKeyring{values: map[string]string{
		"google-colab-cli/GOLAB_TEST_KR_SECRET": secret,
	}}
	ctx := WithSecretKeyring(WithDeps(context.Background(), h.deps), kr)
	if code := execute(ctx, []string{"exec", "-s", "s1", "--secret", "GOLAB_TEST_KR_SECRET"},
		strings.NewReader("print(1)\n"), &h.stdout, &h.stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) < 2 || !strings.Contains(execs[1].Code, pyRepr(secret)) {
		t.Fatalf("secret not injected: %+v", execs)
	}
	assertNoSecret(t, secret, h.stdout.String(), h.stderr.String())
}

func TestExecEnvFileValidationBeforeAllocation(t *testing.T) {
	t.Run("0644 refused", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("Unix mode bits")
		}
		h := newHarness(t)
		path := filepath.Join(t.TempDir(), "loose.env")
		if err := os.WriteFile(path, []byte("PLAIN=one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := h.run(t, "print(1)\n", "exec", "-s", "s1", "--env-file", path); code != 2 ||
			!strings.Contains(h.stderr.String(), "0600") {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if len(h.ctrl.Requests()) != 0 || len(h.kernel.Executions()) != 0 {
			t.Fatal("validated too late: kernel/control plane touched")
		}
	})
	t.Run("malformed key exits 2", func(t *testing.T) {
		h := newHarness(t)
		path := filepath.Join(t.TempDir(), "bad.env")
		if err := os.WriteFile(path, []byte("A-B=secret-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := h.run(t, "print(1)\n", "exec", "-s", "s1", "--env-file", path); code != 2 ||
			!strings.Contains(h.stderr.String(), "Invalid --env-file key") {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if len(h.ctrl.Requests()) != 0 {
			t.Fatal("validated too late: control plane touched")
		}
	})
	t.Run("0600 file injects prelude", func(t *testing.T) {
		h := newHarness(t)
		h.putSession(t, "s1")
		h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
		h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
		path := filepath.Join(t.TempDir(), "vars.env")
		if err := os.WriteFile(path, []byte("A=one\nB=a=b\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := h.run(t, "print(1)\n", "exec", "-s", "s1", "--env-file", path); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		execs := h.kernel.Executions()
		payload := execs[1].Code
		for _, want := range []string{`os.environ["A"] = "one"`, `os.environ["B"] = "a=b"`} {
			if !strings.Contains(payload, want) {
				t.Fatalf("payload missing %q: %q", want, payload)
			}
		}
	})
}

// A quoted/newline secret is escaped by pyRepr in the prelude; both the raw
// value and its escaped literal must be absent from history.
func TestExecTrickySecretRedactedInHistory(t *testing.T) {
	const secret = "it's a \"tricky\"\nsecret-1234"
	t.Setenv("GOLAB_TEST_TRICKY_SECRET", secret)
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	if code := h.run(t, "print(1)\n", "exec", "-s", "s1", "--secret", "GOLAB_TEST_TRICKY_SECRET"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) < 2 || !strings.Contains(execs[1].Code, pyRepr(secret)) {
		t.Fatalf("secret not injected: %+v", execs)
	}
	b, err := os.ReadFile(filepath.Join(h.dir, "s1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, secret, string(b), h.stdout.String(), h.stderr.String())
	assertNoSecret(t, pyRepr(secret), string(b), h.stdout.String(), h.stderr.String())
	assertNoSecret(t, strings.Trim(pyRepr(secret), `"`), string(b))
}

func assertNoSecret(t *testing.T, secret string, surfaces ...string) {
	t.Helper()
	for i, s := range surfaces {
		if strings.Contains(s, secret) {
			t.Fatalf("secret leaked on surface %d: %q", i, s)
		}
	}
}
