# Contributing to golab

Thanks for helping build **golab**, an unofficial Go implementation of Google's
`google-colab-cli` (0.8.x compatible). This project is Apache-2.0 licensed and
community-maintained. By participating you agree to our
[Code of Conduct](CODE_OF_CONDUCT.md).

> "Google" and "Colab" are trademarks of Google LLC. This is an independent,
> unofficial port; it is not endorsed by Google.

## Development setup

You need **Go 1.27.4** (pinned in `go.mod`; CI builds from `go-version-file`) and
`golangci-lint` v2. `CGO_ENABLED=0` is exported by the Makefile — keep it that
way.

```bash
git clone https://github.com/20Youssef10/golab.git
cd golab

make build          # CGO_ENABLED=0, -trimpath -> ./golab
./golab version      # prints "golab <ver> (compatible with google-colab-cli 0.8.x+HEAD)"
./golab --help

make check          # go vet + go test -race + golangci-lint + redaction/paths scripts
```

Useful additional gates:

| Command | What it runs |
|---|---|
| `make test` | `go test -race -count=1 ./...` |
| `make lint` | `golangci-lint run ./...` |
| `make cross` / `make cross-smoke` | full cross matrix into `./dist` + artifact checks |
| `make coverage` / `make coverage-gate` | coverage profile + threshold (target 80% for core packages) |
| `make fuzz` | short fuzz smoke on the frame decoder and sanitiser |
| `make redaction` / `make paths` | secret-redaction and remote/local path safety scripts |

Read `AGENTS.md` and `docs/01`–`docs/09` before changing behaviour. Where the
docs and the code disagree, **the code wins and the doc must be fixed in the
same change**.

## The test-first mandate (non-negotiable)

1. **Write the failing test first.** Name the upstream Python test it ports in a
   comment, so provenance is auditable:

   ```go
   // Port of upstream tests/test_ssh.py::test_ssh_force_kills_stale_sshd
   func TestSSHForceKillsStaleChildren(t *testing.T) { /* ... */ }
   ```

2. **Watch it fail.** Confirm the test fails for the intended reason before
   touching production code.
3. **Then implement.** Fake-server tests are the default (`internal/testutil`);
   validate new endpoints against captured traces in `testdata/wire/`, never
   guesses. A "the API might reject X" note is a TODO to probe, not a footnote.
4. **No live proof, no "done".** Fake tests are necessary, not sufficient. A
   feature is not called complete until the live e2e is run by a human (see
   below).

## Package boundaries

- `cmd/golab` wires dependencies; `internal/app` is the composition root.
- `internal/cli` (cobra) **may import anything**; **nothing imports `internal/cli`**.
- Protocol packages (`colabapi`, `runtime`, `contents`, `sshx`) take plain values
  and **never import `state` or `cli`** — this keeps them promotable to a public
  module.
- Two planes, two credentials: control plane (`/tun/m/*`, user OAuth/ADC bearer)
  vs data plane (runtime proxy token). Never confuse them. `colab auth` is a
  third thing: it injects credentials *into the VM*.
- Fire-and-forget: no daemons, no background services.
- All I/O-bearing functions take `context.Context`; no package-level mutable
  state except the one `sync.Once` Termux net-fix.

### Adding a command

Add one file under `internal/cli/` and register it in `internal/cli/register.go`.
Keep parsing quirks in mind (`run` and `ssh` disable interspersed flags so
unknown flags after the script path are forwarded verbatim). Route program
output to stdout and all `[colab] …` diagnostics to stderr; detect TTY only via
the helpers in `internal/platform` so CI never blocks on a real TTY. Map errors
to messages/exit codes in one table (`internal/cli/errors.go`). Validate scripts,
flags, accelerators and keys **before** any allocation request (spending money).

### Adding a protocol endpoint

Put wire logic in the owning protocol package (`colabapi`, `runtime`,
`contents`, `sshx`) with plain-value inputs. Handle the XSSI prefix and
GET-then-POST XSRF for control-plane mutations; use typed errors with
`errors.As`/`errors.Is` (never string-match `"404"`/`"401"`). Give every HTTP
call explicit connect/TLS/header/idle timeouts. Capture or extend a trace in
`testdata/wire/` and drive the fake server from it. Never splice user strings
into code that runs on the VM — send JSON/`repr`-quoted payloads. Remember:
Colab extends Jupyter with `colab_request` on iopub and `input_reply` on
**stdin** wrapping `colab_reply`.

