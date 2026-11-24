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

## 4. Release pipeline and updater identity

`.goreleaser.yaml` + `.github/workflows/release.yml` now produce, for every
Tier-1 archive:

| Asset class | Example name | Updater recogniser |
|---|---|---|
| Archive | `golab_0.1.0-alpha.1_linux_amd64.tar.gz` | `AssetPattern` = anchored, version-agnostic `^golab_v?\d+\.\d+\.\d+.*_<GOOS>_<GOARCH>\.tar\.gz$` |
| Checksums | `checksums.txt` | `isChecksumAsset` |
| Signature | `golab_…_linux_amd64.tar.gz.sigstore.json` | `isSignatureAsset` ("sigstore", not "sbom") |
| Signed SBOM | `golab_….sbom.sigstore.json` | `isSBOMAsset` ("sbom" + "sigstore"); predicate `--type cyclonedx` |

- **Identity pin matches this repository:** `internal/app/app.go` defaults
  `Owner=20Youssef10`, `Repo=golab`, and `internal/update/verify.go` builds the
  Fulcio SAN from those values. `git remote` and `GITHUB_REPOSITORY` are also
  `20Youssef10/golab`, and the workflow filename matches (`release.yml`).
  A repository transfer/rename requires updating that pin in `internal/app`.
- **Per-platform bundle selection is implemented:**
  `internal/update/check.go:selectPlatformBundles` matches the signature and
  SBOM bundles to the chosen archive by name, with no "first bundle globally"
  fallback, so each platform verifies against its own subject digest.
- **Archive asset selection is version-agnostic (2026-11-24 fix):**
  `internal/app.UpdateConfig` no longer embeds `version.Number` in
  `AssetPattern` — doing so made a user on vX look for a vX-named asset in a
  newer release and always fail with `ErrNoRelease`. The pattern is now anchored
  to `^golab_v?\d+\.\d+\.\d+.*_<GOOS>_<GOARCH>\.tar\.gz$`; `CurrentVersion`
  stays `version.Number`, so `update.IsNewer` alone gates offers (no downgrade
  or weakening of checksum/signature/SBOM verification). Covered by
  `TestUpdateConfigOffersNewerRelease` against a fake newer release.
- **Post-release proof now covers every artifact class.** The proof step runs
  `sha256sum -c` over all of `checksums.txt`, `cosign verify-blob` for every
  `*.tar.gz.sigstore.json` bundle and `cosign verify-blob-attestation --type
  cyclonedx` for every SBOM bundle, each against its exact archive with the
  pinned identity/issuer; any failure fails the job. cosign v2.6.5's
  `verify-attestation` accepts only an OCI `<IMAGE>` and exposes no `--bundle`
  flag, so blob attestations made by `attest-blob` must be verified with
  `verify-blob-attestation` (whose `--check-claims` defaults to true and checks
  the in-toto subject digest against the archive).
- **Release job is gated by the `release` GitHub environment.** GitHub
  auto-creates it on first use, but repo admins must add required reviewers for
  it to actually gate publishing; until then it is bookkeeping only. Reducing
  the keyless-signing path to a separate `id-token: write`-only job is deferred
  until a live signed release can prove the artifact hand-off and ordering.
- GoReleaser is pinned to **v2.20.3** in all three action steps (was the
  floating `~> v2`), and the proof step asserts `GITHUB_REPOSITORY` equals the
  updater pin `20Youssef10/golab` so a rename/transfer cannot silently publish
  un-installable signed releases. Published signature assets are immutable:
  `gh release upload` no longer passes `--clobber`.
- No signed release has been cut from this pipeline yet. `goreleaser check`, a
  snapshot dry-run and a real signed tag are **human/CI external steps**;
  end-to-end proof that `colab update --install` accepts a signed release is
  still pending. The new proof-step commands have only been shell-syntax and
  local stub checked, not run against real cosign bundling material.

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
5. Cut a signed tag and verify `colab update --install` accepts it end-to-end
   (§4: identity pin and per-platform bundle selection are already in place).
6. Wire goleak `TestMain` + dependency (another agent), then drop `ADVISORY=1`
   from `ci.yml` and raise `COVERAGE_MIN` to 80.
7. Termux device matrix, Windows console/ACL/`.old` cleanup, macOS keychain
   argv/timing, QEMU/Termux job enablement.

## Change log

- 2026-11-24 — v0.1.6 release prepared: CLI version bumped to match the
  changelog, with `TestNumberMatchesLatestChangelogRelease` guarding against a
  future tag carrying a stale binary version. Live signed-tag verification
  pending.
- 2026-11-24 — Self-update could never offer a newer release: `app.UpdateConfig`
  embedded the running version in `AssetPattern`, so a v0.1.4 binary scanning a
  v0.1.5 tag looked for a `golab_0.1.4_…` asset that does not exist and failed
  with `ErrNoRelease`. The pattern is now version-agnostic but anchored and
  platform-specific; `CurrentVersion` (and therefore `IsNewer`) remains the
  no-downgrade guard, and signature/SBOM/checksum verification is untouched.
  `TestUpdateConfigOffersNewerRelease` fails on the old pattern and passes on
  the new one; `go vet ./...` and `go test -count=1 ./...` pass locally. Live
  `colab update` against a real signed tag is still pending (human-only).
- 2026-11-24 — Release proof hardened: every archive signature, every CycloneDX
  SBOM attestation and every checksum line is now verified; GoReleaser pinned to
  v2.20.3; repository-identity and `release`-environment gates added; syft output
  moved to an env var and signature uploads made immutable. `check-redaction.sh`
  now also scans YAML/shell/JSON. Shell syntax, PyYAML parse and local stub runs
  pass; no live signed release was cut, so cosign verification is unproven here.
- 2026-11-24 — Release SBOM step repaired: `download-syft` now pins syft
  v1.57.2 and the scan uses the root-command form (v0.42.2 had no `scan`
  subcommand and could not parse `go 1.27.4`). Verified locally that the
  CycloneDX JSON contains the Go module dependencies (v0.42.2 produced 0
  components). Release not cut; end-to-end signed-tag verification is pending.
- 2026-11-23 — Initial ledger created. Added CI truthfulness gates, release
  pipeline, live-e2e/QEMU/Termux skeletons, conformance scripts and testdata
  skeletons (remediation of AUDIT-02 S-9 and AUDIT-03 P0/P1 findings). No live
  proof; an identity mismatch was recorded at the time, later verified stale and
  corrected on 2026-11-24 (see §4).
