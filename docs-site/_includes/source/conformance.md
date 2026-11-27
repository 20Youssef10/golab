<!-- Generated from docs/CONFORMANCE.md §1. Test-layer status, 3. Coverage gate, 6. Human-only / external steps blocking "done" by docs-site/tools/sync-sources.py. Do not edit. -->

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
