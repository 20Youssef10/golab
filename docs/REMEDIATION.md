# Remediation tracker (v0.1.0-alpha.1 → v0.2.0)

> P0/P1 findings completed 2026-11-23; v0.2.0 update 2026-11-26. Status values:
> `FIXED (tests)` means the fix is in the tree and its test passes locally;
> `HUMAN REQUIRED` items are deliberately not closed by an agent (AGENTS.md: no
> live proof, no "done"). Items newly closed in v0.2.0 and the remaining
> human-gated set are listed below.

## P0 findings

| ID | Finding | Status | Verification |
|---|---|---|---|
| P0-1 | `ssh --force` missing while 429 messages recommend it | FIXED (tests) | upstream `test_ssh.py` force cases ported; `golab ssh --help` shows `--force` |
| P0-2 | `url --open` no-op | FIXED (tests) | stubbed launcher test; `url --open` shares launcher with `pay`/Drive |
| P0-3 | Chunked upload may truncate a remote file | FIXED (code) / HUMAN REQUIRED (live) | small-file single PUT, typed `ErrIncompleteUpload`, size check; live SHA-256 check pending |
| P0-4 | `make cross` wrote to session-local `/tmp/opencode` | FIXED | `make -n cross`, `dist/` output, CI smoke job |
| P0-5 | `update.CleanupOld` never called | FIXED | called from `app.New`; idempotent |
| P0-6 | No GoReleaser/release workflow for signed SBOM updater | FIXED | `.goreleaser.yaml` validated; `release.yml` produces cosign + SBOM bundles; identity `20Youssef10/golab`; per-platform asset matching |
| P0-7 | Drive failure echoes raw HTML | FIXED (tests) | upstream `test_drivefs_hook_reports_400_without_raw_html` ported |
| P0-8 | Module path blocked `go install` | FIXED | module `github.com/20Youssef10/golab`; full suite green |

## P1 / P2 findings

| Area | Findings | Status |
|---|---|---|
| CLI | env vars `COLAB_CLI_REQUEST_TIMEOUT`, `COLAB_CLI_ASSIGN_RETRIES`, `COLAB_CLI_RETRY_BACKOFF`; exact messages | FIXED (tests) |
| Missing features | `logout`, recursive `upload/download -r` | FIXED (tests) |
| Protocol | Full Public API LRO lifecycle + `runtime_name` | FIXED (fake-server tests) |
| Security | `id_token`/password redaction, REPL SGR-only CSI, SCT verification, client-agent identity, SSH error redaction | FIXED (tests) |
| Testing | golden/wire fixtures, differential proxy, live e2e, `docs/CONFORMANCE.md` | SCAFFOLDED — live fixtures HUMAN REQUIRED |
| CI/supply chain | pinned Actions, lint/fuzz/goleak/coverage gates, shell scans | FIXED (advisory goleak/coverage gaps documented) |
| Platform | Windows ACL, Termux `faccessat2`, WSL/Termux launchers, QEMU/Termux nightly | PARTIAL — launchers + skeletons fixed; device checks HUMAN REQUIRED |
| Innovations | doctor, audit, budget, bundle, sync, explain, agentout, image caps | FIXED (tests) |

## v0.2.0 update (2026-11-26)

Three items previously held as `HUMAN REQUIRED` are now closed, with recorded
evidence in `CHANGELOG.md` `[0.2.0]` (no new claim is made for the remaining
human-only work):

| Formerly open item | Status | Evidence |
|---|---|---|
| Billable live CPU e2e (one `run`, empty `golab sessions`) | **CLOSED** | `[0.2.0]` Verification: `new`, `status`, `ls`, `exec` (text/JSON/secrets), `run` (script and notebook), `ssh`, piped `repl`, upload/download, `stop --verify`, `resume` error paths and cleanup; final session list empty. |
| Real signed release + self-update verification | **CLOSED** | `[0.2.0]` Verification: release run `45489842618` and CI run `45487165817` green; a real v0.1.9 → v0.2.0 signed self-update installed an executable ELF (see also `[0.1.9]`). Windows `.old` cleanup is still open. |
| Proxy (`HTTP_PROXY`) body-less POST behaviour (upstream #270) | **CLOSED (code)** | `[0.2.0]` Fixed: body-less control-plane POSTs now use chunked transfer instead of `Content-Length: 0`, avoiding HTTP 411 behind forward proxies. No live-proxy proof is claimed. |

Feature/fix set shipped in v0.2.0 (`CHANGELOG.md` `[0.2.0]` and `[Unreleased]`):
second upstream command batch (`exec --json`, `--secret`/`--env-file`,
`--scopes`, `drivemount --wait-for-consent`), `resume` with a bounded msg-id
journal and typed `ErrOutputGap`, notebook `run`, `stop --verify`, bounded
kernel-WS reconnect, and `docs/INSTALL-TERMUX.md`; fixes for CPython
`SystemExit`/error exit codes, assign reconciliation and typed `capacity_unavailable`,
server-assigned shape in `status`, and a quieter routine keyring fallback.
The `[Unreleased]` batch closes the spend-safety, trust-anchor, protocol-integrity,
silent-false-success, file/release-chain, SSH/platform, final gap-report and
live-e2e-found defect sets. Fakes remain fakes: no wire fixture or real-device
claim is added here.

## Human-required items (still open at v0.2.0)

1. `colab auth` / OAuth consent, including `--scopes` mode-change re-consent; interactive-TTY `repl`/`ssh`; `drivemount` browser approval (`--wait-for-consent` polls, but the approval itself is human-only).
2. Live chunked upload of >75 MiB with SHA-256 comparison.
3. GET-only live wire capture for the 8 🔴 items of docs/02 §7.
4. Induced WebSocket drop against a live runtime to exercise `resume`'s `ErrOutputGap` end to end (only fake-server coverage so far).
5. Windows `.old` cleanup on update (the signed release/self-update path itself is closed — see the v0.2.0 update above).
6. Termux real-device and Windows console matrix; the cross-build smoke run was interrupted by a server restart and is not claimed.

## Release

- `v0.1.0-alpha.1` — pushed at remediation start.
- `v0.1.0` — final annotated tag after the original code-fixable findings.
- `v0.2.0` — annotated tag (2026-11-24); signed release and billable live e2e
  recorded in `CHANGELOG.md` `[0.2.0]`.
- Remaining human-gated items are tracked here and in `docs/CONFORMANCE.md`;
  they do not block the v0.2.0 code release but do block any "supported
  platform", "live-proven wire" or "real-device" claim.
