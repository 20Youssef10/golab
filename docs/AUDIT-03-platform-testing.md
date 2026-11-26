# AUDIT-03 · Platform / Testing / Release-Automation Audit

> **Scope.** Read-only audit of `golab` against `docs/04` (platform matrix), `docs/07` (testing layers), `docs/06` (roadmap), `docs/08` (risks/security), `docs/COMPAT.md`, and the upstream checkout at `HEAD 0abf82d` (`/tmp/opencode/google-colab-cli`). No code and no other document was modified.
>
> **Date:** 2026-11-23. **Method:** static inspection of every file listed in the brief; file/test counts taken from the working tree with shell tooling. Go is not installed in the audit sandbox, so the "known facts" (vet/lint/race/11 cross-builds/govulncheck green) are accepted as reported and were **not** re-executed here. That matters: several findings below are *CI wiring* defects that local green runs do not detect.
>
> **Reconciliation addendum (2026-11-26, v0.2.0).** This document is a dated snapshot of the 2026-11-23 tree; the verdicts in the tables below are the *original* audit verdicts, not current status. Superseded findings: no GoReleaser/`release.yml`/signed artefacts (all now present); no `live-e2e.yml`/secrets/kill switch and no live pass (workflow exists; a billable CPU e2e pass is recorded in `CHANGELOG.md` `[0.2.0]` Verification); no lint/shell gates in CI (now wired through `make check` and `ci.yml`, Actions pinned); no golden harness (`scripts/golden.sh --scenario <name> --update` exists, though scenario fixtures and `DELTAS.yaml` are still absent); `module golab` and `cmd/colab` (now `github.com/20Youssef10/golab` and `cmd/golab`); no coverage gate and no goleak (both now wired — `scripts/coverage-gate.sh` gates seven packages with non-regression ratchets for `platform`/`app`, and `goleak` is a hard CI gate with `VerifyTestMain` in `runtime`/`sshx`/`repl` and `VerifyNone` in `update`). Live GET-only wire capture, real devices, interactive TTY and induced WS-drop checks remain **human-gated** and are not claimed done by this addendum.
>
> **Verdict labels** used throughout:
> - ✅ **Implemented + tested** — code exists and has an automated test that exercises it.
> - 🟡 **Implemented, unverified** — code exists and is wired in, but only compile-tested or tested with a mock/simulated input; no real-environment proof.
> - 🔴 **Broken** — a concrete failure path exists in the current tree.
> - ⬜ **Not in scope yet** — planned work, no code expected at this milestone; tracked so it is not mistaken for a defect.
>
> **Headline (as audited 2026-11-23; see reconciliation addendum above):** the *code* under audit was in better shape than the *machinery around it*. The test suite (313 Go test functions) and the updater verifier were substantive; the release pipeline that the updater depends on did not exist, the golden/differential/live layers were empty, and two CI defects would have made the then-current workflow fail or lie on a clean runner. Those gaps have since been closed in code; one billable live e2e pass is recorded in `CHANGELOG.md` `[0.2.0]`, while wire capture, real-device and interactive-TTY proof remain human-gated.

---

## 0. Executive summary

| Area | State | One-line finding |
|---|---|---|
| Tier-1 platform code | 🟡 | All six targets cross-compile; Windows VT/console-close, Termux netfix, atomic state are wired in, but **none** has passed doc 04 §9's per-platform acceptance list. |
| Tier-2 platform code | 🟡 | Compile-only via `make cross`; no QEMU execution, no Termux emulator job. |
| Test L1 (unit) | ✅ | 311 `Test*` functions across 17 internal packages. |
| Test L2 (fake servers) | ✅ | `fakecolab`, `fakekernel`, `fakesshd` exist and cover the documented message/fault matrix. |
| Test L3 (golden) | 🟡 (harness present, fixtures absent) | `testdata/golden/` holds only `README.md`; the harness is `scripts/golden.sh --scenario <name> --update`; no fixtures and no `DELTAS.yaml` yet. |
| Test L4 (differential) | 🔴 | No intercepting proxy, no Python-vs-Go scenario runner. |
| Test L5 (live e2e) | 🟡 (workflow present, re-run pending) | `.github/workflows/live-e2e.yml` exists (manual, secrets-gated, kill switch, sweeper); one billable CPU pass recorded in `CHANGELOG.md` `[0.2.0]`. |
| Test L6 (platform) | 🟡 | 6-OS unit matrix + cross-build; no QEMU/Termux/device functional tests. |
| Test L7 (security/fuzz) | 🟡 | Strong redaction + sigstore-tamper tests, 2 fuzz targets, but no fuzz CI, no `goleak`, no coverage gate, redaction/path shell scripts unwired. |
| Release engineering | 🔴 | No GoReleaser config, no `release.yml` (the exact identity the updater pins), no signed artefacts or SBOM, no installers, no release channels. Module path is not importable. |
| CI/CD | 🟡 | `make cross` now targets `dist/`; lint plus both shell gates run in CI and Actions are pinned; `live-e2e`/`qemu-smoke`/`termux-nightly`/`release` workflows exist; upstream-sync/wire-recapture jobs remain absent. |
| Porting estimate | 🟡 | ~200–240 of 373 upstream top-level tests (~55–65%) have intent-level equivalents; no `docs/CONFORMANCE.md` records the count required by docs/07 §2. |

---

## 1. Platform matrix status

### 1.1 Tier 1 (release-blocking) — docs/04 §1

