# Conformance ledger — golab vs google-colab-cli 0.8.x+HEAD

> **Date:** 2026-11-23 · **Status:** skeleton ledger, honest accounting.
> Fakes are **not** live proof (AGENTS.md §3). No L5 live pass has occurred, so
> no feature is marked "done". This file is the mandatory ledger required by
> `docs/07-testing-and-conformance.md` §2 and the target of AUDIT-03 P1-1.

## 1. Test-layer status (L1–L7)

| Layer | Requirement (docs/07 §1) | Status | Evidence / gap |
|---|---|---|---|
| **L1 Unit** | every PR, 6 OSes, `-race` | ✅ | ~313 `Test*`/`Fuzz*` functions across 17 packages; `ci.yml` runs `go vet` + `go test -race -count=1` on 6 runners. |
| **L2 Component (fakes)** | fakecolab / fakekernel / fakesshd scenario | 🟡 | All three fakes exist with fault injection; the full `new→exec→upload→download→ls→rm→stop` scenario is split across package tests, not one entry point. |
| **L3 Golden** | fixtures + normalisation + `DELTAS.yaml` + `-update` | 🔴 → 🟡 | `scripts/golden.sh` harness and `testdata/golden/README.md` added; **zero scenario fixtures**. `scripts/golden.sh --scenario wire` secret-scan works once a human supplies `testdata/wire/*.json`. |
| **L4 Differential** | Python ↔ Go via intercepting proxy, ≥40 scenarios | 🔴 → 🟡 | `scripts/differential.sh` skeleton (help/version + normalisation + diff); proxy/CA bootstrap and 38 scenarios are TODO (external tooling + human account). |
| **L5 Live e2e** | real CPU runtime, throw-away account, sweeper | 🔴 → 🟡 | `.github/workflows/live-e2e.yml` added: manual dispatch, `LIVE_E2E_ENABLED=false` default, secrets gate, concurrency 1, GET-only default, 60-min sweeper, final emptiness check. **Never run**; first pass is human-only. |
| **L6 Platform** | QEMU + Termux + devices + Windows consoles | 🟡 | `qemu-smoke.yml` and `termux-nightly.yml` skeletons added (disabled by default); no real runner/device proof. Tier-1 per-OS acceptance (docs/04 §9) has no recorded pass. |
| **L7 Security/fuzz** | redaction, injection, fuzz ≥1 h, goleak, coverage | 🟡 | `ci.yml` now pins actions, runs `golangci-lint`, pinned `govulncheck`, `go mod verify`, both shell gates, 10 s fuzz smoke and a coverage gate. `goleak` wiring is **absent** (no dep / no `TestMain`): `scripts/goleak-check.sh` currently reports FAIL and runs in ADVISORY mode in CI. |

## 2. Upstream-test porting matrix (intent-level, estimate)

Counting: upstream `tests/test_*.py` = 36 files / 373 top-level `def test_`;
our tree = 52 `*_test.go`, ~311 `Test*` + 2 `Fuzz*` = ~313 top-level test
functions. Many Go tests are Go-specific (sigstore, sanitiser, frame decoder)
and do not map 1:1. **Estimate ≈ 200–240 of 373 (~55–65%) intent-level ported.**
Lower-confidence: no per-test ledger existed until this file.

| Upstream group | Upstream tests | Estimated ported | Notes |
|---|---:|---:|---|
| `test_auth` (26) + `test_auth_adc` (9) + `test_local_credentials` (8) | 43 | ~25 | Missing Linux locked-keyring family, cross-OS file-privacy/ACL, chmod-failure tolerance. |
| `test_client` (19) + consumption (4) | 23 | ~15 | XSSI/XSRF/retry/412/portured; fewer fault-matrix cases. |
| `test_runtime` (10) + `test_runtime_options` (5) + `test_streaming` (12) | 27 | ~24 | Strong; idle/dead/reply-after-idle covered. |
| `test_contents` (6) | 6 | 6 | Superset upstream. |
| `test_state` (19) + `test_common` (7) + `test_resolution_logic` (7) + `test_history` (2) | 35 | ~20 | Missing cross-process `kill -9` crash test, RW-lock/sidecar semantics. |
| CLI surface (`test_cli`, `test_exec`, `test_ipynb_exec`, `test_run`, `test_repl`, `test_url`, `test_utils`, `test_cli_log`, `test_whoami`, `test_readme`, `test_version`, `test_pay`, `test_log_export`) | 131 | ~75–85 | Missing `SystemExit` table, URL host/fragment normalisation, log show/filter/export, whoami refresh. |
| `test_automation` (14) | 14 | ~5 | Drive-propagation failures and 400-without-raw-HTML not ported. |
| SSH (7 files: `test_ssh`, `_autocreate`, `_cmd`, `_lifecycle`, `_wire_contract`, `_workdir`, `test_windows_ssh`) | 72 | ~48–52 | Missing stale-sshd kill, most Windows SSH cases, proxy re-invocation cases. |
| `test_update` (28) | 28 | ~0 behavioural | Deliberate deviation: signed GitHub Releases replace PyPI/pip logic (docs/COMPAT.md). |

