# Conformance ledger — golab vs google-colab-cli 0.8.x+HEAD

> **Date:** 2026-11-27 · **Status:** ledger with one recorded billable CPU live
> pass, honest accounting.
> Fakes are **not** live proof (AGENTS.md §3). A billable CPU e2e pass on
> 2026-11-26/27 is recorded (§6), but it did not cover OAuth, Drive, interactive
> TTY, real devices, the GCP quota project or >75 MiB uploads; no feature is
> marked "done" on the strength of fakes alone. This file is the mandatory
> ledger required by `docs/07-testing-and-conformance.md` §2 and the target of
> AUDIT-03 P1-1.

## 1. Test-layer status (L1–L7)

| Layer | Requirement (docs/07 §1) | Status | Evidence / gap |
|---|---|---|---|
| **L1 Unit** | every PR, 6 OSes, `-race` | ✅ | 664 `Test*`/`Fuzz*` functions in 103 `_test.go` files across 23 packages (25 total in `go list ./...`); `ci.yml` runs `go vet` + `go test -race -count=1` on 6 runners. |
| **L2 Component (fakes)** | fakecolab / fakekernel / fakesshd scenario | 🟡 | All three fakes exist with fault injection; the full `new→exec→upload→download→ls→rm→stop` scenario is split across package tests, not one entry point. |
| **L3 Golden** | fixtures + normalisation + `DELTAS.yaml` + `-update` | 🔴 → 🟡 | `scripts/golden.sh` harness and `testdata/golden/README.md` added; **zero scenario fixtures**. `scripts/golden.sh --scenario wire` secret-scan works once a human supplies `testdata/wire/*.json`. |
| **L4 Differential** | Python ↔ Go via intercepting proxy, ≥40 scenarios | 🔴 → 🟡 | `scripts/differential.sh` skeleton (help/version + normalisation + diff); proxy/CA bootstrap and 38 scenarios are TODO (external tooling + human account). |
| **L5 Live e2e** | real CPU runtime, throw-away account, sweeper | 🟡 | `.github/workflows/live-e2e.yml` has manual dispatch, `LIVE_E2E_ENABLED=false` default, secrets gate, concurrency 1, GET-only default, 60-min sweeper and a final emptiness check. A billable CPU pass ran 2026-11-26/27 (allocation, `new`/`run`, VM inspection, detach, `reap`, `cost`, GET-only trace capture, sweeper; final `golab sessions` empty) and found/fixed two Tier 2 path/decoding bugs. Not a full matrix: see §6. |
| **L6 Platform** | QEMU + Termux + devices + Windows consoles | 🟡 | `qemu-smoke.yml` and `termux-nightly.yml` skeletons added (disabled by default); no real runner/device proof. Tier-1 per-OS acceptance (docs/04 §9) has no recorded pass. |
| **L7 Security/fuzz** | redaction, injection, fuzz ≥1 h, goleak, coverage | 🟡 | `ci.yml` now pins actions, runs `golangci-lint`, pinned `govulncheck`, `go mod verify`, both shell gates, 10 s fuzz smoke and a coverage gate. `goleak` is **wired and gating**: `go.uber.org/goleak` is a direct dependency, `runtime`/`sshx`/`repl` use `VerifyTestMain` and `update` uses `VerifyNone`, and `scripts/goleak-check.sh` is a hard CI gate. |

## 2. Upstream-test porting matrix (intent-level, estimate)

Counting: upstream `tests/test_*.py` = 36 files / 373 top-level `def test_`;
our tree = 103 `*_test.go` files with 664 top-level `Test*`/`Fuzz*` functions
across 23 packages (25 total in `go list ./...`). Many Go tests are Go-specific
(sigstore, sanitiser, frame decoder) and do not map 1:1.
**Estimate ≈ 200–240 of 373 (~55–65%) intent-level ported.**
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

Measured with `make coverage` (`go test -covermode=atomic -coverprofile`) and
aggregated by `scripts/coverage-gate.sh`:

| Package | Coverage | docs/07 §11 target | Gated? |
|---|---:|---:|---|
| colabapi | 82.75% | 80% | yes |
| runtime | 84.55% | 80% | yes |
| state | 77.50% | 80% | yes |
| sshx | 74.17% | 80% | yes |
| auth | 82.93% | 80% | yes |
| platform | 31.6% | 80% | yes (ratchet floor, no-regression) |
| app | 47.2% | 80% | yes (ratchet floor, no-regression) |

Total coverage is **75.8%**. `scripts/coverage-gate.sh` checks the five
`docs/07 §11` target packages at the 80% target and additionally ratchets
`platform` and `app` with floors just below their measured values (a
no-regression gate; `COVERAGE_MIN_<pkg>` still overrides). CI floors the original
five at 70% via `vars.COVERAGE_MIN` until the under-target packages (`state`,
`sshx`) reach 80%. A 100% gate for the error-mapping table is still TODO (the
profile cannot isolate that table).