| Target | What is actually implemented | Evidence | Test status | Concrete gap |
|---|---|---|---|---|
| `linux/amd64` | Full binary; `Makefile:9` `-trimpath`; netfix, state, keyring, render, SSH all compile here. | `Makefile:8-9`, `internal/platform/platform.go`, `internal/state/state.go` | 🟡 Unit + fake-server tests run on `ubuntu-latest` (`.github/workflows/ci.yml:16,25`). Doc 04 §9 items 2–7 unverified. | No `colab version`/`help`/completion install check as a gate; no fake-server *end-to-end* scenario job; no recurring per-release live smoke gate (one billable CPU pass is recorded in `CHANGELOG.md` `[0.2.0]`). |
| `linux/arm64` | Same code, no arch-specific source. | `Makefile:22-26` (dist list loop) | 🟡 Unit tests on `ubuntu-24.04-arm` (`ci.yml:16`); compile only otherwise. | Same §9 gaps. `GOARM`/`GOAMD64` variants are not built at all (doc 04 §1 optional `v3` absent). |
| `darwin/arm64` | Same code; universal2/`lipo` only planned. | doc 04 §5, §7 | 🟡 Unit tests on `macos-latest` (`ci.yml:16`). | macOS keychain `security`-tool argv exposure (doc 04 §5, doc 08 DEC/§3) **not measured**; notarisation ⬜; no universal2 artefact. |
| `darwin/amd64` | Same code. | `ci.yml:16` (`macos-15-intel`, label availability ❓) | 🟡 Unit tests only. | Same as above; Rosetta fallback not exercised. |
| `windows/amd64` | VT enable/restore (`internal/sshx/console_windows.go:17-32`), console-close handler (`signals_windows.go:32-66`), named-pipe agent (`npipe_windows.go:16-29`), `CTRL_CLOSE_EVENT` wiring (`lifecycle.go:56`), Windows rename-replace updater (`internal/update/install.go:86-102`), Pageant (`agent_windows.go:22-26`). | paths as listed | 🟡 Compiled + unit-tested on `windows-latest`; Windows-only paths are exercised via **simulated** inputs (`TestReplaceBinaryWindows` passes `goos` as a string; `TestWindowsProxyCommandQuoting` tests quoting, not a console). | Owner-only **ACL not implemented** (§1.3.3); VT restore on real conhost/Windows Terminal untested; `CTRL_CLOSE_EVENT` teardown never fired in a test; running-`.exe` replace never run on Windows. |
| Termux (`linux/arm64` + shim) | Detection (`platform.go:99-113`), `TMPDIR`/`$PREFIX/tmp` (`platform.go:85-96`), resolver + CA shim (`platform.go:144-186`), wired at `internal/app/app.go:81`, embedded fallback roots (`platform.go:19`). | paths as listed | 🟡 `TestReadNameservers` and `TestTempDirPrefersTMPDIR` only (`platform_test.go:27-33,73-81`). | No emulator/device run; `faccessat2` workaround absent; `termux-open-url`/WSL launcher absent (§1.3.1/§1.3.5). |

**Doc 04 §9 "Definition of done" status:** of the seven criteria, only #1 (`go test ./...`) is demonstrably green on the CI matrix. #2 (version/help/completion), #3 (fake-server end-to-end scenario), #4 (token-store ≤ 5 s on absent/locked keyring, measured on each OS), #5 (fake sshd interactive incl. resize/Ctrl-C/`exit 7`), #6 (signed-update dry run), and #7 (human live smoke) have **no automated gate or recorded result on any platform**. Consequence: per doc 04 §9, **zero platforms are currently "supported"**.

### 1.2 Tier 2 / Tier 3

| Group | Targets | Status | Evidence / gap |
|---|---|---|---|
| Tier 2 | `windows/arm64`, `linux/arm` (GOARM 6/7), `linux/386`, `linux/riscv64`, `freebsd/amd64`, Termux `linux/arm`, Termux `linux/amd64` | 🟡 **Compile only** on most; unit tests on `windows-11-arm` (label availability ❓) and `ubuntu-24.04-arm` | `Makefile:22-26` loops over `go tool dist list \| grep -E '^(linux\|darwin\|windows\|freebsd)/'` — this covers the listed GOOS/GOARCH pairs but **not** GOARM=6/7 variants, not `GOAMD64=v3`, and not `GOOS=android` (deliberate, ADR-002). No binary is uploaded as an artefact. |
| Tier 3 | `linux/ppc64le`, `linux/s390x`, `linux/loong64`, `freebsd/arm64`, `openbsd/amd64`, `netbsd/amd64`, `windows/386` | 🟡 Compile-only where the Makefile pattern reaches them; ⬜ OpenBSD/NetBSD entirely absent from the loop | doc 04 §1 expects compile-only; that part is satisfied for the four covered OS families. The OpenBSD/NetBSD gap is a one-line Makefile change, not a design issue. |

`CGO_ENABLED=0` is exported globally in `Makefile:2` — good, and consistent with the platform mandate.

### 1.3 Concrete platform gaps (deep dives)

#### 1.3.1 Termux DNS / CA shim — 🟡 implemented, unverified
- **Evidence:** `internal/platform/platform.go:150-186` sets `SSL_CERT_FILE` from `$PREFIX/etc/tls/cert.pem` when unset, reads `$PREFIX/etc/resolv.conf` (fallback `/etc/resolv.conf`), falls back to `1.1.1.1:53`/`8.8.8.8:53`, and installs `net.DefaultResolver` with `PreferGo: true` and a 5 s dial. `readNameservers` is unit-tested (`platform_test.go:73-81`); `x509roots/fallback` is blank-imported (`platform.go:19`); the shim is invoked from `app.New` (`app.go:81`) so it is **not dead code**.
- **Not a defect, but unverified:** no test asserts `InstallNetFix` actually changes `net.DefaultResolver` under simulated Termux env (`TERMUX_VERSION`/`PREFIX`), no `SSL_CERT_FILE` test, no device. Doc 04 §3.2 marks these ❓ by design; doc 08 §6 still says "not reproduced on a device".
- **Correctness concerns to probe, not assumptions:** the resolver uses the *same* nameserver list for `network` values other than `udp` (the `Dial` callback ignores `network` filtering, `platform.go:174-183`); IPv6-only `nameserver` lines and search domains/`options` are ignored; `SSL_CERT_FILE` is set via `os.Setenv` after process start, which Go's `x509` reads lazily but some libraries cache. These are **TODO probes** (upstream AGENTS.md §14), not confirmed bugs.
- **Next action:** add `TestInstallNetFixTermux` using `t.Setenv` + a temp `PREFIX` with a fake `resolv.conf`/`cert.pem`, asserting the resolver dials the fake nameserver and `SSL_CERT_FILE` is set; then run doc 04 §3.4 nightly emulator job (§4 below).

