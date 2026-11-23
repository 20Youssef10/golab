# colab-go: Agent Guidelines

Unofficial Go implementation of the Google Colab CLI (`google-colab-cli` 0.8.x compatible).
Read `docs/01`–`docs/08` before changing behaviour. **Where docs and code disagree, code wins and the doc must be fixed in the same change.**

## Architecture (one screen)
- `cmd/colab` wires dependencies. `internal/cli` (cobra) may import anything; **nothing imports `cli`**.
- Protocol packages (`colabapi`, `runtime`, `contents`, `sshx`) take plain values and never import `state` or `cli`.
- Two planes, two credentials: control plane (`/tun/m/*`, user OAuth/ADC bearer) vs data plane (runtime proxy token). **Never confuse them.** `colab auth` is a third thing: it injects credentials *into the VM*.
- Fire-and-forget: no daemons, no background services. A command authenticates, works, exits. (A keep-alive daemon was removed upstream.)
- Colab extends Jupyter: `colab_request` on iopub; reply is `input_reply` on **stdin** wrapping `colab_reply`.
- Control-plane JSON may carry an XSSI prefix; mutations use GET-then-POST XSRF.

## Core mandates
1. **Test first.** Write the failing test (name the upstream Python test it ports in a comment), see it fail, then implement.
2. **Protocol fidelity.** New endpoints are validated against captured traces (`testdata/wire/`), not guesses.
3. **No live proof, no "done".** Fake-server tests are necessary, not sufficient. Run the live e2e (below) before declaring a feature complete.
4. **Piping matters.** Program output → stdout; `[colab] …` diagnostics → stderr. Detect TTY via the helpers in `internal/platform` so tests can mock them (never block on a real TTY in CI).
5. **Explicit timeouts everywhere.** No HTTP call without connect/TLS/header/idle limits. No unbounded waits except the documented `--timeout 0`.
6. **Typed errors.** Use `errors.As/Is`. Never match "404"/"401" in strings.
7. **Atomic state.** All writes to `sessions.json`/`settings.json` go through `internal/state` (lock + temp + fsync + rename). A corrupt file aborts; it is never treated as empty.
8. **Validate before spending money.** Check scripts, flags, accelerators and keys **before** any allocation request.
9. **Never splice user strings into code that runs on the VM.** Send JSON/`repr`-quoted payloads.
10. **Remote paths use `path`, local paths use `filepath`.** The VM is Linux even when you are on Windows.

## Security rules (non-negotiable)
- Tokens, OAuth codes/verifiers, client secrets, `Authorization`, `Cookie`, `X-Colab-Runtime-Proxy-Token`, `X-Goog-Colab-Token` **never** reach logs, argv, env dumps or error messages. Use the `Secret` type; run the redaction golden test.
- No `InsecureSkipVerify`. No env var or flag that changes Colab hosts in release builds (host override exists only under `-tags testhooks`).
- Session names: `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, no Windows reserved names.
- Untrusted remote output is sanitised before it reaches the terminal (strip OSC/DCS/APC and query sequences; keep SGR).
- New dependency ⇒ one-line justification in `docs/DEPENDENCIES.md`, `govulncheck` clean.

## Platform rules
- `CGO_ENABLED=0` always. No `GOOS=android` artefacts.
- Termux: DNS/CA shim lives in `internal/platform/netfix`; do not add env-var workarounds elsewhere. Avoid `exec.LookPath` on Android hot paths. Use `$TMPDIR`/`$PREFIX/tmp`.
- Windows: enable VT console modes and restore them on **every** exit path; teardown also on console-close events; never delete the running `.exe`.
- Token store tiers: keyring (5 s probe, cached) → 0600 file. Store the refresh token only.
- Run `GOOS=<each tier> go build ./...` (or `goreleaser build --snapshot`) before pushing platform-touching changes.

## Workflow
1. **Draft:** new git branch per change; plan; list test cases.
2. **Refine:** `go vet ./... && golangci-lint run && go test -race -count=1 ./...`
3. **Finalize:** re-read the affected `docs/` page and update it; add a dated note to its frontmatter/changelog section; commit. Suggest the review command `git diff main..<branch>` (full cumulative diff), not `git show <sha>`.
- Fetch before pushing; never force-push `main`; amend only commits you created in this session and have not pushed.
- Reproduce a reported failure on `main` **before** assuming your change caused it.
- A globally installed Python `colab` may shadow this binary on `PATH`: check `which colab` and `colab version` (it names the implementation).

## Live tests: what agents can and cannot run
- **Can:** unit/component tests, lint, fakes, `colab new/status/stop/ls/upload/download/install/usage`, `colab exec <file>`, piped `colab repl`, `colab ssh -- CMD </dev/null`, `colab run script.py`.
- **Cannot (needs a human):** `colab auth`, `colab drivemount`, interactive TTY `repl`/`ssh`, the OAuth consent step. Build the logic, write fake-server tests, then ask the user to run the live check.
- **Live probes allocate real, billable VMs.** Prefer GET-only probes. Record every endpoint you create; unassign before finishing; end with `colab sessions` showing nothing. Unassign orphans (`[?]` entries) explicitly.
- Heed research caveats: a "the API might reject X" note is a TODO to probe, not a footnote. Verify claims from tools (including AI tools) against primary sources.

## Release rules
Release **only when the user explicitly asks.** SemVer; breaking = CLI surface, flag semantics, on-disk schema, token format. Flow: changelog PR → merged → annotated tag `vX.Y.Z` on the merge commit → GoReleaser. Confirm the inferred version with the user before tagging.

## Continuous improvement
Whenever the user gives feedback or a correction, encode it **here** immediately so the mistake is never repeated.