## 4. Release pipeline and updater identity

`.goreleaser.yaml` + `.github/workflows/release.yml` now produce, for every
Tier-1 archive:

| Asset class | Example name | Updater recogniser |
|---|---|---|
| Archive | `golab_0.1.0-alpha.1_linux_amd64.tar.gz` | `AssetPattern` = anchored, version-agnostic `^golab_v?\d+\.\d+\.\d+.*_<GOOS>_<GOARCH>\.tar\.gz$` |
| Checksums | `checksums.txt` | `isChecksumAsset` |
| Signature | `golab_…_linux_amd64.tar.gz.sigstore.json` | `isSignatureAsset` ("sigstore", not "sbom") |
| Signed SBOM | `golab_….sbom.sigstore.json` | `isSBOMAsset` ("sbom" + "sigstore"); predicate `--type https://cyclonedx.org/schema/bom-1.5` (exact URI, not the `cyclonedx` alias) |

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
  https://cyclonedx.org/schema/bom-1.5` for every SBOM bundle, each against its
  exact archive with the pinned identity/issuer; any failure fails the job.
  cosign v2.6.5's `verify-attestation` accepts only an OCI `<IMAGE>` and exposes
  no `--bundle` flag, so blob attestations made by `attest-blob` must be
  verified with `verify-blob-attestation` (whose `--check-claims` defaults to
  true and checks the in-toto subject digest against the archive). A format
  guard also asserts every bundle is v0.3 protobuf (`.mediaType` contains
  `v0.3` and `.verificationMaterial.tlogEntries[0].inclusionProof` exists),
  because the cosign commands accept legacy bundles that the updater rejects.
  A `jq` guard decodes each SBOM bundle's v0.3 DSSE payload
  (`(.dsseEnvelope.payload // .content.dsseEnvelope.payload) | @base64d |
  fromjson`) and requires `.predicateType` to equal the exact URI above,
  because cosign's verifier also accepts the `cyclonedx` alias whose emitted
  predicate the updater rejects. The two-path read is deliberate: cosign
  v2.6.5 writes `dsseEnvelope` at the bundle root (see the v0.1.8 note below).
- **Release job is gated by the `release` GitHub environment.** GitHub
  auto-creates it on first use, but repo admins must add required reviewers for
  it to actually gate publishing; until then it is bookkeeping only. The
  keyless-signing path now runs in a separate `sign` job holding the only
  `id-token: write` permission; the `build` job has no signing identity and
  hands off `dist/` as an Actions artifact. Both jobs remain in the same
  `release.yml` file so the pinned certificate SAN is unchanged. A live tagged
  release is still required to prove the artifact hand-off and Fulcio identity
  end to end (see the v0.2.0 note below).
- GoReleaser is pinned to **v2.20.3** in all three action steps (was the
  floating `~> v2`), and the proof step asserts `GITHUB_REPOSITORY` equals the
  updater pin `20Youssef10/golab` so a rename/transfer cannot silently publish
  un-installable signed releases. Published signature assets are immutable:
  `gh release upload` no longer passes `--clobber`.
- **v0.1.6 was cut from this pipeline and cosign-verified, but its updater was
  BROKEN (2026-11-24).** Release run `45399682610` is green: `goreleaser check`,
  snapshot dry-run, signed tag, syft SBOM, per-artifact cosign verification and
  the checksum gate all ran against real material. The release carries 13
  archives, 13 signature bundles, 13 CycloneDX attestation bundles and
  `checksums.txt`. A sampled attestation is CycloneDX 1.7 with 1,627 components
  (including the Go modules) and is bound to its exact archive. The read-only
  `golab update` discovery against the published release offered the new
  version. **However, `golab update --install` against v0.1.6 fails** with
  `unknown field "base64Signature": invalid signature`: cosign v2.6.5 wrote
  LEGACY bundles (`base64Signature`, `cert`, `rekorBundle`), while
  `internal/update/verify.go` parses only sigstore v0.3 protobuf bundles
  (`mediaType: application/vnd.dev.sigstore.bundle.v0.3+json`). The workflow's
  cosign proof passed because `verify-blob`/`verify-blob-attestation` auto-detect
  both formats, so it could not detect the mismatch. **Fix:** both `sign-blob`
  and `attest-blob` now pass cosign v2.6.5's `--new-bundle-format`, and the
  post-release proof asserts v0.3 `mediaType` plus a tlog `inclusionProof` on
  every bundle with `jq` before the cosign checks. Release assets are immutable,
  so v0.1.6 cannot be repaired in place: a **new tag** is required to prove the
  updater end-to-end (§6 item 5). No `verify.go` change was needed or made.