#### 1.3.2 `faccessat2` — 🟡 unverified, no mitigation present
- **Evidence:** doc 04 §3.2 requires a build-tagged `findExecutable` using `os.Stat` + mode bits and avoiding `exec.LookPath` on Android hot paths. The only `exec.LookPath` call in the tree is `internal/cli/utility_extra.go:49` (the cold-path URL launcher). There is no `android`-tagged source file and no `findExecutable` helper.
- **Why this is not automatically "broken":** release artefacts are `linux/arm64` (ADR-002), not `GOOS=android`, so the SIGSYS report may not apply; this is exactly the kind of caveat doc 08 §6 labels "reported, not reproduced".
- **Next action:** on a real Android 10/11/12/14 device under Termux run `strace -f -e trace=faccessat2,execve` while invoking `colab url --open`, `colab exec` and `colab run`; if SIGSYS appears, add `internal/platform/exec_android.go` with a `Stat`-based lookup and a build-tagged test. Treat the result as a recorded finding in doc 04 §3.2 either way.

#### 1.3.3 Windows console modes / ACL / VT restore — 🟡 partially implemented, 🟡 unverified, 🔴 ACL missing
- **Implemented:** `enableVirtualTerminal` sets `ENABLE_VIRTUAL_TERMINAL_INPUT|OUTPUT` and `SetConsoleOutputCP(65001)`, returns a restore closure (`console_windows.go:17-32`), called from `internal/sshx/session.go:49` and deferred. `installConsoleClose` registers `CTRL_CLOSE/LOGOFF/SHUTDOWN` and fans out to handlers (`signals_windows.go:32-66`), wired into teardown at `internal/sshx/lifecycle.go:56`. The REPL restores raw mode via `sync.Once` (`internal/repl/tty.go:192-202`), with a test hook (`TestTTYRestoresConsoleOnExit`).
- **Gaps:**
  - The restore closure ignores all `SetConsoleMode` errors (`console_windows.go:27-32`) and is only wired for the **SSH** path; the REPL uses `x/term.MakeRaw` but never calls `enableVirtualTerminal`, so VT output processing is not explicitly enabled there (doc 04 §4 expects it for REPL too). No test fires the console-close callback.
  - `pipeConn.SetDeadline`/`SetReadDeadline`/`SetWriteDeadline` are no-ops (`npipe_windows.go:64-66`) and `ReadFile`/`WriteFile` are synchronous; a hung OpenSSH agent can block the process indefinitely, in tension with AGENTS.md §5 ("explicit timeouts everywhere").
  - **🔴 ACL:** `platform.MakePrivate` returns `nil` on Windows with only a comment that `%APPDATA%` is "already user-profile ACL restricted" (`platform.go:132-139`). doc 04 §2 and doc 08 §3 require an owner-only ACL via `x/sys/windows`. `TestMakePrivate` **skips Windows entirely** (`platform_test.go:53-56`). `state.writeJSON` and the token file both rely on this no-op. Verdict: not implemented, not broken on the happy path, but a security-control gap.
- **Next action:** (a) add `internal/platform/private_windows.go` using `windows.GetSecurityInfo`/`SetSecurityInfo` with an explicit owner-only DACL and a `//go:build windows` test asserting the descriptor; (b) add a Windows CI step that creates a file in `%APPDATA%`, calls `MakePrivate`, and opens it as a second user (or at minimum parses the DACL); (c) call `enableVirtualTerminal` from `repl.NewTTY.Run` and assert the restore closure fires on signal/panic.

#### 1.3.4 macOS keychain — 🟡 unverified
- **Evidence:** `internal/auth/tokenstore.go:22-70` uses `github.com/zalando/go-keyring` with a 5 s `withStringTimeout`/`withErrorTimeout` wrapper; `tieredStore.probe` caches availability once per process (`tokenstore.go:89-106`) and falls back to a 0600 file with one stderr notice (`tokenstore.go:108-120,146-158`). `TestKeyringProbeTimeoutIsCached` and `TestKeyringTierUsesUpstreamIdentity` exist.
- **Gap:** doc 04 §5 asks to confirm the `security` CLI does not leak the token via argv; no `ps`-style test, no timing measurement on a locked keychain, no notarisation/quarantine documentation. `go-keyring` shells out to `security add-generic-password` on macOS — the secret is typically passed via stdin/`-w`, but **this must be verified on a Mac**, not inferred.
- **Next action:** add a manual-release checklist item "run `ps -ww -ef | grep security` during `colab auth`; assert no token in argv" plus a CI job on `macos-latest` that locks/creates a temporary keychain and asserts `probe` returns within 5 s (`auth` tests already mock the backend for the deterministic part).

#### 1.3.5 WSL and URL launching — 🟡 partial, concrete gap
- **Evidence:** `internal/cli/utility_extra.go:38-55` implements only three launchers: `rundll32 url.dll,FileProtocolHandler` (Windows), `open` (macOS), `xdg-open` (everything else). doc 04 §2 requires `wslview`/`explorer.exe` on WSL and `termux-open-url` on Termux, with "print URL" fallback.
- **Verdict:** on headless WSL/Termux `openURL` returns false and the caller prints the URL, so this is a **degraded path, not a crash** — but the documented behaviour is missing. It also uses `exec.LookPath` (AGENTS.md: avoid on Android hot paths; cold here, so acceptable with a comment).
- **Next action:** add `openURL` cases keyed off `platform.IsTermux()` and a new `platform.IsWSL()` (check `/proc/version` for `microsoft`/`WSL`) with `wslview` → `explorer.exe` → print-URL fallback; add table-driven tests with a mocked `LookPath`/exec seam (currently the function is not injectable).

#### 1.3.6 Running-exe self-update (Windows) — 🟡 simulated only
- **Evidence:** `internal/update/install.go:65-108` creates a temp file in the target dir, renames the running `.exe` to `.old`, installs the new file, and rolls back on failure; `CleanupOld` (`install.go:113-119`) removes the backup next start. `TestReplaceBinaryWindows` and `TestReplaceBinaryUnix` cover the logic with a `goos` parameter.
- **Gap:** no test runs on Windows against a genuinely locked executable. `cmd/golab/main.go:11-13` does **not** call `update.CleanupOld` — the comment at `install.go:110-112` says "The CLI composition root calls this at startup", but the composition root is `cli.Execute`/`app.New` and no call site exists in the tree (grep for `CleanupOld` finds only its definition and tests). **This is a real wiring defect**: after a successful Windows update the `.old` backup is never deleted by the next start.
- **Next action:** call `update.CleanupOld` in `cmd/golab/main.go` (or `app.New`) behind the existing error-tolerant semantics, add a test that seeds a fake `*.old` and asserts removal, and add a Windows CI step that runs the updater against a locally signed fake release.

