# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Breaking changes are defined as changes to the CLI surface, flag semantics,
on-disk schema or persisted token format (per AGENTS.md).

## [Unreleased]

## [0.1.2] - 2026-11-23

### Fixed

- Windows CI: skip POSIX directory-mode assertions, avoid blocking on `CONIN$`
  when stdin is not a TTY (Drive consent fixture), and disable `-race` on
  windows/arm64 where the Go race detector is unavailable.
- Release workflow: pass `GITHUB_TOKEN` to GoReleaser so publishing can proceed.

### Verification

Local `go test`, Windows test-binary compile, YAML parse, `goreleaser check`.

## [0.1.1] - 2026-11-23

### Fixed

- Release workflow's annotated-tag check: explicitly fetch tag refs before
  inspecting them (checkout leaves a detached HEAD).
- CI on Windows: tests no longer assert POSIX `0600` file modes where Windows
  reports `0666` (ACLs are the real control), skip SIGHUP and POSIX-shell
  editor fixtures, and avoid `.bak` hard-link assertions.
- Two stdin-feedback test fixtures that could drop an `input_reply` when the
  CLI closed the kernel under coverage instrumentation; bounded the runtime
  stdin prompt wait with a timeout.

### Verification

Local `go test -race ./...`, `-covermode=atomic ./...`, Windows test-binary
cross-compilation and `golangci-lint` all pass.

## [0.1.0] - 2026-11-23

First feature-complete release of the Go port against live upstream HEAD
`0abf82d` (v0.8.0 + 5 commits). Closes the code-fixable findings of
`docs/AUDIT-00` … `docs/AUDIT-04`; see `docs/REMEDIATION.md` for the per-finding
statuses and the remaining human-only checks.

### Added

- Full Colab **Public API lifecycle** (LRO create/poll/delete, `runtime_name`
  persisted in session state, operation-code mapping) ahead of upstream, which
  currently only lists runtimes via the Public API.
- `ssh --force` (upstream `cda02bc`) to kill stale sshd children before dialling.
- `logout` (clears keyring/legacy token, invalidates cached proxy tokens and
  capability flags) and recursive `upload/download -r` with traversal/symlink
  guards (upstream PRs #266/#267).
- New Go-only commands: `doctor` (GET-only redacting probe), `audit` (credential
  and update-verification status), `budget` (advisory compute-unit guard),
  `bundle` (reproducible provenance), `sync`, and `agentout`.
- Structured output `--output text|json|ndjson` with the stable `golab/1`
  schema, `--explain` dry-run plans, and `--yes` confirmation for `stop --all`.
- GoReleaser config, cosign + signed-SBOM release workflow, live-e2e skeleton
  with kill switch, QEMU/Termux skeletons, conformance scripts and
  `docs/CONFORMANCE.md`.
- Env-var support: `COLAB_CLI_REQUEST_TIMEOUT`, `COLAB_CLI_ASSIGN_RETRIES`,
  `COLAB_CLI_RETRY_BACKOFF`, `COLAB_OUTPUT`, `COLAB_ASSUME_YES`,
  `GOLAB_BUDGET_FILE`.

### Changed

- Module path is the canonical `github.com/20Youssef10/golab`, enabling
  `go install …@latest` (audit P0-8). CLI and on-disk schema are unchanged.
- One honest client identity `golab/<version>` across control, data and SSH
  planes (audit S-3).
- Token storage keeps only the refresh token + metadata; access tokens are
  minted on demand (audit S-4).
- Small uploads use one atomic `chunk:1` PUT; large uploads report
  `ErrIncompleteUpload` and verify final size (audit S-2).
- `url --open` and `pay`/Drive consent URLs share one cross-platform launcher
  with Termux/WSL support.
- GitHub Actions pinned to commit SHAs; CI adds lint, fuzz, coverage,
  cross-build smoke and safety-script gates.

### Fixed

- `url --open` no-op (audit P0-2); Drive propagation failures echoing raw HTML
  (P0-7); `make cross` writing to a session-local path (P0-4);
  `update.CleanupOld` never called (P0-5); updater identity/asset patterns that
  would reject our own releases (P0-6).
- Redaction covers `id_token`, passwords, secrets and code verifiers; REPL
  sanitiser keeps SGR only; cosign verification requires signed certificate
  timestamps; updater matches signature/SBOM bundles per platform archive.
- All Python defects D-1…D-11 remain fixed; no regressions found.
- Test-fixture race in `TestAuthFlow` made deterministic.

### Verification

`go vet ./...`, `golangci-lint run` (0 issues), `go test -race -count=1 ./...`,
11 `CGO_ENABLED=0` cross-builds, `govulncheck`, `go mod verify`, redaction and
remote-path scans. **Live Colab e2e is still human-gated** per AGENTS.md.

## [0.1.0-alpha.1] - 2026-11-23

- Audit baseline: initial Go port, audit reports AUDIT-00…04, canonical module path.

[Unreleased]: https://github.com/20Youssef10/golab/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/20Youssef10/golab/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/20Youssef10/golab/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/20Youssef10/golab/compare/v0.1.0-alpha.1...v0.1.0
[0.1.0-alpha.1]: https://github.com/20Youssef10/golab/releases/tag/v0.1.0-alpha.1
