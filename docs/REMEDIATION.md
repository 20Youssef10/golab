# Remediation tracker (v0.1.0-alpha.1 → v0.1.0)

> Completed 2026-11-23. Status values: `FIXED (tests)` means the fix is in the
> tree and its test passes locally; `HUMAN REQUIRED` items are deliberately not
> closed by an agent (AGENTS.md #3: no live proof, no "done").

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

## Human-required items (not closed by this release)

1. `colab auth` / OAuth consent; interactive `repl`/`ssh`; `drivemount`.
2. Live chunked upload of >75 MiB with SHA-256 comparison.
3. GET-only live wire capture for the 8 🔴 items of docs/02 §7.
4. One CPU `run` e2e, ending with an empty `golab sessions`.
5. Real signed release + SBOM verification; Windows `.old` cleanup.
6. Termux device and Windows console matrix.
7. Proxy (`HTTP_PROXY`) body-less POST behaviour (upstream #270).

## Release

- `v0.1.0-alpha.1` — pushed at remediation start.
- `v0.1.0` — final annotated tag after all code-fixable findings above.
- Remaining human-gated items are tracked here and in `docs/CONFORMANCE.md`;
  they do not block the v0.1.0 code release but do block any "supported
  platform" or "live-proven" claim.