---

## 2. Test-layer audit (docs/07 §1, L1–L7)

### 2.1 Layer status

| Layer | Doc 07 requirement | Present | Evidence / gap | Next action |
|---|---|---|---|---|
| **L1 Unit** | Pure functions, parsers, validators, state; every PR, 6 runners, `-race`; blocking | ✅ | 311 `Test*` functions across 17 packages; `ci.yml:16,25` runs `-race -count=1` on 6 OSes. | Confirm `windows/arm64` supports `-race`; if not, split the matrix. |
| **L2 Component (fake servers)** | CLI ↔ fakecolab ↔ fakekernel ↔ fakesshd in process; blocking | ✅ | `internal/testutil/fakecolab/fakecolab.go` (3 self-tests), `fakekernel/fakekernel.go` (scripted `stream`, `display_data`, `update_display_data`, `clear_output`, `error`, `input_request`, `colab_request`, dead/restarting status, binary buffers, 10 MB `BigStream` at `fakekernel.go:511`), `fakesshd/fakesshd.go` (2 self-tests, upstream failure presets). | Add a single `go test` entry-point that runs the full `new → exec → upload → download → ls → rm → stop` scenario as one fake-server test (doc 04 §9 item 3); today the scenario is split across package tests. |
| **L3 Golden** | `testdata/golden/<scenario>/{cmd,stdout,stderr,exit}`, normalisation, `DELTAS.yaml`, `--update` | 🟡 | `scripts/golden.sh` implements the harness (normalisation + `--update` + a `wire` secret scan); `testdata/golden/` still contains only `README.md`, with no fixtures and no `DELTAS.yaml`. Some inline exact assertions (e.g. `TestSSHCommandExitStatusAndByteExactOutput`) are not a golden layer. | Generate fixtures **from the Python CLI via L4** with `COLAB_BIN=./golab bash scripts/golden.sh --scenario <name> --update`; review every diff and record divergences in `DELTAS.yaml`. |
| **L4 Differential** | Python ↔ Go through intercepting proxy, ≥40 scenarios, nightly + release, blocking for release | 🔴 | No proxy code, no Python invocation, no scenario list in repo. docs/07 §6 design exists on paper only. | Implement a small Go reverse proxy with a locally trusted CA, redirect both CLIs' hosts, normalise outputs, fail on unlisted diffs. Start with 8 scenarios (help/version/new/exec/run/ls/url/usage) and grow to 40. |
| **L5 Live e2e** | Real CPU runtime, throw-away account, secrets-gated, sweeper, kill switch | 🟡 | `.github/workflows/live-e2e.yml` exists (manual dispatch, billing ack, kill switch, sweeper, GET-only default); a billable CPU pass is recorded in `CHANGELOG.md` `[0.2.0]` Verification. Auth/consent and a repeat pass remain human-gated. | Ask a maintainer to re-run the workflow for the current tree; wire fixtures still need GET-only capture (S0). |
| **L6 Platform** | QEMU smoke, Termux emulator, real devices, Windows consoles | 🟡 | 6-OS unit matrix + `make cross` compile; only one build-tagged test file (`internal/sshx/resize_unix_test.go`); no QEMU job, no emulator, no device checklist artifact. | Add `qemu-smoke` and `termux-nightly` jobs (§4); record the manual pre-release checklist as a checked-in file. |
| **L7 Security & fuzz** | Redaction golden, traversal/injection, updater tamper, decoders, fuzz ≥1 h, `goleak`, coverage, reproducible build, `go-licenses` | 🟡 | Strong: `internal/logx` redaction, `TestSecretsNeverLeak` in `colabapi`/`contents`/`runtime`/`auth`, `TestAppendRedactsSecrets`, injection tests (`TestInstallQuotingInjectionAttempt`, `TestExecEnvPrelude`), 15+ sigstore tamper tests in `internal/update`, `FuzzDecodeFrame` (`runtime/frame_test.go`), `FuzzSanitize` (`render/sanitize_test.go`), `scripts/check-redaction.sh`, `scripts/check-paths.sh`. Missing: no fuzz job/corpus, no `goleak`, no coverage gate, no reproducible-build check, no `go-licenses`, and **neither shell script is called from `ci.yml` or the Makefile**. | Wire both scripts into `make check` + CI; add `goleak.VerifyTestMain` to the main packages; add nightly `go test -fuzz` with a seeded corpus; add coverage/license/reproducibility jobs (§4). |

### 2.2 Porting estimate (explicitly an estimate)

**Counting method.** Upstream: `tests/test_*.py` = 36 files, `conftest.py` + `mock_kernel.py` = 38 files total; 373 top-level `def test_` (415 including nested `def test_` helpers). Our tree: 52 `*_test.go`, 311 `Test*` + 2 `Fuzz*` = 313 top-level test functions. docs/07 §2 requires `docs/CONFORMANCE.md` to record the count — **that file does not exist**, so the following is a group-by-group judgment from test names and assertions, not a recorded metric.