- **v0.1.7 was signed but STILL could not be installed (2026-11-24).** Release
  run `45414858634` is green: the v0.3 bundle fix worked (every `.sigstore.json`
  has mediaType `application/vnd.dev.sigstore.bundle.v0.3+json` plus a tlog
  `inclusionProof`) and cosign signature verification passed. `colab update
  --install`, tried from a v0.1.6-labelled binary, then failed with `SBOM
  attestation has an unsupported predicate type: invalid signature`. Root cause:
  `cosign attest-blob --type cyclonedx` (v2.6.5) emits in-toto predicateType
  `https://cyclonedx.org/bom`, while `internal/update/sbom.go:18-28` accepts only
  `https://cyclonedx.org/schema/bom` or the `https://cyclonedx.org/schema/bom-*`
  family. **Fix (producer only):** `attest-blob` now uses
  `--type https://cyclonedx.org/schema/bom-1.5` (cosign copies a URI verbatim
  into the DSSE statement), the post-release `cosign verify-blob-attestation`
  uses the same URI, and a `jq` guard asserts the emitted predicateType. The
  verifier is deliberately unchanged: this exact URI is chosen because it is
  already accepted by the unmodified v0.1.6/v0.1.7 binaries, so either can
  install the next release. Published assets are immutable, so a **new tag** was
  required; v0.1.9 later supplied that proof (see the verified entry below).

- **v0.1.8 was published with correct artifacts but its workflow FAILED at the
  post-release proof guard (2026-11-24).** Release run `45428451505` stopped at
  the `jq` predicate check with
  `jq: error (at dist/golab_0.1.8_darwin_amd64.tar.gz.sbom.sigstore.json:0):
  Malformed BOM`. The guard read `.content.dsseEnvelope.payload`, but
  `cosign attest-blob --new-bundle-format` (cosign v2.6.5) writes the DSSE
  envelope at the **root** of a v0.3 bundle: top-level keys are `mediaType`,
  `verificationMaterial`, `dsseEnvelope`. I downloaded the published
  `golab_0.1.8_linux_amd64.tar.gz.sbom.sigstore.json` and confirmed the root
  `.dsseEnvelope.payload` decodes (base64) to an in-toto statement whose
  `predicateType` is exactly `https://cyclonedx.org/schema/bom-1.5`
  (`payloadType` is `application/vnd.in-toto+json`): **the release artifacts
  are correct; only the guard's path was wrong.** Fix: the guard now accepts
  either layout via
  `(.dsseEnvelope.payload // .content.dsseEnvelope.payload) | @base64d |
  fromjson | .predicateType == $pt`, keeping `--arg pt`, the assertion and the
  loud FAIL message unchanged. The run did not go green and published assets
  are immutable, so a **new tag** was required; no green v0.1.8 release is
  claimed.

- **v0.1.9 verified end-to-end (2026-11-24).** Release run `45446096192` is
  green with all guards: 13 archives, 13 signature bundles, 13 signed SBOM
  attestations and `checksums.txt`. `internal/update/install.go` now extracts
  the single `golab` binary from the *verified* `.tar.gz` before replacing the
  executable (path/type/size checks; signature remains bound to archive bytes).
  A v0.1.8-labelled binary ran `golab update --install --yes` against the real
  v0.1.9 release: it discovered the newer version, verified signature + SBOM,
  installed, and the resulting file is a statically linked ELF (`7f 45 4c 46`)
  that executes as `golab 0.1.9`; `golab doctor` also runs. No fixtures or fake
  servers were used for this check. The host's Termux resolver picked an
  unreachable GitHub address during two attempts; pinning `github.com` to a
  reachable address in `/etc/hosts` (restored afterward) was required — an
  environment workaround, not a product defect. The globally installed
  `/usr/local/bin/golab` was rebuilt from this commit and reports 0.1.9.

## 5. WSL / Termux URL-launcher expectations (docs/04 §2)

**Expected:** on WSL open with `wslview`, then `explorer.exe`, then print the
URL; on Termux use `termux-open-url`; elsewhere `xdg-open` / macOS `open` /
Windows `rundll32 url.dll,FileProtocolHandler`. Headless environments must
fall back to printing the URL with a clean stdout when piped.

**Current:** the gap is **CLOSED** (2026-11-26). `browserLaunchCommand` in
`internal/cli/utility.go:271-287` selects all five launchers: `rundll32
url.dll,FileProtocolHandler` on Windows, `termux-open-url` when present, `open`
on macOS, `wslview` on WSL, and `xdg-open` elsewhere. WSL is detected by a
read-only `/proc/version` probe (`detectWSL`), and launcher choice is covered by
`TestBrowserLaunchCommandSelection` in `internal/cli/utility_test.go`. On a
headless host with no launcher available, `launchSystemBrowser` returns false
and the caller prints the URL — a **degraded path, not a crash**. Real
WSL/Termux device execution remains an open L6 human-only proof (§6 item 7).

## 6. Human-only / external steps blocking "done"

