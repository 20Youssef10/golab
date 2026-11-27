<!-- Generated from docs/03-go-architecture.md §1. Goals and non-goals, 2. Repository layout, 3. Key interfaces, 4. Concurrency model, 5. Error model, 9. Architecture Decision Records by docs-site/tools/sync-sources.py. Do not edit. -->

## 1. Goals and non-goals

**Goals**
1. **Drop-in parity** with `colab` 0.8.x for every non-removed command, flag, exit code and on-disk file (users switch without re-login or losing sessions).
2. **One static binary per OS/arch**: no Python, no `ssh`, no `ssh-keygen`, no keyring daemon required.
3. **First-class Windows and Termux**, the two platforms upstream cannot support.
4. **Fix the defects in doc 01 §10** rather than porting them.
5. Make the protocol layer reusable (`internal/` first, promotable to `pkg/` later).

**Non-goals (v1):** a GUI; Colab Enterprise/Vertex; porting `console`; a daemon/keep-alive; re-implementing Google's server behaviour; supporting other notebook backends.

## 2. Repository layout

```text
golab/
├── cmd/golab/main.go              # entry point: calls cli.Execute (internal/cli/root.go)
├── internal/
│   ├── cli/                       # cobra commands (one file per command); register.go is the single registration point
│   ├── app/                       # composition root: Deps{Auth,Colab,State,History,Render,Clock,…}
│   ├── auth/                      # providers (adc, oauth2), scopes, tokenstore (keyring|file)
│   ├── colabapi/                  # /tun/m/* + Public API client: assign, unassign, list, ccu, credprop; XSSI; retry
│   ├── runtime/                   # Jupyter REST+WS client, execute, outputs, stdin, colab_request
│   ├── contents/                  # Contents API (chunked)
│   ├── sshx/                      # WS net.Conn, embedded ssh client, proxy bridge, key resolution
│   ├── state/                     # sessions.json, settings.json: lock + atomic write + migration
│   ├── history/                   # JSONL logger + exporters (ipynb|md|txt|jsonl)
│   ├── nb/                        # minimal nbformat v4(.5) read/write
│   ├── render/                    # ANSI, markdown, html→md, kitty images, TTY/NO_COLOR logic
│   ├── repl/                      # interface + implementation
│   ├── platform/                  # paths, termux/android detect, netfix, perms, browser, tty, signals
│   ├── update/                    # version check, signed self-update (sigstore-go)
│   ├── version/                   # Number = "0.3.0", Upstream = "0.8.x+HEAD", ClientAgent()
│   ├── logx/                      # slog setup, redaction, rotation
│   ├── doctor/ auditsec/ budget/ bundle/ agentout/   # Go-only commands
│   └── testutil/                  # fakecolab, fakekernel, fakesshd
├── testdata/wire/README.md        # capture target only — no fixtures yet
├── testdata/golden/README.md      # golden target only — no fixtures yet
├── scripts/                       # golden.sh, differential.sh, live-capture.sh, live-sweeper.sh, coverage-gate.sh, …
├── docs/                          # this pack
├── .goreleaser.yaml
└── .github/workflows/
    ├── ci.yml
    ├── live-e2e.yml
    ├── qemu-smoke.yml
    ├── release.yml
    └── termux-nightly.yml
```
Rule: **`internal/cli` may import everything; nothing imports `internal/cli`.** `colabapi`, `runtime`, `contents`, `sshx` never import `state` or `cli` (they take plain values), which keeps them promotable to a public module.

## 3. Key interfaces (sketch)