| Upstream group (file counts from `upstream/tests/`) | Upstream tests | Our equivalents (representative) | Estimated intent-level port | Assessment |
|---|---:|---|---:|---|
| `test_auth.py` 26 + `test_auth_adc.py` 9 + `test_local_credentials.py` 8 | 43 | `internal/auth/*_test.go` (26), `internal/logx/logx_test.go`, `TestMakePrivate` | ~25/43 | OAuth remote flow, refresh, partial grant, migration, ADC scopes ported. **Missing:** Linux locked-keyring detection (`_check_linux_keyring_locked` family), file-privacy tests for debug log/history on all OSes, `chmod` failure tolerance. |
| `test_client.py` 19 + consumption (4) | 23 | `internal/colabapi` (13), `TestUsage`, `TestPublicAPIProbePersistsSettings` | ~15/23 | XSSI/XSRF/retry/412/portured; fewer fault-matrix cases. |
| `test_runtime.py` 10 + `test_runtime_options.py` 5 + `test_streaming.py` 12 | 27 | `internal/runtime` (28) | ~24/27 | Strong; idle-timeout, dead kernel, reply-after-idle, cross-request filtering covered. |
| `test_contents.py` 6 | 6 | `internal/contents` (18) | 6/6 | Superset upstream. |
| `test_state.py` 19 + `test_common.py` 7 + `test_resolution_logic.py` 7 + `test_history.py` 2 | 35 | `internal/state` (5), `internal/app` (4), `internal/history` (10) | ~20/35 | **Missing:** RW-lock-vs-sidecar semantics, `fcntl` prohibition test, *cross-process* `kill -9` mid-write crash test (only in-process `TestConcurrentWritesFromSeparateStores`), most resolution sub-cases. |
| CLI surface: `test_cli.py` 39, `test_exec.py` 13, `test_ipynb_exec.py` 3, `test_run.py` 23, `test_repl.py` 15, `test_url.py` 12, `test_utils.py` 9, `test_cli_log.py` 4, `test_whoami.py` 4, `test_readme.py` 4, `test_version.py` 3, `test_pay.py` 1, `test_log_export.py` 1 | 131 | `internal/cli` 53, `internal/repl` 32, `internal/render` 24, `internal/nb` 5, `internal/history` 10 | ~75–85/131 | Many Go-specific tests inflate the count. **Missing:** SystemExit exit-code table from `test_run.py`, `test_url.py` host/fragment normalisation, `test_cli_log.py` show/filter/export, `test_whoami.py` refresh-before-tokeninfo, `test_exec.py` shell-escape forwarding. |
| `test_automation.py` 14 | 14 | `TestInstallBuildsAndExecutesSnippet`, `TestAuthFlow`, `TestDriveMountColabReplyRoundTrip`, `TestDriveMountQuotesPath` | ~5/14 | Drive-propagation failure messages, 400-without-raw-HTML, headless URL printing not ported. |
| SSH: `test_ssh.py` 27 + `test_ssh_autocreate.py` 9 + `test_ssh_cmd.py` 9 + `test_ssh_lifecycle.py` 11 + `test_ssh_wire_contract.py` 8 + `test_ssh_workdir.py` 3 + `test_windows_ssh.py` 5 | 72 | `internal/sshx` (43), `internal/cli` SSH tests (~7) | ~48–52/72 | **Missing:** stale-sshd kill paths (`test_force_kills_stale_sshd`, `test_kill_stale_sshd_body`), most `test_windows_ssh.py` (only `TestWindowsProxyCommandQuoting`; no `NUL` device, no signal-registration assertions), most `test_ssh_cmd.py` proxy re-invocation cases. |
| `test_update.py` 28 | 28 | `internal/update` (35, mostly sigstore-specific) | ~0/28 **behaviourally** | Deliberate deviation (docs/COMPAT.md): signed GitHub Releases replace PyPI/pip/uv logic and banner hints. Not a defect; document as Δ, not "ported". |

**Total estimate: ≈ 200–240 of 373 (~55–65%) intent-level ported.** Treat as lower-confidence because no conformance ledger exists. The Go suite has ~313 tests but many are Go-specific (sigstore, sanitizer, frame decoder) and do not map 1:1 to upstream behaviours.

### 2.3 Major unported groups (upstream files to port next)

| Upstream file | What is missing | Why it matters | Concrete next action |
|---|---|---|---|
| `upstream/tests/test_windows_ssh.py` (5) plus `upstream/integration/repro_windows_ssh/test.py` | Literal-argument ProxyCommand launch, local `NUL` device, `CTRL_CLOSE`-without-SIGHUP handler registration | Windows is Tier 1; this is the least-covered Tier-1 behaviour group | Add `internal/cli/ssh_windows_test.go` (build-tagged) + port the five cases against a fake ProxyCommand. |
| `upstream/tests/test_run.py` (23) | `SystemExit` 0/non-zero/string, prelude suppression of IPython warning, timeout flag, teardown with refreshed token | Exit-code contract is user-visible and piped-output critical | Port the exit-code table as one table-driven Go test in `internal/cli/execution_test.go`. |
| `upstream/tests/test_cli_log.py`, `test_log_export.py`, `test_readme.py`, `test_whoami.py` (13) | log show/filter/export golden, resource fallback, tokeninfo refresh | Docs/07 L3 has no fixtures partly because these formats were never pinned | Generate golden fixtures for the four log export formats and the README/SKILL fallbacks. |
| `upstream/tests/test_local_credentials.py` (8) | File-mode/ACL assertions, redaction of logs and stderr, look-alike strings | Directly maps to doc 08 §3 security controls; Windows ACL is already a known gap (§1.3.3) | Add cross-OS file-mode test + Windows DACL test; fold into the redaction golden. |
| `upstream/tests/test_automation.py` (14) | Drive propagation failure messages and hook replies | `install`/`auth`/`drivemount` are P1 commands (doc 05) | Port message assertions into `internal/cli/automation_test.go`. |
| `upstream/tests/test_force_kills_stale_sshd` (in `test_ssh.py`) | Stale-sshd kill body/HTTP status | Prevents hung sessions on reused runtimes | Port into `internal/sshx` with a fake PID/connection map. |
| `upstream/tests/test_state.py` (19) | Lock semantics, cross-process readers, crash safety | D-1/D-2 defect class; our atomic write is unproven under `kill -9` | Add a helper binary + `TestStateCrashSafety` that kills a writer mid-write and re-reads. |

---

## 3. Release-engineering gaps

> **Superseded (2026-11-26, v0.2.0):** `.goreleaser.yaml` and `.github/workflows/release.yml` now exist and produce cosign signatures plus signed SBOMs; real signed releases and a v0.1.9 → v0.2.0 self-update are recorded in `CHANGELOG.md`. The row statuses below are the original audit verdicts.
>
> **Critical dependency first (as audited):** `internal/update` is written to *require* something this repository does not yet produce. `internal/update/check.go:139-141` returns `ErrMissingSBOM` when a release has no signed SBOM attestation; `install.go:31-33` refuses installation without `SBOMURL`; `verify.go:127-129` pins the signer identity to `https://github.com/<Owner>/<Repo>/.github/workflows/release.yml@`. Owner/Repo default to **`golab-cli` / `golab`** (`internal/app/app.go:88-93`). Therefore a release cut today from the current tree would make `colab update` **always refuse it**. This is ⬜ *not in scope yet* (no release has been cut), but it is a proven, testable failure path — not a hypothetical.

