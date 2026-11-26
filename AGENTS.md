# Agent Instructions — golab

Unofficial Go port of `google-colab-cli`; one static binary (`CGO_ENABLED=0`). Full contributor rules: [CONTRIBUTING.md](CONTRIBUTING.md). Honest verification status: [docs/CONFORMANCE.md](docs/CONFORMANCE.md). Where docs and code disagree, **code wins** — fix the doc in the same change.

## Entrypoints & package boundaries
- Entry: `cmd/golab/main.go` → `cli.Execute` (`internal/cli/root.go`); dependency wiring is the `internal/app` composition root. **Register every new command in `internal/cli/register.go`** (single registration point).
- `internal/cli` may import anything, but **only `cmd/golab` imports `internal/cli`** (no other `internal/` package does).
- Protocol packages (`colabapi`, `runtime`, `contents`, `sshx`) take plain values and **never import `state` or `cli`** (keeps them promotable to a public module).
- Two planes, two credentials: control plane = user OAuth/ADC bearer; data plane = short-lived runtime-proxy token. `colab auth` is a third thing (injects credentials into the VM). Never conflate them.
- Fire-and-forget: no daemons/background services; all I/O functions take `context.Context`. Package-level vars exist, but only in three categories: test seams (function variables with production defaults that tests override, e.g. the CLI command/kill/hooks in `internal/cli`, the raw-TTY and signer hooks in `internal/sshx`, and the executable-path and trust-loader seams in `internal/update`), effectively-constant lookup tables (regexes, error sentinels, static name/flag maps), and the one package-level `sync.Once` for the Termux net-fix (`internal/platform/platform.go`). No runtime-mutable business state lives at package scope.

## Commands
| Task | Command |
|------|---------|
| Build (`./golab`) | `make build` |
| PR gate | `make check` = vet + `go test -race -count=1 ./...` + golangci-lint + `make redaction` + `make paths` |
| Single test | `go test ./internal/<pkg> -run '^TestName$' -count=1` |
| Focused package | `go test -race -count=1 ./internal/<pkg>` |
| Cross matrix | `make cross` → `make cross-smoke` (artifacts in `./dist`) |
| Coverage | `make coverage` → `bash scripts/coverage-gate.sh dist/coverage.out` |
| Fuzz smoke | `make fuzz` (10 s, both seeds) |

- `make all` = `check` + `build`. Go **1.27.4** pinned in `go.mod`; golangci-lint **v2** (v2.15.0 in CI).
- CI gotchas: `windows-11-arm` drops `-race`; the coverage gate is floored at **70** via `COVERAGE_MIN` (target 80); `goleak-check` is a hard gate (goleak is wired in `runtime`/`sshx`/`repl`/`update`). Check `docs/CONFORMANCE.md` before treating a gate or harness as real.

## Tests & fixtures
- **Test-first, non-negotiable:** write the failing test first, name the upstream Python test in a comment, watch it fail, then implement.
- Fakes are the default (`internal/testutil/fakecolab`, `fakekernel`, `fakesshd`). **Fakes are not live proof** — no feature is "done" until a human runs the live e2e.
- `testdata/wire/` and `testdata/golden/` contain only READMEs — **zero fixtures**. Redacted ground truth must be captured from the official Python CLI by a human (`scripts/live-capture.sh`, GET-only). Never fabricate a fixture.
- Golden harness: `COLAB_BIN=./golab bash scripts/golden.sh --scenario <name> --update` (review diff; record divergences in `DELTAS.yaml`). `bash scripts/golden.sh --scenario wire` secret-scans wire fixtures.

## Security invariants (every change)
- Tokens, OAuth codes/verifiers, client secrets, `Authorization`/`Cookie`/proxy-token headers never reach logs, argv, env dumps or errors — use the `Secret` type; run `make redaction`.
- No `InsecureSkipVerify`; no env var or flag that changes Colab hosts in release builds.
- All `sessions.json`/`settings.json` writes go through `internal/state` (lock + temp + fsync + rename); a corrupt file aborts, never resets.
- Remote paths use `path`, local paths use `filepath` (`make paths`). `CGO_ENABLED=0` always; no `GOOS=android`. Termux DNS/CA shim lives in `internal/platform/netfix`.
- Validate scripts/flags/accelerators/keys **before any allocation** (spends money). Never splice user strings into code run on the VM — send JSON/`repr`-quoted payloads.
- New dependency ⇒ one-line justification in root `DEPENDENCIES.md`; `govulncheck ./...` must stay clean.

## Command & protocol conventions
- Program output → stdout; every `[colab] …` diagnostic → stderr. Detect TTY only via `internal/platform` helpers so CI never blocks on a real TTY.
- `run`/`ssh` disable interspersed flags so unknown flags after the script path are forwarded verbatim. There is no `--debug` flag; debugging is `COLAB_DEBUG=1`.
- Map errors to messages/exit codes in one table: `internal/cli/errors.go`. Use typed errors (`errors.As`/`errors.Is`), never string-match `"404"`/`"401"`.
- Control-plane mutations: strip the XSSI prefix and do GET-then-POST XSRF; every HTTP call gets explicit connect/TLS/header/idle timeouts.
- Kernel WS: `colab_request` arrives on iopub; `input_reply` on stdin wraps `colab_reply`.

## Workflow & release
- Branch off `main`, one change per PR. Re-read and update the affected `docs/` page + dated note, and add a `CHANGELOG.md` `[Unreleased]` entry for user-visible changes, **in the same commit**.
- Fetch before push; never force-push `main`; amend only unpushed commits made this session. Suggest `git diff main..<branch>` for review, not `git show <sha>`.
- A globally installed Python `colab` can shadow the binary — check `which colab` / `colab version` when reproducing reports.
- **Never cut a release autonomously.** Confirm the version with the requester; require an annotated tag (`git tag -a`). `.github/workflows/release.yml` is pinned in the verifier's certificate identity — do not rename it. Validate with `goreleaser check && goreleaser release --snapshot --clean`.

## Human-only (ask a maintainer)
`colab auth` (OAuth consent), `colab drivemount`, interactive-TTY `repl`/`ssh`, live probes, signed-release verification. Live probes allocate billable VMs: prefer GET-only, log every endpoint, unassign before finishing, end with `golab sessions` empty. `scripts/live-sweeper.sh` is dry-run unless `APPLY=1`.

- Always update docs and CHANGELOG
- FOLLOW Semantic Versioning rules