```go
// colabapi
type Client interface {
    Assign(ctx context.Context, req AssignRequest) (Assignment, error) // handles XSRF, retry, reconcile
    Unassign(ctx context.Context, endpoint string) error
    List(ctx context.Context) ([]ListedAssignment, error)
    Usage(ctx context.Context) (Consumption, error)
    CredentialsPropagation(ctx context.Context, endpoint string) *CredProp // dryrun/propagate flow
}
type AssignRequest struct {
    NotebookHash uuid.UUID; Variant Variant; Accelerator Accelerator; HighMem bool
}

// runtime
type Runtime interface {
    Attach(ctx context.Context, s Target) (Kernel, error)          // creates/reuses kernel, returns effective IDs
}
type Kernel interface {
    Execute(ctx context.Context, code string, o ExecOptions) (Result, error)
    Restart(ctx context.Context) error
    Interrupt(ctx context.Context) error
    Shutdown(ctx context.Context) error
    Close() error                                                   // closes WS only
}
type ExecOptions struct {
    IdleTimeout time.Duration            // 0 = unlimited
    AllowStdin  bool
    OnOutput    func(Output)             // streaming hook
    StdinHook   func(prompt string) (string, error)
    ColabRequestHook func(Request) (handled bool)
}
type Result struct { Outputs []Output; Status string; ExecutionCount *int }

// auth
type TokenSource interface{ Token(ctx context.Context) (*oauth2.Token, error) } // from x/oauth2
type Provider interface{ HTTPClient(ctx context.Context, scopes []string) (*http.Client, error) }
type TokenStore interface{ Load() ([]byte, error); Save([]byte) error; Delete() error }

// state
type Store interface {
    List() (map[string]Session, error); Get(string) (*Session, error)
    Put(Session) error; Delete(string) error
}
```

All I/O-bearing functions take `context.Context`. **No package-level mutable state** except one `sync.Once` Termux net-fix (`platform`).

## 4. Concurrency model of the kernel client

```mermaid
flowchart LR
    WS[(WebSocket)] --> R[single reader goroutine]
    R --> D{dispatch by channel}
    D --> I[iopub chan]
    D --> S[shell chan]
    D --> T[stdin chan]
    D --> X[colab_request hook]
    E[Execute()] --> W[writer mutex] --> WS
    E -- select --> I & S & T & timer & ctx
```
* One reader, one writer (mutex). Channel buffers sized so a slow consumer applies back-pressure rather than dropping messages.
* `Execute` is a `select` over iopub, shell, stdin, `ctx.Done()` and an **idle timer reset on every received message** (identical semantics to upstream, §3.4 of doc 02).
* `ctx` cancellation (SIGINT) → `Interrupt()` (REST) then wait for idle; a **second** SIGINT force-closes. (Behaviour change, DEC-05.)
* The `colab_request` hook runs **on the reader goroutine before queueing**, exactly as upstream intercepts in `on_message`; it must not block (long human waits happen in a separate goroutine that later sends the stdin reply).
* WS ping every 60 s; read limit raised to ≥ 64 MiB (configurable).

## 5. Error model
* Typed errors: `colabapi.StatusError{Code, Body}`, `colabapi.ErrTooManyAssignments`, `runtime.ErrSessionLost`, `runtime.ErrKernelGone`, `runtime.ErrTimeout{Idle}`, `sshx.HandshakeError{Status, Body}`.
* `errors.As`/`errors.Is` everywhere; **never** string-match "404"/"401".
* The CLI layer maps errors to `[colab] …` messages and exit codes in one table (`internal/cli/errors.go`), so messages stay identical to upstream and are unit-testable.

## 9. Architecture Decision Records