| Item | Status | Evidence | Concrete next action |
|---|---|---|---|
| GoReleaser config | 🔴 absent | No `.goreleaser.yml`/`.goreleaser.yaml` in repo root; docs/04 §7 and §8 assume it. | Add `.goreleaser.yaml` with the full GOOS/GOARCH matrix, `CGO_ENABLED=0`, `-trimpath -s -w`, `archives` named `golab_<ver>_<os>_<arch>.tar.gz` (must match `app.go:211`), checksums, and `ignore` for unsupported combos. |
| `release.yml` workflow (the pinned cosign identity) | 🔴 absent | `verify.go:127-129` expects the signing workflow to be exactly `.github/workflows/release.yml`; no such file exists. `permissions: id-token: write` is required for keyless OIDC and is absent from `ci.yml`. | Create `.github/workflows/release.yml` triggered on annotated `v*` tags, with `permissions: contents: write, id-token: write`, GoReleaser, `cosign sign` for each checksum/bundle, and `cosign attest` for a CycloneDX/SPDX SBOM whose in-toto subject digest equals the artifact (required by `internal/update/sbom.go:17-29,36-51`). Assert the SAN in a release dry-run test. |
| Signed artefacts + SBOM publication | 🔴 absent | No `*.sigstore.json`, `*.bundle`, or SBOM assets anywhere; updater expects them (`check.go:108-141`). `sigstore_test.go`/`helpers_test.go` build a **virtual** Sigstore, so local tests pass without real release material. | Publish checksums + one cosign bundle per artifact + one signed SBOM attestation per release. Add a post-release CI step that downloads them and runs `colab update --install` against a stale version (dry-run field) to prove the happy path. |
| `install.sh` / `install.ps1` | ⬜ not in scope yet (Phase 4, docs/04 §7) | No files. | Write `install.sh` that detects Termux, installs to `$PREFIX/bin`, verifies checksum + cosign signature before install; `install.ps1` with signature verification and SmartScreen guidance. Both must be tested in CI against a fake release. |
| Homebrew / Scoop / winget / nfpm / AUR / Termux packages | ⬜ not in scope yet (Phases 4–5) | No manifests. | After GoReleaser is green, add `brews`/`scoops`/`winget`/`nfpms` blocks and a Termux `go install` doc; validate with `goreleaser check` in CI. |
| `go install github.com/20Youssef10/golab/cmd/golab@latest` channel | ✅ configured (was 🔴 as audited) | `go.mod:1` declares `module github.com/20Youssef10/golab`; the only entry point is `cmd/golab/main.go`. This is the canonical source channel documented in `README.md` and `docs/INSTALL-TERMUX.md`. | Verify from a clean GOPATH before advertising a new release; DEC-01 is resolved. |
| Binary/repo name identity | ✅ resolved (was 🔴) | The canonical binary is `golab` (`docs/USAGE.md`, `README.md`); the updater pins `20Youssef10/golab`. | DEC-01 settled; `golab version` prints the implementation version and the upstream compatibility target. |
| Version / changelog process | 🟡 partial, ⬜ release artifacts missing | `internal/version/version.go` exists; `docs/06 §6` describes SemVer + Keep-a-Changelog; there is **no `CHANGELOG.md`** in our repo (upstream has one at `upstream/CHANGELOG.md`). | Add `CHANGELOG.md` (Keep-a-Changelog) and a `release` runbook mirroring upstream `AGENTS.md` §44–57; confirm the inferred version with the user before tagging (our AGENTS.md release rule). |
| Pinned GitHub Actions | 🔴 absent | `ci.yml:19-20,29-30,37-39` uses `@v4`/`@v5`; `govulncheck` is installed with `@latest` (`ci.yml:41`). doc 08 R-13 requires pinning by SHA. | Pin each action to a full commit SHA with a `# vX.Y.Z` comment; pin the govulncheck version; add Dependabot for controlled bumps. |
| Cosign identity / key management | 🟡 designed, ⬜ operational | Pinned issuer and SAN regex in `verify.go:20,127-132`; no release identity exists in GitHub yet. | Document the exact OIDC subject (`repo:golab-cli/golab:ref:refs/tags/vX.Y.Z`, workflow `release.yml`) in docs/08 and add a test that a *different* workflow path is rejected (already present as `TestVerifySignatureRejectsWrongWorkflow` — keep it green after wiring). |
| Reproducible build check | 🔴 absent | No `-trimpath` reproducibility gate; docs/07 §11 requires it. | Build twice with `SOURCE_DATE_EPOCH` fixed, compare SHA-256 of artifacts, fail on mismatch; publish the check as a CI job. |
| `go-licenses` report / SBOM generation | 🔴 absent | No `go-licenses` or Syft/CycloneDX step; docs/07 §11 + docs/08 §4 require attribution. | Add a release step producing a CycloneDX SBOM (Syft or `cdxgen`) and a `go-licenses report ./...` artifact; attach both to the release and sign the SBOM. |

---

## 4. CI/CD gaps

### 4.1 docs/04 §8 job matrix vs `.github/workflows/ci.yml`

| Doc 04 §8 job | Present? | Finding / next action |
|---|---|---|
| `test` on 6 OSes | ✅ | `ci.yml:12-25`. Add an explicit `-race` support check for `windows-11-arm`; runner-label availability/pricing for `ubuntu-24.04-arm`, `macos-15-intel`, `windows-11-arm` is ❓ and must be confirmed with a real run (doc 04 §8 flags this too). |
| `cross-build` (`goreleaser build --snapshot --clean`) | 🔴 **broken** | Current job runs `make cross` (`ci.yml:33`), which writes binaries to **`/tmp/opencode/golab-<os>-<arch>`** (`Makefile:25`). `/tmp/opencode` is a session-local path that does not exist on a clean GitHub runner, so `go build -o` will fail. **Next action:** replace with `$(mktemp -d)` / `dist/`; migrate to `goreleaser build --snapshot --clean` once §3's config exists; upload artifacts. |
| `qemu-smoke` | 🔴 absent | Add `qemu-user-static` + `colab version`/`help` + fake-server suite for `linux/arm`, `linux/riscv64`, `linux/386`, `linux/arm64` (doc 04 §8). |
| `termux-nightly` | 🔴 absent | Add Android emulator + Termux job running DNS/CA/TMPDIR/no-D-Bus shim tests and the fake-server suite (doc 04 §3.4). |
| `live-e2e` (nightly/manual, secrets-gated) | 🔴 absent | Add per doc 07 §9: throw-away account, CPU only, concurrency 1, `LIVE_E2E_ENABLED` kill switch (repo variable), no fork PRs, protected-branch secrets, 60-min orphan sweeper, `colab sessions` must end empty. |