## Security rules (apply to every change)

- Tokens, OAuth codes/verifiers, client secrets, `Authorization`, `Cookie`,
  `X-Colab-Runtime-Proxy-Token` and `X-Goog-Colab-Token` **never** reach logs,
  argv, env dumps or error messages. Use the `Secret` type and run the redaction
  golden test (`make redaction`).
- No `InsecureSkipVerify`. No env var or flag that changes Colab hosts in
  release builds (host override exists only under `-tags testhooks`).
- Session names: `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, no Windows reserved names.
- Sanitise untrusted remote output before it reaches the terminal (strip
  OSC/DCS/APC and query sequences; keep SGR).
- **Atomic state:** every write to `sessions.json`/`settings.json` goes through
  `internal/state` (lock + temp + fsync + rename). A corrupt file aborts; it is
  never treated as empty.
- Remote paths use `path`, local paths use `filepath` — the VM is Linux even on
  Windows.
- New dependency ⇒ one-line justification in `docs/DEPENDENCIES.md`, and
  `govulncheck` must stay clean.
- `CGO_ENABLED=0` always; no `GOOS=android` artefacts. Termux's DNS/CA shim
  lives in `internal/platform/netfix`. On Windows, restore VT console modes on
  every exit path.

See [SECURITY.md](SECURITY.md) for how to report a vulnerability.

## Docs, branches and commits

- **One branch per change.** Before pushing, re-read the affected `docs/` page,
  update it, and add a dated note to its frontmatter/changelog section. Code
  changes that alter behaviour must update the relevant doc in the same commit
  (AGENTS.md rule).
- Run `go vet ./... && golangci-lint run && go test -race -count=1 ./...` (i.e.
  `make check`) before requesting review.
- Fetch before pushing; never force-push `main`; amend only commits you created
  in this session and have not pushed. Reproduce a reported failure on `main`
  before assuming your change caused it.
- Use short, imperative commit subjects (`fix(ssh): kill stale sshd with --force`).
  Reference issue numbers when relevant.
- For review, suggest `git diff main..<branch>` (the full cumulative diff), not
  `git show <sha>`.
- A globally installed Python `colab` may shadow this binary on `PATH`: check
  `which colab` and `colab version` when reproducing reports.

## Live e2e: agents vs humans

Automated agents **can** run unit/component tests, lint, fakes, and
`colab new/status/stop/ls/upload/download/install/usage`, `colab exec <file>`,
piped `colab repl`, `colab ssh -- CMD </dev/null`, `colab run script.py`.

Only a **human** can run `colab auth` (OAuth consent), `colab drivemount`,
interactive-TTY `repl`/`ssh`, and the live-probe checks in
`docs/REMEDIATION.md`. If your change touches these paths, build the logic and
fake-server tests, then ask a maintainer to run the live check. Live probes
allocate real, billable VMs: prefer GET-only probes, record every endpoint,
unassign before finishing, and end with `golab sessions` showing nothing
(unassign `[?]` orphans explicitly). The outstanding human-only items are
tracked in `docs/REMEDIATION.md`, and no release is cut until they are closed or
explicitly waived by a maintainer.

## Pull request checklist

- [ ] Branch off `main`; one logical change per PR.
- [ ] Failing test written first, upstream test named in a comment; new endpoints
      backed by `testdata/wire/` fixtures.
- [ ] `make check` is green; `make cross` green for platform-touching changes.
- [ ] Affected `docs/` page re-read, updated, dated note added.
- [ ] No tokens, secrets, raw wire captures or redaction regressions in the diff.
- [ ] Live e2e run by a human (or explicitly requested from a maintainer) for
      auth/interactive/release-affecting changes.
- [ ] `CHANGELOG.md` entry under `[Unreleased]` for user-visible changes.
- [ ] PR description summarises behaviour, risk, and verification evidence.

## Related documents

- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Security policy](SECURITY.md)
- [Release runbook](docs/RELEASING.md)
- [Governance](GOVERNANCE.md)