### ADR-001 · Naming, module and Go version
**Decision.** Repo/module `github.com/<owner>/colab-go`; binary **`colab`** (drop-in) with an install-time alias option `colab-go`. Latest stable Go at kickoff, pinned with a `toolchain` directive; CI also builds with the previous minor.
**Why.** Agents/scripts call `colab` and shebangs resolve via `$PATH`. The names `colab-cli` are already taken on PyPI and crates.io, so avoid them as the *distribution* name.
**Caveat.** If Python `colab` is also installed, PATH order decides who wins (upstream's `AGENTS.md` #8 describes exactly this trap); `golab version` prints the implementation (`golab 0.3.0 (compatible with google-colab-cli 0.8.x+HEAD)`). The original `colab-go …` text above is historical.
**Branding.** Unofficial. Must not imply Google endorsement ("Colab" is Google's mark).

### ADR-002 · Static builds, `CGO_ENABLED=0`, everywhere including Termux
**Decision.** Ship pure-Go static binaries; on Termux ship **`linux/arm64` (and amd64/arm)**, not `GOOS=android`, plus an in-binary Android shim (ADR in doc 04 §Termux).
**Why.** Verified in the research sandbox (Go 1.22.2): `GOOS=android CGO_ENABLED=0` *does* compile but yields a PIE whose interpreter is `/system/bin/linker64`; independent projects report that such binaries cannot resolve DNS and can mis-read `argv` when started through the linker. Static `linux/arm64` binaries run under Termux but need the DNS/CA shim.
**Alternatives rejected.** Cgo/NDK builds (CI complexity, no cross-compile); relying on `termux-chroot`/proot (extra user setup).
**Revisit if** the Termux community Go toolchain patches (resolv.conf, CA path) prove stable → add a `termux-packages` recipe.

### ADR-003 · CLI framework: `spf13/cobra`
**Why.** Per-command `SetInterspersed(false)` + `--` handling match `colab run script.py --script-flag` and `colab ssh -- CMD` (upstream needed `context_settings` hacks in Typer, `AGENTS.md` #20). Shell completion for bash/zsh/fish/**PowerShell** out of the box; wide ecosystem.
**Alternatives.** `kong` (cleaner structs, weaker completion), `urfave/cli/v3`.
**Rule.** Interspersed parsing disabled on `run` and `ssh`; unknown flags after the script path are forwarded verbatim; `--env` accepted both before and after the script (upstream parses both).

### ADR-004 · State: same files, locked, atomic
**Decision.** Same JSON schema and locations; every write = write temp in the same dir → `fsync` → atomic rename (Windows: `MoveFileEx` replace); `.bak` of the last good file; **parse failure aborts** with a clear message instead of treating the file as empty (fixes D-1). Cross-process locking via `.lock` file (`flock`/`LockFileEx`). Session names validated (fixes D-2).
**Why.** Compatibility with Python state + removing silent data loss.

### ADR-005 · SSH: embedded client by default, bridge for external tools
**Decision.**
* `colab ssh` / `colab ssh -- CMD` use `golang.org/x/crypto/ssh` over a `net.Conn` made from the WebSocket (`websocket.NetConn`). **No system `ssh`/`ssh-keygen` needed**; the public-key header is derived natively from the private key (or `.pub`).
* `colab ssh --proxy-mode` keeps the stdio bridge so OpenSSH, VS Code Remote-SSH, JetBrains Gateway, `scp`, `rsync` work.
* Auth order: `--identity` → ssh-agent (`SSH_AUTH_SOCK`; Windows OpenSSH agent named pipe; Pageant) → `~/.ssh/id_ed25519` → `id_ecdsa`. RSA never auto-selected (server rejects it).
* **Host keys:** accept any (like upstream's `StrictHostKeyChecking=no`). Justification: VM host keys are regenerated per runtime and the channel is already TLS-authenticated to Google with a bearer token. We document it, pin nothing, and do not ship a `known_hosts` file. (A gosec suppression with an explanatory comment is required.)
* PTY/raw-mode/resize handled with `x/term` plus Windows console-mode code; resize via `SIGWINCH` on Unix and a 250 ms poll on Windows.
**Why.** Removes the largest platform liability (Windows/Termux often lack a usable OpenSSH client) and the ProxyCommand-quoting class of bugs (doc 01 §8.11).
**Risk.** Behavioural divergence from OpenSSH (agent forwarding, `~/.ssh/config`, ControlMaster) → keep `--proxy-mode` documented as the escape hatch.

### ADR-006 · Token storage tiers
**Decision.** (1) OS keyring (`zalando/go-keyring`: Keychain / Credential Manager / Secret Service) with a **hard 5 s probe timeout** cached per process; (2) fallback **0600 file** in the config dir (ACL-restricted on Windows) with a one-line stderr notice; (3) optional passphrase-encrypted file (`COLAB_TOKEN_PASSPHRASE`, age/scrypt). Store the **refresh token + metadata only** and mint access tokens on demand (keeps blobs under Windows Credential Manager's ~2.5 KB limit and shrinks exposure).
**Termux/headless Linux** default to tier 2 (no Secret Service).
**Import.** On first run, read Python's keyring entry and the legacy `token.json` and convert.
**Why.** Python's behaviour on locked keyrings was a 30 s stall; fail fast and fall back.

### ADR-007 · WebSocket library: `coder/websocket`
**Why.** Context-aware, `NetConn()` adapter (needed by `x/crypto/ssh`), custom headers and subprotocols, configurable read limit, active maintenance. **Alternative:** `gorilla/websocket` (revived, but needs a hand-written `net.Conn` adapter).
**Rule.** Always `SetReadLimit` explicitly; never rely on the default.

### ADR-008 · Own Jupyter client (no dependency)
**Decision.** Implement ~600 LOC: REST kernel lifecycle, default-protocol framing (JSON + binary-buffer variant), channel dispatch, output accumulator (doc 02 §3.5), stdin, `colab_request`. No third-party Jupyter library exists in Go that is maintained and Colab-compatible.
**Hardening.** Fuzz the frame decoder; cap message sizes; validate offsets exactly as upstream does.

### ADR-009 · Execution engine behaviours
1. Idle-timeout semantics identical to upstream (reset per message; ≤0 unlimited).
2. Optional `kernel_info_request` handshake before the first execute (behind a flag, default **off** for parity until spike S1 shows it helps).
3. SIGINT → interrupt then graceful stop (DEC-05).
4. Always persist the **effective** kernel id (fixes the stale-id bug in doc 02 §3.2).
5. Streaming and stdin become **compatible** (upstream forbids the combination): needed for interactive `auth`/`drivemount`.

### ADR-010 · Rendering and REPL
Rendering: `glamour` for markdown, `html-to-markdown` for `text/html`, raw passthrough for ANSI, Kitty protocol for PNG/JPEG on TTY (also saved to `--output-image` or a temp file, exactly as upstream). REPL sits behind `repl.Frontend`; choose between `reeflective/readline`, Bubble Tea `textarea`, or `go-prompt` fork in spike **S7** by testing **Windows Terminal, conhost, Termux, tmux** (multi-line paste, Enter vs Alt-Enter, `Ctrl-J`). `chroma` supplies Python highlighting. REPL is P1: the rest of the tool must not depend on it.

### ADR-011 · Logging: see §8.

### ADR-012 · Output/exit contract & deprecations: see §7.

### ADR-013 · Test hooks
Upstream cannot point the CLI at a fake backend. We add a **build-tag-gated** (`-tags testhooks`) override for the Colab domain and runtime URL, plus `httptest`-based fakes. Never compiled into release binaries (an env var that redirects bearer tokens to an arbitrary host would be a vulnerability). For differential tests against the Python CLI we instead use an HTTPS-intercepting proxy (doc 07 §Differential).

### ADR-014 · Release engineering
GoReleaser; `CGO_ENABLED=0 -trimpath -buildvcs`; reproducible builds; SHA-256 checksums; **cosign keyless signatures + SBOM (syft)**; an updater that verifies checksum **and** signature against a pinned identity; channels in doc 04 §Distribution.

**Implementation note (2026-11-23).** `internal/update` uses
`sigstore-go/pkg/verify` with the public-good TUF trust root: Fulcio CA-chain
validation, Rekor inclusion proof and SET, certificate-time checks, an OIDC
issuer/SAN pinned to this repository's release workflow, and an in-toto digest
bound to the downloaded bytes. A second signed CycloneDX/SPDX attestation is
**required** — GoReleaser must publish `*.sbom.sigstore.json` or `Check` fails
with `ErrMissingSBOM`. Fulcio CT SCT checking **is enforced** in production
(`internal/update/verify.go:157-163` adds
`verify.WithSignedCertificateTimestamps(1)`; only an explicit test-CA opt-out
for SCT-less bundles can relax it, and a present SCT is still verified). TUF
refresh and live release verification still need a human. Signed bundles must be
sigstore v0.3 protobuf format — legacy cosign bundles pass `cosign verify-*` but
break `golab update --install` (see `docs/CONFORMANCE.md` §4).