### 4.2 Cross-cutting CI gaps

| Gap | Evidence | Next action |
|---|---|---|
| `golangci-lint` not run in CI | `ci.yml` has `go vet`, `go test`, `govulncheck`, `go mod verify` only; `make lint` exists (`Makefile:17-18`) but no workflow calls it. The reported "0 issues" is a local result that CI cannot enforce. | Add a `lint` job running `golangci-lint run ./...` with a pinned version; make `make check` include `lint`. |
| `forbidigo` missing | `.golangci.yml:6-15` does not enable `forbidigo`, despite doc 04 §2 and doc 06 WP-01 requiring it. `scripts/check-paths.sh:8-10` is the substitute but is unwired. | Wire `scripts/check-paths.sh` and `scripts/check-redaction.sh` into `make check` and a CI step; optionally enable `forbidigo` for `filepath.` in `internal/contents`. |
| No coverage gate | No `-coverprofile`; docs/07 §11 requires ≥80% for `colabapi`, `runtime`, `state`, `sshx`, `auth` and 100% of the error-mapping table. | Add `go test -coverprofile=coverage.out -coverpkg=... ./...` + a script that fails below thresholds; upload to Codecov with a pinned action. |
| No `goleak` | No import of `go.uber.org/goleak` anywhere. | Add `TestMain` with `goleak.VerifyTestMain` in `runtime`, `sshx`, `repl`, `update` (the websocket/goroutine-heavy packages). |
| No fuzz CI / corpora | `FuzzDecodeFrame`, `FuzzSanitize` exist but only as unit-seed runs; no `-fuzz` job. docs/07 §7, doc 08 WP-21 require ≥1 h per decoder. | Add a nightly job with `-fuzztime=1h` and commit generated corpora under `testdata/fuzz/`. |
| No upstream-sync cron | docs/06 §5.1 requires a weekly diff issue from upstream Releases/CHANGELOG. | Add `upstream-sync.yml` cron (weekly), `gh api` for latest tag, diff `upstream/CHANGELOG.md`, open an issue labelled `upstream-sync`. |
| No monthly wire re-capture | docs/07 §5.7 and docs/06 §5.4 require monthly re-capture; `testdata/wire/` is empty. | After a human performs S0 (TLS key log + throw-away account), commit redacted fixtures and add a monthly reminder workflow that fails if fixtures are older than 35 days. |
| No property-based tests | No `testing/quick`, `gopter` or `rapid` imports; docs/07 §7 lists seven property targets (state crash safety, POSIX quoting round-trip, accumulator model, redaction generation). | Start with `FuzzQuoteRoundTrip` against real `sh -c` and a generated-secret redaction property. |
| No SBOM/license/reproducibility jobs | See §3. | Add a `compliance` workflow. |
| No branch protection / protected-release secrets | Repo has no commits yet (staged tree only), so no protection can be configured from code. | Document required settings in docs/04 §8: branch protection, 2FA, required status checks, `PROD_REFRESH_TOKEN` available only to `live-e2e` on manual dispatch. |
| Docs changelog notes | AGENTS.md finalize step requires a dated note in the affected doc; docs/04/06/07 do not carry dated change logs. | Add a short dated "Change log" section to each touched doc in the same commit as the corresponding work item. |

---

## 5. Implementation plan (P0 / P1 / P2)

Sizes follow docs/06 §0: **S** ≤ 1 day, **M** 2–3 days, **L** 4–6 days. Every item is test-first: write the failing test with the ported upstream test name in a comment, observe red, then implement.

### P0 — unblock truthfulness and the release path (do first)

| ID | Deliverable | Files to add/change | Test-first step | Size | Depends on |
|---|---|---|---|---|---|
| P0-1 | Fix CI cross-build output path + pin Actions | `Makefile:22-26`, `.github/workflows/ci.yml` | Run `make cross` in a clean container with `/tmp/opencode` absent → observe failure; assert fix builds into `dist/`. | S | – |
| P0-2 | Wire all existing gates into CI | `Makefile:20`, `.github/workflows/ci.yml` | Delete the `lint`/script steps locally and show CI currently passes without them; add them and assert a deliberate violation fails. | S | P0-1 |
| P0-3 | `CleanupOld` startup call + Windows update proof | `cmd/golab/main.go` or `internal/app/app.go`, `internal/update/install_test.go` | Test: seed `fake.exe.old`, call startup path, assert removal; red because no call site exists. | S | – |
| P0-4 | Windows owner-only ACL | new `internal/platform/private_windows.go` + `private_windows_test.go`, `platform.go:132-139`, `platform_test.go:53-56` | Test parses the DACL of a `MakePrivate`-treated file and asserts owner-only; red on Windows because no-op. | M | P0-2 |
| P0-5 | GoReleaser config + `release.yml` (cosign + signed SBOM) | `.goreleaser.yaml`, `.github/workflows/release.yml` | Test: virtual-Sigstore test that the release metadata naming matches `app.go:211` and `check.go:192-213`; then a dry-run `goreleaser release --snapshot` and assert all three asset classes exist. | L | P0-1, P0-3 |
| P0-6 | Resolve module/binary identity (DEC-01) — **DONE** | `go.mod:1`, all imports, `Makefile:9`, `docs/08 §2`, `docs/COMPAT.md` | Resolved as `github.com/20Youssef10/golab`, binary `golab`; test: `go install github.com/20Youssef10/golab/cmd/golab@latest` in a clean GOPATH, then `golab version` prints `golab 0.2.0 (compatible with google-colab-cli 0.8.x+HEAD)`. | M | – (resolved) |
| P0-7 | Live e2e skeleton with kill switch and sweeper | `.github/workflows/live-e2e.yml`, `docs/07 §9` checklist | Test: with `LIVE_E2E_ENABLED=false`, job exits 0 and allocates nothing; with `true`, a fake-server smoke runs first. Human must run the first real pass. | L | P0-5 |

### P1 — close the conformance layers

