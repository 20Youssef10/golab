# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Breaking changes are defined as changes to the CLI surface, flag semantics,
on-disk schema or persisted token format (per AGENTS.md).

## [Unreleased]

### Fixed

- `whoami`: stop discarding the OAuth transport when extracting the access
  token. The previous code replaced `client.Transport` with a capture
  RoundTripper, so no bearer header was attached and `whoami` always failed with
  `credentials have no access token after refresh` even with valid credentials.
  Verified by `TestAccessTokenFromAuthCapturesBearer` (red before, green after);
  no token is logged or included in errors.

## [0.1.6] - 2026-11-24

### Fixed

- Self-update: `UpdateConfig` no longer embeds the running version in the
  release asset pattern, so a newer GitHub release can actually be offered.
  Version gating stays with `IsNewer`; checksum, signature and SBOM checks are
  unchanged. Verified by `TestUpdateConfigOffersNewerRelease` (red before, green
  after) plus `go vet ./...` and `go test -count=1 ./...`; a live signed-tag
  `colab update` is still human-pending.
- Release workflow: pin syft to v1.57.2 and drop the unsupported `scan`
  subcommand so the signed CycloneDX SBOM contains the Go dependencies
  (v0.42.2 could not parse `go 1.27.4` and produced an empty SBOM).
- CI: force `bash` for the matrix `go test` step so `TEST_FLAGS` expands on
  Windows runners (PowerShell silently dropped `-race`/`-count=1`).
- Docs: correct stale identity and per-platform bundle claims; the updater pin
  matches this repository and bundle selection is implemented.
- Release workflow: the post-release proof now verifies every archive signature
  and every CycloneDX SBOM attestation against its own archive, and checks every
  `checksums.txt` entry (it previously sampled one signature bundle and checked
  neither SBOMs nor checksums).
- Release: v0.1.6 signed release verified (workflow run `45399682610` green) —
  13 archives, 13 signature bundles, 13 CycloneDX attestation bundles and
  `checksums.txt`; a sampled attestation contains 1,627 components including the
  Go modules. The read-only `golab update` check succeeds.
- Release workflow: pin GoReleaser to v2.20.3 instead of floating `~> v2`, assert
  `GITHUB_REPOSITORY` matches the updater's `20Youssef10/golab` pin, and gate the
  release job behind the `release` environment (admins must add required
  reviewers for that gate to take effect).
- Release workflow: pass the syft command through an env var instead of
  interpolating it into `run:`, and drop `--clobber` from `gh release upload` so
  re-runs cannot overwrite published signatures.
- CI: extend `check-redaction.sh` to scan YAML/shell/JSON as well as Go for
  obvious credential literals.

## [0.1.5] - 2026-11-23

### Fixed

- Release workflow: generate the CycloneDX SBOM with the syft CLI directly
  instead of sbom-action (whose pinned version does not support `output-file`
  and whose artifact upload was rejected).

### Verification

YAML parse; CI green on v0.1.4.

## [0.1.4] - 2026-11-23

### Fixed

- Release workflow: give sbom-action an explicit artifact name; the default
  contains a dot and is rejected by GitHub's artifact service.

### Verification

YAML parse, local `go test`; CI green on v0.1.3.

## [0.1.3] - 2026-11-23

### Fixed

- Last Windows-only POSIX mode assertion in the token-store test.
- Release workflow: disable sbom-action's artifact upload (its default name
  contains a dot and is rejected); the SBOM file is still produced for cosign.

### Verification

Local `go test`, YAML parse, `goreleaser check`.

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

[Unreleased]: https://github.com/20Youssef10/golab/compare/v0.1.5...HEAD
[0.1.5]: https://github.com/20Youssef10/golab/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/20Youssef10/golab/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/20Youssef10/golab/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/20Youssef10/golab/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/20Youssef10/golab/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/20Youssef10/golab/compare/v0.1.0-alpha.1...v0.1.0
[0.1.0-alpha.1]: https://github.com/20Youssef10/golab/releases/tag/v0.1.0-alpha.1