Statuses are from the billable CPU e2e on **2026-11-26/27** and earlier recorded
runs. "Closed" applies to the specific check named, not to the whole layer.

1. **Still human-only.** Live `auth` + OAuth re-consent, `drivemount` browser
   approval, and interactive-TTY `repl`/`ssh`. The 2026-11-26/27 CPU pass did
   not exercise any of these; piped `repl` and non-interactive `ssh -- CMD` are
   different paths. Upstream issue #289's re-consent/quota-project prerequisites
   are diagnosed locally but stay human-controlled.
2. **Partly closed.** A GET-only wire/trace capture was performed during the
   2026-11-26/27 pass. **Still open:** upstream Python wire
   fixtures — `testdata/wire/` and `testdata/golden/` still contain only READMEs
   (zero fixtures), so `bash scripts/golden.sh --scenario wire` has nothing to
   secret-scan and L3/L4 remain scaffolding. Redacted ground truth must still
   be captured from the official Python CLI by a human.
3. **Still human-only.** Chunked upload of >75 MiB with SHA-256 comparison
   (S6 / P0-3, HIGH). The live pass covered ordinary upload/download only.
4. ✅ **Closed 2026-11-26/27.** A CPU `new`/`run` executed on a billable VM;
   `reap` removed a real orphan and the final `golab sessions` was empty. The
   pass also exercised `wait`, VM inspection (`ps`/`cat`/`tail`/`grep`/`du`/
   `find`/`fetch`), `exec` with secrets, `run --json` and `--detach`, `jobs`,
   `logs`, `cost`, config/init/state/doctor/trace/compat/update commands and the
   Public API quota-project failure path. It found and fixed two Tier 2 bugs
   (`du` array-shaped `lines`; relative paths resolving against kernel cwd
   `/content` instead of VM root), both re-verified live.
5. ✅ **Closed for v0.1.9 and v0.2.0** (release runs `45446096192` /
   `45489842618`; a real signed self-update installed an executable ELF; see §4
   and `CHANGELOG.md` `[Unreleased]` Verification). v0.1.6/v0.1.7 failed this
   check and v0.1.8 never reached it; those failures are recorded in §4.
   **Still non-waivable and human-only for each future tag:** a live tagged
   signed-release install proof, now covering the reworked `build`
   (`contents: read` only) → `sign` (`contents: write` + `id-token: write`)
   artifact hand-off. `golab-mcp` is not in `.goreleaser.yaml`, so it cannot be
   validated through that path. No new tag was cut.
6. ✅ **Done.** goleak is a hard CI gate (`runtime`/`sshx`/`repl` `VerifyTestMain`,
   `update` `VerifyNone`, `scripts/goleak-check.sh`), `ADVISORY=1` was removed,
   and the coverage gate is enforced via `COVERAGE_MIN` (floor 70, target 80).
   The 2026-11-26/27 pass confirmed both gates; `state`/`sshx` remain below the
   80% target, as recorded in §3.
7. **Still human-only.** Termux device matrix (DNS/CA/`faccessat2`), Windows
   console/ACL/`.old` cleanup, macOS keychain argv/timing and notarisation, and
   QEMU/Termux job enablement. No real-device or per-OS §9 acceptance pass is
   recorded.
8. **Still human-only.** Live GCP quota-project setup for the Public API and a
   real Device Code/OAuth re-consent flow; `golab doctor --section api` and
   hidden `test-public-api` only read local signals — they do not provision or
   prove GCP-side state.
9. **Still human-only.** A live signed-release tag proof that includes
   publishing and verifying a release built with the new least-privilege `build`
   / `sign` split (see item 5).

## Change log

- 2026-11-27 — **Flagged follow-ups closed honestly; no new live proof claimed
  for fake-tested surfaces.** The `ssh --accept-host-key SHA256:…` flag pins a
  host key only after the handshake matches (explicit pin, mismatch = exit 2
  before allocation); `compat refresh [--out FILE] [--ref REF]` writes a
  provenance-tagged, explicitly partial snapshot from the official upstream
  Typer source and can be consumed by `compat report --snapshot FILE`; the cost
  ledger's read-modify-rename cycle is serialized across processes with a
  per-path flock; detached `--ttl` is now **remotely enforced** by a 5-second
  watchdog (`exit=124` / `ttl-reason=ttl_exceeded`, ADR-001 amended) while
  `--detach` + explicit `--max-cu`/`--enforce` is rejected with exit 2; the
  release `build` job is `contents: read` only and `sign` alone holds
  `contents: write` + `id-token: write`; and packaging configs were added under
  `packaging/` (winget blocked pending a `.zip` repack, none live-proven here).
  New surfaces: `cmd/golab-mcp` (official `modelcontextprotocol/go-sdk`, 22
  `colab_*` tools with `confirm: true` gating, `golab://docs/skill` and
  `golab://docs/usage` resources, stdio and streamable HTTP at `/mcp` and `/`,
  no `/sse`, legacy `2025-06-18` negotiation tested) and two portable Agent
  Skills (`.agents/skills/golab-operations`, `.agents/skills/colab-mcp`), both
  passing `agentskills validate`. **Caveats:** `golab-mcp` is not in
  `.goreleaser.yaml` and has no signed-release/self-update proof; the skills,
  MCP tools, compat refresh, host-key pin and cost/detach changes are
  static/fake-tested only; the doctorfix fix was a clock-dependent test fixture,
  not a CLI behaviour change; the 2026-11-26/27 billable CPU pass closed the
  items marked ✅ in §6 but did not cover OAuth/Drive/TTY/devices/quota project/
  >75 MiB. See `CHANGELOG.md` `[Unreleased]` and [USAGE.md](USAGE.md).