| ID | Deliverable | Files to add/change | Test-first step | Size | Depends on |
|---|---|---|---|---|---|
| P1-1 | Golden harness + DELTAS ledger | `internal/cli/golden_test.go`, `testdata/golden/**`, `testdata/golden/DELTAS.yaml`, `docs/CONFORMANCE.md` | Write an empty-fixture test that fails for every scenario; generate fixtures from Python via P1-2 and review diffs. | L | P0-2 |
| P1-2 | Differential proxy (L4) | `internal/testutil/proxy/**`, `.github/workflows/differential.yml` | One scenario (`colab version` + `help`) run through both CLIs with normalisation; fail on any unlisted diff. | L | P0-7 (Python CLI install) |
| P1-3 | Port remaining high-risk upstream groups | `internal/cli/execution_test.go`, `internal/cli/ssh_windows_test.go`, `internal/cli/automation_test.go`, `internal/sshx/**_test.go` | Port `test_run.py` exit-code table, `test_windows_ssh.py` (5), `test_automation.py` (14), stale-sshd cases, naming each upstream test in a comment. | L | P1-1 |
| P1-4 | State crash-safety / lock semantics | `internal/state/state_test.go`, new `internal/testutil/statewriter/main.go` | `kill -9` a writer mid-write from a parallel process, re-read 100 times, assert parseable and no lost session; red today. | M | – |
| P1-5 | Coverage + goleak + fuzz CI | `Makefile`, `ci.yml`, new `.github/workflows/nightly-fuzz.yml`, `TestMain` in 4 packages | Set thresholds from docs/07 §11 and watch CI fail on current numbers; add goleak and 1 h fuzz jobs. | M | P0-2 |
| P1-6 | WSL/Termux URL launchers | `internal/platform/platform.go` (+ `IsWSL`), `internal/cli/utility_extra.go`, tests | Table test for `wslview`/`explorer.exe`/`termux-open-url`/print-URL fallback with an injectable exec seam; red for WSL/Termux cases. | S | P0-4 (platform test conventions) |

### P2 — platform breadth and supply-chain polish

| ID | Deliverable | Files to add/change | Test-first step | Size | Depends on |
|---|---|---|---|---|---|
| P2-1 | QEMU smoke job | `.github/workflows/qemu-smoke.yml` | `colab version` + fake-server suite under `qemu-*-static` for four arches. | M | P0-1 |
| P2-2 | Termux nightly (emulator) | `.github/workflows/termux-nightly.yml`, shim tests | Assert resolver/CA/TMPDIR/keyring-fallback behaviour inside Termux; red if no job exists. | L | P1-6 |
| P2-3 | `faccessat2` device probe + optional workaround | doc 04 §3.2 finding, `internal/platform/exec_android.go` if needed | `strace` on real devices; only implement the build-tagged `findExecutable` if SIGSYS is observed. | M | P2-2 |
| P2-4 | Installers + package channels | `install.sh`, `install.ps1`, `.goreleaser.yaml` brews/scoops/winget/nfpms, AUR/TUR docs | CI installs the snapshot artifact in a clean container/VM and runs `colab version`. | L | P0-5 |
| P2-5 | Reproducibility, `go-licenses`, SBOM publication check | `.github/workflows/compliance.yml` | Two builds with fixed `SOURCE_DATE_EPOCH` must have identical digests; fail if SBOM/license artifacts missing. | M | P0-5 |
| P2-6 | macOS keychain argv + timing proof | macOS CI job + release checklist in doc 04 §5 | `ps -ww -ef` during a token write; locked-keychain probe must return < 5 s. | S | P0-7 |
| P2-7 | Upstream-sync cron + monthly wire recapture | `.github/workflows/upstream-sync.yml`, `testdata/wire/**`, docs/06 §5 update | Workflow opens a labelled issue on new upstream tag; fixture-age check fails after 35 days. | M | P1-2 |

**Dependency spine:** `P0-1 → P0-2 → P0-5 → P0-7 → P1-2 → P1-3`, with `P1-1` fed by `P1-2`; `P0-6` blocks any public release channel (`P2-4`). `P0-3`, `P0-4`, `P1-4`, `P1-6` are independent and can run in parallel. Do **not** start `P2-4` before `P0-5` and `P0-6`: the updater's pinned identity and the module path both change the artifact names.

**Exit criteria for this backlog:** every doc 04 §9 item automated or explicitly recorded as a human checklist; `docs/CONFORMANCE.md` populated with ≥360 intent-level cases; one recorded live e2e pass with `colab sessions` empty at the end; a tag whose release contains checksums + cosign bundles + signed SBOM and which `colab update --install` accepts.

---

## 6. Open questions requiring an owner decision

1. **DEC-01 identity:** final module path, repo owner/name, and whether the binary is `colab` or `golab`? This blocks `go install`, install scripts, and the updater's Owner/Repo pins.
2. Is publishing signed artefacts under a third-party GitHub repo acceptable given doc 08 R-02/R-03 (OAuth client reuse and client-agent allow-listing)? The live probe (S0/S1) has not run.
3. Which GitHub runner plans are available for `ubuntu-24.04-arm`, `macos-15-intel`, `windows-11-arm`, and does `go test -race` work on `windows/arm64`?
4. Who owns the throw-away Google account and quarterly refresh-token rotation for L5? Until answered, P0-7 stays a skeleton and no feature can be marked "done".
5. Confirm whether the captured-wire fixtures (S0) were ever produced outside the repo; `testdata/wire/` is empty, so `docs/02` 🔴 items remain unverified.

---

### Evidence index (paths cited)

`Makefile` · `.golangci.yml` · `go.mod` · `.github/workflows/ci.yml` · `cmd/golab/main.go` · `internal/platform/{platform.go,platform_test.go}` · `internal/state/state.go` · `internal/auth/tokenstore.go` · `internal/app/app.go` · `internal/cli/{update.go,ssh.go,utility_extra.go}` · `internal/sshx/{console_windows.go,console_unix.go,signals_windows.go,signals_unix.go,lifecycle.go,session.go,npipe_windows.go,agent_windows.go,agent_unix.go,resize_windows.go}` · `internal/repl/tty.go` · `internal/render/sanitize.go` · `internal/update/{check.go,install.go,verify.go,sbom.go,trust.go,version.go}` · `internal/testutil/{fakecolab,fakekernel,fakesshd}/**` · `scripts/{check-paths.sh,check-redaction.sh}` · `docs/{04,06,07,08,COMPAT}.md` · upstream `tests/**`, `AGENTS.md`, `CHANGELOG.md`, `cloudbuild.yaml`, `pyproject.toml`, `integration/**`.