**Priority groups to port next** (each with the upstream test name in a comment
in the Go test): `test_windows_ssh.py` (5), `test_run.py` exit-code table (23),
`test_cli_log.py`/`test_log_export.py`/`test_readme.py`/`test_whoami.py` (13),
`test_local_credentials.py` (8), `test_automation.py` (14), stale-sshd cases,
`test_state.py` crash-safety (19).

Exit criterion from AUDIT-03 §5 is **≥360 intent-level cases**; we are roughly
two-thirds there. Do not claim release-blocking conformance until the ledger is
filled per-test and the L4/L5 layers have recorded passes.

## 3. Coverage gate (scripts/coverage-gate.sh)

Measured 2026-11-23 with `go test -covermode=atomic -coverprofile`:

| Package | Coverage | docs/07 §11 target |
|---|---:|---:|
| colabapi | 79.6% | 80% |
| runtime | 83.9% | 80% |
| state | 76.0% | 80% |
| sshx | 74.0% | 80% |
| auth | 83.0% | 80% |

CI floors the gate at 70% via `vars.COVERAGE_MIN` until the three under-target
packages are raised. A 100% gate for the error-mapping table is still TODO
(the profile cannot isolate that table).

## 4. Release pipeline and the identity mismatch (ACTION REQUIRED)

`.goreleaser.yaml` + `.github/workflows/release.yml` now produce, for every
Tier-1 archive:

| Asset class | Example name | Updater recogniser |
|---|---|---|
| Archive | `golab_0.1.0-alpha.1_linux_amd64.tar.gz` | `AssetPattern` = `golab_*_<GOOS>_<GOARCH>.tar.gz` |
| Checksums | `checksums.txt` | `isChecksumAsset` |
| Signature | `golab_…_linux_amd64.tar.gz.sigstore.json` | `isSignatureAsset` ("sigstore", not "sbom") |
| Signed SBOM | `golab_….sbom.sigstore.json` | `isSBOMAsset` ("sbom" + "sigstore"); predicate `--type cyclonedx` |

- **MISMATCH to resolve by the owner / updater agent (do not edit
  `internal/update` here):** `internal/app/app.go` defaults `Owner=golab-cli`,
  `Repo=golab`, so `verify.go` pins the Fulcio SAN to
  `https://github.com/golab-cli/golab/.github/workflows/release.yml@…`. This
  repository is `20Youssef10/golab`; its OIDC SAN is
  `https://github.com/20Youssef10/golab/.github/workflows/release.yml@…`.
  **`colab update` will reject our own signed releases** until either the
  defaults are changed to `20Youssef10`/`golab` or the repo is transferred/
  renamed to `golab-cli/golab`. The workflow filename already matches
  (`release.yml`) — that part is correct.
- **Second mismatch (updater logic):** `check.go` picks the *first* signature
  and *first* SBOM asset globally, but a multi-platform release has one of each
  per archive with different subject digests. Per-artifact selection must be
  implemented in `internal/update` (owned by another agent) for non-matching
  platforms to verify. Bundles are currently produced per archive.
- No release has been cut. `goreleaser check` / a snapshot dry-run and a real
  signed tag are **human/CI external steps** (GoReleaser, cosign and syft are
  not installed in this sandbox).

## 5. WSL / Termux URL-launcher expectations (docs/04 §2)

**Expected:** on WSL open with `wslview`, then `explorer.exe`, then print the
URL; on Termux use `termux-open-url`; elsewhere `xdg-open` / macOS `open` /
Windows `rundll32 url.dll,FileProtocolHandler`. Headless environments must
fall back to printing the URL with a clean stdout when piped.

**Current:** `internal/cli/utility_extra.go` implements only the three
non-WSL/non-Termux launchers. On headless WSL/Termux `openURL` returns false and
the caller prints the URL — a **degraded path, not a crash**. The Go-side fix
(`platform.IsWSL`, `termux-open-url`, injectable `exec` seam, table tests) is
owned by another agent; do not duplicate it here. Track it as an open L6 gap.

## 6. Human-only / external steps blocking "done"

1. Live `auth` + OAuth consent; interactive `repl`/`ssh`; `drivemount`.
2. GET-only wire capture for the 8 🔴 items (`scripts/live-capture.sh`), then
   `bash scripts/golden.sh --scenario wire`.
3. Chunked upload of >75 MiB with SHA-256 comparison (S6 / P0-3, HIGH).
4. One CPU `new`/`run`, then `colab sessions` shows nothing (live-e2e manual run).
5. Resolve the release identity mismatch (§4) and cut a signed tag; verify
   `colab update --install` accepts it.
6. Wire goleak `TestMain` + dependency (another agent), then drop `ADVISORY=1`
   from `ci.yml` and raise `COVERAGE_MIN` to 80.
7. Termux device matrix, Windows console/ACL/`.old` cleanup, macOS keychain
   argv/timing, QEMU/Termux job enablement.

## Change log

- 2026-11-23 — Initial ledger created. Added CI truthfulness gates, release
  pipeline, live-e2e/QEMU/Termux skeletons, conformance scripts and testdata
  skeletons (remediation of AUDIT-02 S-9 and AUDIT-03 P0/P1 findings). No live
  proof; identity mismatch recorded for the owner.