- 2026-11-26 — **Six feature tiers landed on `main`; all are fake-tested only and
  none has live proof.** Tier 1 adds spend safety (`reap`, `new --ttl` /
  `--ttl-action`, `--wait-capacity`, `cost`, `run --max-cu --enforce`):
  `ccu-info` is account-level, TTL is a local check with no daemon. Tier 2 adds
  VM inspection (`ps`/`top`, `cat`, `tail`, `grep`, `du`, `find`, `fetch`) via
  server-side polling; `tail -f` is not byte-perfect under rotation. Tier 3 adds
  detached runs (`run --detach --ttl`, `jobs`, `logs ID [-f]`, `wait`,
  `sync --watch`): the job is remote, there is no local daemon, the VM stays
  allocated, and `--ttl` is mandatory but advisory — the decision and non-goals
  are recorded in [ADR-001](adr-001-detached-runs.md). **Superseded 2026-11-27:**
  `--ttl` is now remotely enforced by a watchdog (see the 2026-11-27 entry
  above). Tier 4 adds reproducibility
  (`env freeze`, `run --lockfile`, `-p`/`--params-file`, `run --bundle`,
  `bundle verify --env`); metadata-only verification is RECORDED, only `--env`
  executes in a fresh VM. Tier 5 adds ergonomics (`config`, `init`, `use` /
  `GOLAB_SESSION`, `status --watch`, `log -f`, `run --json/--secret/--env-file/
  --output-image`). Tier 6 adds honesty tooling (`GOLAB_TRACE` / `trace -- ...`
  with keys-only queries and an `--unsafe-bodies` opt-in, `compat report` against
  an embedded self-snapshot, `doctor --fix` with a dry-run default,
  `state migrate` preserving unknown fields, `update --verify-only PATH`, and
  `install.sh` / `install.ps1` with checksum + cosign verification). New
  fake-tested gate packages (`cost`, `vmstat`, `detach`, `envlock`, `nbparams`,
  `compat`, `doctorfix`, `trace`) exist; the coverage gate still checks the
  original seven. **At landing, L3/L4/L5 were unproven for these commands**:
  a human still needs the remaining e2e, including a detached-job audit —
  **now updated** to check remote TTL enforcement (`exit=124` /
  `ttl-reason=ttl_exceeded`) and to stop the VM manually, before any tier is
  called done. The deadline-expiry branch itself is still unproven live. See §6 and [USAGE.md](USAGE.md).

- 2026-11-26 — Reviewed upstream issue #289 (`googlecolab/google-colab-cli`).
  Upstream plans a 1.0 release candidate in ~2 weeks whose breaking change is a
  switch to Colab's Public API; users will have to re-run `colab auth` and set a
  GCP quota project, and 1.0 adds instructions plus a `colab init-gcp-project`
  helper. Short-term opt-outs are staying on 0.8.0 or `--no-public-api`, but the
  tunnel API will likely be turned down soon to prevent abuse. **Already covered
  in golab:** the full Public API lifecycle (LRO create/poll/delete, persisted
  `runtime_name`), `--public-api` / `--no-public-api`, `COLAB_CLI_PUBLIC_API`,
  hidden `test-public-api`, `logout`, WS reconnect/resume and bounded timeouts.
  **Readiness landed later in the same round:** quota-project / re-consent
  diagnostics in `doctor --section api` / `test-public-api` (local signals only,
  no GCP mutation), migration docs for the re-auth + quota-project prerequisite,
  and `golab attach --runtime NAME [--session NAME]` for browser-created runtimes.
  There is deliberately no `init-gcp-project` helper — GCP provisioning and
  browser consent stay human-controlled. **Not claimed:** golab does not claim upstream 1.0 support,
  and the CLI default stays the tunnel API — `version.Upstream` remains
  `0.8.x+HEAD` and this ledger still targets 0.8.x+HEAD. No default flip or 1.0
  support claim without live proof; tunnel shutdown would remove the opt-out, so
  the Public API path needs live verification before it can become the default.
