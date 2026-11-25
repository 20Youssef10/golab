package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// fakeSecretKeyring implements the narrow keyring reader without touching the
// OS keyring (no D-Bus, no `security` CLI). gets counts reads so a test can
// assert that a reserved name is refused before the keyring is consulted.
type fakeSecretKeyring struct {
	values map[string]string
	err    error
	gets   int
}

func (f *fakeSecretKeyring) Get(service, user string) (string, error) {
	f.gets++
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

// Local hardening (no upstream Python counterpart): CONFIRMED DEFECT §4.17.
// osSecretKeyring.Get used to fire keyring.Get in a goroutine bounded only by a
// 2s timer and returned on timeout WITHOUT cancelling the probe, leaking one
// blocked goroutine per --secret lookup in a long repl session. The probe must
// now be context-cancellable, and a confirmed-unavailable verdict must be
// cached so later lookups never start another probe.
func TestOSSecretKeyringProbeCancelledAndCached(t *testing.T) {
	const timeout = 40 * time.Millisecond
	var probes atomic.Int32
	probeExited := make(chan struct{})
	probe := func(ctx context.Context, service, user string) (string, error) {
		probes.Add(1)
		<-ctx.Done() // emulate a keyring backend hung until the bound cancels it
		close(probeExited)
		return "", ctx.Err()
	}
	kr := osSecretKeyring{
		probe: probe,
		state: &keyringProbeState{timeout: timeout},
	}

	_, err := kr.Get(auth.KeyringService, "GOLAB_TEST_HUNG")
	if err == nil || err.Error() != "OS keyring lookup timed out" {
		t.Fatalf("error = %v, want the preserved timeout message", err)
	}
	// The timeout must cancel the probe: the injected backend unblocks only on
	// ctx.Done and signals its exit. A leaked goroutine never closes this.
	select {
	case <-probeExited:
	case <-time.After(2 * time.Second):
		t.Fatal("probe goroutine survived the timeout (leaked)")
	}

	// A confirmed-unavailable verdict is sticky: a second lookup must not
	// spawn another probe, only return the same bound error immediately.
	_, err2 := kr.Get(auth.KeyringService, "GOLAB_TEST_HUNG")
	if err2 == nil || err2.Error() != err.Error() {
		t.Fatalf("second error = %v, want %v", err2, err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe started %d times, want exactly 1 (unavailable not cached)", got)
	}
}

// The unavailable cache must not swallow ordinary successful reads: only a
// timeout poisons the keyring, a healthy backend is probed on every lookup.
func TestOSSecretKeyringSuccessfulProbeNotCached(t *testing.T) {
	var probes atomic.Int32
	kr := osSecretKeyring{
		probe: func(ctx context.Context, service, user string) (string, error) {
			probes.Add(1)
			return "value-" + user, nil
		},
		state: &keyringProbeState{timeout: time.Second},
	}
	for i := 0; i < 2; i++ {
		v, err := kr.Get(auth.KeyringService, "GOLAB_TEST_OK")
		if err != nil || v != "value-GOLAB_TEST_OK" {
			t.Fatalf("Get #%d = %q, %v", i, v, err)
		}
	}
	if got := probes.Load(); got != 2 {
		t.Fatalf("probe started %d times, want 2", got)
	}
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

// Local hardening (no upstream Python counterpart): the CLI's own control-plane
// credential must never be injectable into a VM. AGENTS.md separates the two
// planes: user OAuth/ADC bearer vs the short-lived data-plane token. Reading
// (KeyringService, KeyringUser) as a --secret would cross that boundary.
func TestResolveSecretValueRefusesReservedNames(t *testing.T) {
	const tokenBlob = `{"refresh_token":"cli-control-plane-token-9999"}`
	t.Run(auth.KeyringUser, func(t *testing.T) {
		os.Unsetenv(auth.KeyringUser)
		kr := &fakeSecretKeyring{values: map[string]string{
			auth.KeyringService + "/" + auth.KeyringUser: tokenBlob,
		}}
		_, err := resolveSecretValue(secretKeyringContext(kr), auth.KeyringUser)
		if err == nil {
			t.Fatal("expected reserved credential name to be refused")
		}
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != 2 {
			t.Fatalf("error = %v (%T), want exit 2", err, err)
		}
		if kr.gets != 0 {
			t.Fatalf("keyring read %d times, want 0", kr.gets)
		}
		if strings.Contains(err.Error(), tokenBlob) || strings.Contains(err.Error(), "refresh_token") {
			t.Fatalf("error echoed a credential value: %q", err)
		}
		if !strings.Contains(err.Error(), auth.KeyringUser) {
			t.Fatalf("error did not name the reserved key: %q", err)
		}
	})
	t.Run("every internal identifier and case variants", func(t *testing.T) {
		for _, name := range []string{
			auth.KeyringUser,
			strings.ToUpper(auth.KeyringUser),
			auth.LegacyTokenFileName,
			auth.OAuthTokenFileName,
		} {
			t.Run(name, func(t *testing.T) {
				os.Unsetenv(name)
				kr := &fakeSecretKeyring{values: map[string]string{
					auth.KeyringService + "/" + auth.KeyringUser: tokenBlob,
				}}
				_, err := resolveSecretValue(secretKeyringContext(kr), name)
				var ee *exitError
				if !errors.As(err, &ee) || ee.code != 2 {
					t.Fatalf("error = %v (%T), want exit 2", err, err)
				}
				if kr.gets != 0 {
					t.Fatalf("keyring read %d times, want 0", kr.gets)
				}
				if strings.Contains(err.Error(), "cli-control-plane-token-9999") {
					t.Fatalf("error echoed a credential value: %q", err)
				}
			})
		}
	})
	t.Run("similar but legitimate name still works", func(t *testing.T) {
		const name = "GOLAB_TEST_MY_OAUTH_TOKEN"
		os.Unsetenv(name)
		kr := &fakeSecretKeyring{values: map[string]string{
			auth.KeyringService + "/" + name: "legitimate-value",
		}}
		got, err := resolveSecretValue(secretKeyringContext(kr), name)
		if err != nil {
			t.Fatal(err)
		}
		if got.Reveal() != "legitimate-value" {
			t.Fatalf("secret = %q", got.Reveal())
		}
		if kr.gets != 1 {
			t.Fatalf("keyring read %d times, want 1", kr.gets)
		}
	})
}

// exec resolves secrets before attachSession, so a reserved name must exit 2
// with no control-plane request and no kernel execution (zero VM allocation).
func TestExecRefusesReservedSecretBeforeAllocation(t *testing.T) {
	const tokenBlob = `{"refresh_token":"cli-control-plane-token-9999"}`
	h := newHarness(t)
	// No putSession on purpose: any attach/allocation attempt would surface as
	// a control-plane request and fail this test.
	kr := &fakeSecretKeyring{values: map[string]string{
		auth.KeyringService + "/" + auth.KeyringUser: tokenBlob,
	}}
	ctx := WithSecretKeyring(WithDeps(context.Background(), h.deps), kr)
	code := execute(ctx, []string{"exec", "-s", "s1", "--secret", auth.KeyringUser},
		strings.NewReader("print(1)\n"), &h.stdout, &h.stderr)
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if kr.gets != 0 {
		t.Fatalf("keyring read %d times, want 0", kr.gets)
	}
	if len(h.ctrl.Requests()) != 0 || len(h.kernel.Executions()) != 0 {
		t.Fatal("reserved secret validated too late: control plane/kernel touched")
	}
	if strings.Contains(h.stderr.String(), tokenBlob) || strings.Contains(h.stderr.String(), "refresh_token") {
		t.Fatalf("credential leaked to stderr: %q", h.stderr.String())
	}
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
		// Every value is protected now, even the plain-looking keys (PLAIN,
		// EMPTY, CONNECTION). This used to assert only API_TOKEN, which encoded
		// the name-based classification that let E2E_KEY leak into history.
		if len(secrets) != len(wantVars) {
			t.Fatalf("secrets = %+v, want one per env-file value", secrets)
		}
		for i, want := range wantVars {
			if secrets[i].name != want.key || secrets[i].value.Reveal() != want.value {
				t.Fatalf("secrets[%d] = %+v, want name=%q value=%q", i, secrets[i], want.key, want.value)
			}
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

// Live-e2e regression (HIGH): an owner-only --env-file is an explicit secrets
// bridge, so EVERY value in it must be registered with execRedactor regardless
// of key name. E2E_KEY does not match the legacy sensitive-name regex, so the
// old code injected its value into the kernel but never redacted the prelude:
// the value landed verbatim in history/<session>.jsonl (and in `golab log -o`)
// and in the kernel-output surfaces.
func TestExecEnvFilePlainKeyNeverLeaks(t *testing.T) {
	const secret = "SUPERSECRETVALUE_9642"
	const name = "E2E_KEY"
	envPath := filepath.Join(t.TempDir(), "vars.env")
	if err := os.WriteFile(envPath, []byte(name+"="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		notebook bool
		json     bool
	}{
		{name: "script-text"},
		{name: "script-ndjson", json: true},
		{name: "notebook-text", notebook: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.putSession(t, "s1")
			var srcPath string
			if tc.notebook {
				n := nb.NewNotebook()
				n.Cells = []nb.Cell{nb.NewCodeCell("print(1)")}
				raw, err := nb.Marshal(n)
				if err != nil {
					t.Fatal(err)
				}
				srcPath = filepath.Join(t.TempDir(), "script.ipynb")
				if err := os.WriteFile(srcPath, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				srcPath = filepath.Join(t.TempDir(), "script.py")
				if err := os.WriteFile(srcPath, []byte("print(1)\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// /content prelude, then the cell carrying the injected --env-file
			// prelude. Every surface echoes the value to prove redaction.
			h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
			h.kernel.Script(
				fakekernel.Stream("stdout", "echo:"+secret+"\n"),
				fakekernel.Stream("stderr", "warn:"+secret+"\n"),
				fakekernel.DisplayData(map[string]any{"text/plain": secret}, nil, ""),
				fakekernel.Error("ValueError", secret, []string{"ValueError: " + secret + "\n"}),
				fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
			)
			args := []string{"exec", "-s", "s1", "-f", srcPath, "--env-file", envPath}
			if tc.json {
				args = append(args, "--json")
			}
			if code := h.run(t, "", args...); code != 1 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}
			// The value MUST still reach the kernel's code payload.
			execs := h.kernel.Executions()
			if len(execs) < 2 || !strings.Contains(execs[1].Code, pyRepr(secret)) {
				t.Fatalf("secret not injected into kernel payload: %+v", execs)
			}
			// History JSONL is the source of `golab log -o exported.jsonl`.
			hist, err := os.ReadFile(filepath.Join(h.dir, "s1.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			assertNoSecret(t, secret, string(hist))
			assertNoSecret(t, pyRepr(secret), string(hist))
			// The export itself must be clean too, not just its source.
			exportPath := filepath.Join(t.TempDir(), "exported.jsonl")
			if code := h.run(t, "", "log", "-s", "s1", "-o", exportPath); code != 0 {
				t.Fatalf("log exit=%d stderr=%q", code, h.stderr.String())
			}
			exp, err := os.ReadFile(exportPath)
			if err != nil {
				t.Fatal(err)
			}
			assertNoSecret(t, secret, string(exp))
			assertNoSecret(t, pyRepr(secret), string(exp))
			// stdout carries NDJSON records in --json mode; stderr is shared.
			assertNoSecret(t, secret, h.stdout.String(), h.stderr.String())
			assertNoSecret(t, pyRepr(secret), h.stdout.String(), h.stderr.String())
			// The deferred notebook write shares the same redacted outputs.
			if tc.notebook {
				outPath := strings.TrimSuffix(srcPath, ".ipynb") + "_output.ipynb"
				out, err := os.ReadFile(outPath)
				if err != nil {
					t.Fatal(err)
				}
				assertNoSecret(t, secret, string(out))
				assertNoSecret(t, pyRepr(secret), string(out))
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