- 2026-11-26 — Ledger reconciled against `main` and fresh measurements: test
  counts (§1), coverage numbers (§3) and the URL-launcher gap (§5) are updated.
  The `docs/gap-report.md` P0–P3 work recorded in `CHANGELOG.md` `[Unreleased]`
  is on `main`: spend safety (compensating `Unassign`, honest `stop --all`,
  pre-allocation script read, fail-closed budget guard, `max-balance` direction,
  named profiles), trust anchors (pinned token endpoint/client ID, `refs/tags/v`
  release SAN), resume/protocol integrity (bounded journal and worker, single
  interrupt, create reconciliation, stable `requestId`, hardware reattach
  check), silent-false-success and response-integrity fixes, and
  file/release-chain and SSH/platform hardening. All are covered test-first
  with fakes; **no live proof is claimed for any fake-tested item**. The
  still-open human-only proofs in §6 are: live billable re-verification of the
  fixed cases, OAuth consent / Drive approval / interactive TTY, chunked
  >75 MiB upload, the WSL/Termux/device and
  wire-capture matrices. The tagged-release install acceptance is recorded as
  closed for v0.1.9 (§4) and v0.2.0 (`CHANGELOG.md` `[Unreleased]` Verification);
  it must be rerun for each future tag.
- 2026-11-25 — Post-v0.2.0 audit and live e2e: code review, security audit and
  billable CPU probes found six client defects in the batch 2 features (JSON
  `SystemExit(0)` record, plaintext `--env-file` history leak for ordinary key
  names, `--secret oauth_token` cross-plane risk, missing notebook
  `execution_count`, `]` in image paths, and Drive consent URI injection plus
  polling/reader edge cases, plus IPython's exit-hint warning in JSON stderr).
  All seven are fixed test-first with fake-kernel
  coverage; live billable re-verification follows in the same working round.
  v0.2.0 remains published — these fixes are on `main` in `[Unreleased]` with no
  patch tag, per requester decision. `run --explain <script> --explain` is not a
  bug: interspersed flags are disabled after `SCRIPT`, so the trailing flag is
  forwarded to the script by design. Coverage `state`/`sshx` stay below the 80%
  target (CI floor 70%); cross-build smoke was interrupted by a server restart
  and is not claimed.
- 2026-11-24 — v0.2.0 release prepared: CLI version bumped to match the
  changelog (guarded by `TestNumberMatchesLatestChangelogRelease`); MINOR bump
  for the new CLI surface from both upstream batches. Signed tag, release
  workflow and live e2e proof are pending; earlier batch records are below.
- 2026-11-24 — Second upstream batch implemented: `exec --json` (PR #237),
  local-only `--secret`/`--env-file` bridge (issue #157), `--scopes full|minimal`
  with forced re-consent (issue #71), `drivemount --wait-for-consent` (issue #258),
  and a scoped #220 implementation (bounded msg-id journal, typed `ErrOutputGap`,
  no teardown on transport errors, and `colab resume`). All are test-first with
  fakes; `go vet`, full `go test -count=1 ./...`, golangci-lint and both safety
  scripts pass locally. Human-only: OAuth re-consent, Drive browser approval,
  and a live billable VM drop/recovery proof. **PR #276 (maintainer notices) is
  deferred/rejected:** upstream's unsigned, user-redirectable `notices_url` feed
  would create an unauthenticated push channel into every command's stderr,
  contradicting the fork's pinned signed-update trust model; the signed release
  banner already covers security-fix delivery. Reopen only with a signed feed
  under the pinned release-workflow identity and a demonstrated need. The next
  release remains MINOR (0.2.0); no tag was cut.
- 2026-11-24 — Upstream triage (`googlecolab/google-colab-cli`: 18 open issues,
  19 open PRs) completed and first fork batch implemented. Ported bug fixes:
  body-less POST chunked encoding (#270), final assign reconciliation (#263),
  typed `capacity_unavailable` for assign 503 (#282/#273/#236), `exec` exit-code
  semantics (#278/#280), and server-shape overlay in `status` (#47). Ported
  features: `.ipynb` input for `run` (#74/#200), `stop --verify` (#47), bounded
  kernel WS reconnect (#261/#220) and a Go-specific Termux guide (#131). All
  changes are covered by test-first fake-server tests; `go vet`, full
  `go test -count=1 ./...`, golangci-lint v2.15.0 and both safety scripts pass
  locally. Live billable/OAuth/TTY proofs remain human-only. Deferred by design:
  full output recovery + `resume` (#220, needs a journal design spike),
  `exec --json` (#237, must follow the new exit semantics), `--scopes full|minimal`
  (#71, needs human re-consent), a local secrets bridge (#157, security review),
  click-free Drive mount (#258, Google consent is interactive), and
  maintainer-pushed notices (PR #276, needs a signed feed). Windows/PR #100 and
  Python packaging issues #81/#100 are not applicable to a static Go binary.
  The next release containing these additions is MINOR (0.2.0); no tag was cut.
- 2026-11-24 — Small user-visible fix: the routine OS-keyring fallback no longer
  prints `[colab] OS keyring unavailable; using owner-only token file …` on
  stderr for every command. `internal/auth/tokenstore.go` `tieredStore.notice`
  still writes a redacted `oauth2: keyring tier unavailable` debug log entry on
  every fallback, and now prints the human-facing hint only when debug logging
  is on (`COLAB_DEBUG=1`, the only debug switch — there is no `--debug` flag).
  `golab audit` continues to report the tier and the token file mode, so the
  diagnosis is not lost. Genuine auth failures (corrupt cached token, refresh
  failure, save failure) are untouched: they still go through
  `oauth2Provider.notice` and are visible by default. RED→GREEN: four auth tests
  (`TestKeyringProbeTimeoutIsCached`,
  `TestTieredStoreSaveFallbackQuietByDefault`,
  `TestKeyringFallbackNoticeOnlyInDebug`,
  `TestOAuth2KeyringInaccessibleFallsBackToFile`) failed on the old behavior
  and pass after the change; an assertion was added to
  `TestOAuth2CorruptStoredTokenFallsBackToRemoteFlow` to prove genuine notices
  are not silenced. Verification: focused auth tests, `go vet ./...`,
  `go test -count=1 ./...`, golangci-lint (0 issues),
  `bash scripts/check-redaction.sh` and `bash scripts/check-paths.sh` all pass
  locally with Go 1.27.4 (`GOTOOLCHAIN=local`, no `-race` on this host). No live
  test, release or tag is claimed.
- 2026-11-24 — v0.1.9 tagged and **verified end-to-end**: CLI version bumped to
  match the changelog (guarded by `TestNumberMatchesLatestChangelogRelease`);
  release run `45446096192` green; a real v0.1.8 → v0.1.9 signed self-update
  installed an executable ELF that reports `golab 0.1.9` and runs `doctor`.
  Earlier live defects and their fixes are recorded below.
- 2026-11-24 — **v0.1.8's release workflow FAILED (run `45428451505`)** at the
  new post-release CycloneDX `predicateType` guard, with
  `jq: … Malformed BOM`, even though the published attestation was correct.
  Root cause: cosign v2.6.5 v0.3 bundles from `attest-blob` put `dsseEnvelope`
  at the bundle root (`mediaType`, `verificationMaterial`, `dsseEnvelope`),
  while the guard read `.content.dsseEnvelope.payload`. Verified against the
  downloaded published `golab_0.1.8_linux_amd64.tar.gz.sbom.sigstore.json`:
  root `dsseEnvelope.payload` decodes to an in-toto statement with
  `predicateType = https://cyclonedx.org/schema/bom-1.5`. Fix: guard accepts
  root or nested layout; the assertion is unchanged (§4). No green v0.1.8
  release or working self-update is claimed — a separate install-path defect
  was fixed and is now proved by v0.1.9 (§4); a new tag was required.
- 2026-11-24 — v0.1.8 prepared: CLI version bumped to match the changelog
  (guarded by `TestNumberMatchesLatestChangelogRelease`) alongside the exact
  CycloneDX predicate URI fix. A signed tag and a real `colab update --install`
  proof are pending; both prior live defects are recorded below.
- 2026-11-24 — **CONFIRMED DEFECT in v0.1.7, producer fixed in this change.**
  v0.1.7 was signed (release run `45414858634` green) with the correct v0.3
  bundles (`.mediaType` `application/vnd.dev.sigstore.bundle.v0.3+json` plus an
  inclusionProof on every bundle) and cosign signature verification passed, but
  `colab update --install` from a v0.1.6-labelled binary failed with `SBOM
  attestation has an unsupported predicate type: invalid signature`. Root cause:
  `cosign attest-blob --type cyclonedx` (v2.6.5) emits predicateType
  `https://cyclonedx.org/bom`, while `internal/update/sbom.go:18-28` accepts only
  `https://cyclonedx.org/schema/bom` or `https://cyclonedx.org/schema/bom-*`.
  Fix (producer only): `attest-blob` now uses
  `--type https://cyclonedx.org/schema/bom-1.5` — a URI cosign copies verbatim —
  the post-release `cosign verify-blob-attestation` uses the same URI, and a
  `jq` guard on the DSSE payload asserts `.predicateType` equals it. The exact
  URI is chosen so the **unmodified** verifier in already-shipped v0.1.6/v0.1.7
  binaries accepts the next release; no verifier change was needed or made.
  Published assets are immutable, so a **new tag** is required. §4/§6 item 5,
  `docs/FAQ.md`, `docs/RELEASING.md` and `CHANGELOG.md` updated accordingly; no
  release was cut, tagged or live-proven in this change.
- 2026-11-24 — v0.1.7 prepared: CLI version bumped to match the changelog
  (guarded by `TestNumberMatchesLatestChangelogRelease`), with the new-format
  cosign bundles and `whoami` fix. A signed tag and real `colab update --install`
  proof are pending; the v0.1.6 bundle-format defect is recorded below.
- 2026-11-24 — **CONFIRMED DEFECT in v0.1.6, producer fixed in this change.**
  v0.1.6 was signed and its cosign proof passed (run `45399682610`), but
  `golab update --install` against it fails with
  `unknown field "base64Signature": invalid signature`. Root cause: cosign
  v2.6.5's `sign-blob`/`attest-blob` default to LEGACY bundles, while
  `internal/update/verify.go` parses sigstore v0.3 protobuf bundles only; the
  cosign proof hid this because `verify-blob`/`verify-blob-attestation`
  auto-detect both formats. Fix: add `--new-bundle-format` to both signing
  commands and a `jq` guard in the post-release proof asserting `.mediaType`
  contains `v0.3` and a tlog `inclusionProof` exists on every bundle. The
  verifier is deliberately unchanged — the producer is the defect. A **new
  tag** is required to prove the fix end-to-end; v0.1.6 is immutable and cannot
  be repaired in place. §4 and §6 item 5 corrected accordingly; no release was
  cut or tagged in this change.
- 2026-11-24 — Docs corrected where they lagged the code: SCT enforcement is
  **not** an open gap (`internal/update/verify.go:157-163` enables
  `WithSignedCertificateTimestamps(1)` in production; only the test-CA opt-out
  for SCT-less bundles remains). Updated `docs/03-go-architecture.md`,
  `docs/AUDIT-02-protocol-security.md` S-8 and `docs/08-risks-decisions-security.md`.
- 2026-11-24 — `whoami` failed with `credentials have no access token after
  refresh` despite a valid stored refresh token (reproduced live). `accessTokenFromAuth`
  replaced the client's `*oauth2.Transport` with a capture RoundTripper, so no
  `Authorization: Bearer` header was ever attached. The fix preserves the oauth2
  transport and swaps only its `Base` for the capture, keeping the synthetic
  200 `{}` short-circuit; non-oauth2 transports fail closed. No token is logged
  or embedded in errors. `TestAccessTokenFromAuthCapturesBearer` fails on the
  old code and passes after; focused `TestWhoami|TestAccess`, `go vet ./...`,
  `go test -count=1 ./...` and both shell safety scans pass locally. No live
  signed-release or tokeninfo claim is made for this fix.
- 2026-11-24 — v0.1.6 tagged and published: CLI version bumped to match the
  changelog, with `TestNumberMatchesLatestChangelogRelease` guarding against a
  future tag carrying a stale binary version. Signed release run
  `45399682610` is green; verification evidence is recorded in §4 (completed
  later the same day).
- 2026-11-24 — Self-update could never offer a newer release: `app.UpdateConfig`
  embedded the running version in `AssetPattern`, so a v0.1.4 binary scanning a
  v0.1.5 tag looked for a `golab_0.1.4_…` asset that does not exist and failed
  with `ErrNoRelease`. The pattern is now version-agnostic but anchored and
  platform-specific; `CurrentVersion` (and therefore `IsNewer`) remains the
  no-downgrade guard, and signature/SBOM/checksum verification is untouched.
  `TestUpdateConfigOffersNewerRelease` fails on the old pattern and passes on
  the new one; `go vet ./...` and `go test -count=1 ./...` pass locally. The
  read-only check was later exercised against v0.1.6 (see §4 and the verified
  entry above); `--install` of a newer release remains human-only.
- 2026-11-24 — Release proof hardened: every archive signature, every CycloneDX
  SBOM attestation and every checksum line is now verified; GoReleaser pinned to
  v2.20.3; repository-identity and `release`-environment gates added; syft output
  moved to an env var and signature uploads made immutable. `check-redaction.sh`
  now also scans YAML/shell/JSON. Shell syntax, PyYAML parse and local stub runs
  passed first; the real cosign proof then passed on v0.1.6 (see verified entry
  above and §4).
- 2026-11-24 — Release SBOM step repaired: `download-syft` now pins syft
  v1.57.2 and the scan uses the root-command form (v0.42.2 had no `scan`
  subcommand and could not parse `go 1.27.4`). Verified locally that the
  CycloneDX JSON contains the Go module dependencies (v0.42.2 produced 0
  components). The signed v0.1.6 release later proved this end-to-end (see
  verified entry above and §4).
- 2026-11-23 — Initial ledger created. Added CI truthfulness gates, release
  pipeline, live-e2e/QEMU/Termux skeletons, conformance scripts and testdata
  skeletons (remediation of AUDIT-02 S-9 and AUDIT-03 P0/P1 findings). No live
  proof; an identity mismatch was recorded at the time, later verified stale and
  corrected on 2026-11-24 (see §4).
