---
log:
  2026-11-23: Initial audit. Evidence read from golab working tree and upstream HEAD 0abf82d; 12 innovations ranked; parity gate and measurement plan defined.
---

# AUDIT-04 · Surpass the Python Source

> **Purpose.** Decide where `golab` already beats `google-colab-cli`, and sequence the work that would make it beat the source rather than merely copy it.
> **Method.** Read `README.md`, `docs/01`–`docs/09`, `AGENTS.md`, `go.mod`, and the implementation of every `internal/` package; read upstream `CHANGELOG.md`, `docs/**`, `src/colab_cli/**`, `tests/**`, and `docs/public_api_migration_plan.md` at HEAD `0abf82dadc2b95c02b6ab2c08b1b4d664e616fbb` (2026-11-23). Claims below cite file and line; where a claim needs a live VM it is marked **UNPROVEN**.
> **Baseline.** `go test -count=1 ./...` passes on all packages (2026-11-23); `testdata/{wire,golden,notebooks}` exist but contain **0 files**; no live Colab VM was allocated. Upstream has 6,020 LOC of `src/`, 36 test files / 379 test functions; `golab` has 15,107 implementation LOC, 10,327 test LOC, 311 `Test*` functions plus in-process fake servers.

## Verdict

The Go port's lead is **real but narrow**: it is stronger at being a *tool* (one static binary, safe state, safe updates, clean streams, cross-platform) and weaker at being a *protocol authority* (no captured wire truth, no live proof, only partial Public API lifecycle). Upstream's lead is that it is the reference implementation: it gets new server features first and has 379 tests against the real behaviour.

The highest-leverage strategy is therefore **not** "more command parity" and **not** "a warm VM pool". It is to become the **agent-safe control plane for Colab**: a stable, versioned, machine-readable event stream; a GET-only, redacting `doctor`/`audit` pair; declarative budgets that block *before* a billable POST; and a complete dual-API implementation that survives the tunnel being retired. Most of that can be proven with fake servers today and does not require a Google account. It does require the parity gate first (§3, Gate 0) — an unproven tool cannot credibly be called superior.

---

## 1. Where `golab` already surpasses upstream

Legend: **Proven** = asserted by code/tests in the working tree; **Unproven** = design present, live behaviour not checked.

| # | Area | `golab` evidence | Upstream evidence (HEAD `0abf82d`) | Why it wins | Proof |
|---|---|---|---|---|---|
| 1 | **One static binary, no runtime** | `Makefile:2,9` (`CGO_ENABLED=0`, `-trimpath`); `cmd/golab/main.go`; `go.mod` has no Python-equivalent runtime requirement | `pyproject.toml` requires Python ≥ 3.12; ships via PyPI/Cloud Build | No interpreter, venv, or `pip` on the target; install is a file copy | Proven (cross-compile matrix in `Makefile:22-26`; not yet run in this session) |
| 2 | **Embedded SSH; no external OpenSSH** | `internal/sshx/doc.go:2`, `session.go:67-71` (`ssh.NewClientConn` over a WS `net.Conn` from `wire.go:121`), `keys.go:50-99` native key discovery; Windows/Unix variants for agent, console, resize, signals | `commands/ssh.py:92-98` shells out to `ssh-keygen -y`; `:488` builds a `ssh --proxy-mode` ProxyCommand; `:547,573` use `subprocess.call(ssh_args)` | Removes the largest install prerequisite and the Windows `list2cmdline` quoting class of bugs; `--proxy-mode` is kept as an escape hatch | Proven by `fakesshd` tests; **Unproven** against a real VM |
| 3 | **Atomic, fail-loud state** | `internal/state/state.go:95-105` (cross-process `flock`), `:262-299` (temp → `chmod 0600` → `fsync` → `.bak` hard-link → atomic rename), `:25-29,220-226` `ErrCorrupt` on parse failure, `:301-320` name validation + Windows reserved names | `docs/01 §10 D-1/D-2`: `_load_raw` swallows parse errors and truncates in place; session names reach `history/<name>.jsonl` unchanged | No silent session loss, no path traversal; corrupt data aborts instead of becoming `{}` | Proven (`internal/state/state_test.go`) |
| 4 | **Typed errors, no string sniffing** | `internal/colabapi/errors.go:19-63` (`StatusError`, `ErrTooManyAssignments`, `ErrNoQuota`, `errors.As`), `runtime/errors.go`, `cli/errors.go` central map | `utils.py:38-47` does `"404" in err_msg or "401" in err_msg`; `client.py:317-324` raises one catch-all `ColabRequestError` | Callers branch on identity, not on a status number embedded in prose; safe to refactor messages | Proven |
| 5 | **Explicit timeouts on every HTTP path** | `colabapi/client.go:32-33,121-155` (60 s total, 5 s dial, 10 s TLS, 30 s header, 90 s idle); `contents/contents.go:28-35,547-587`; lock timeouts `state.go:110` | `client.py:300-305`: a timeout exists only when `COLAB_CLI_REQUEST_TIMEOUT` is set (docs/02 §4 records "unset = no timeout"); `docs/01 §10 D-5` | No unbounded hangs in CI or agents; the only unlimited wait is the documented `--timeout 0` | Proven |
| 6 | **Typed secret redaction, logging off by default** | `internal/logx/logx.go:18-45` `Secret` (`String()`/`LogValue()` → `REDACTED`), `:62-71` `Setup` writes to `io.Discard` unless asked, `:73-119` typed + free-form redaction; `state.go:42` stores tokens as `Secret`; redaction goldens in `auth/auth_test.go:35,689`, `history_test.go:247`, `sshx/wire_test.go:177` | `common.py:242-247` sets the root logger to `logging.DEBUG` on every invocation; `:204-237` a single regex does the masking; `CHANGELOG.md:42-46` is a retrospective "logs may contain credentials, revoke access" notice | Defence in depth: even a new log call cannot print a token by accident | Proven (goldens); **Unproven** for real wire bodies |
| 7 | **Pinned, cryptographically verified self-update** | `internal/update/verify.go:17-147` (SHA-256 + cosign bundle, Fulcio chain, Rekor inclusion proof + SET, pinned OIDC identity / workflow SAN, in-toto digest), `sbom.go` requires a signed SBOM; pinned owner/repo in `app.go:88-93` | `auto_update.py:156` fetches `settings.update_url` (user-editable); `:141-142,235-246` runs `uv tool install` / `pip install --upgrade` with no signature check (`docs/01 §10 D-9`) | A compromised URL or PyPI mirror cannot install code into a tool that holds Google tokens | Proven by fake/tamper tests; **Unproven** for a real release (TUF refresh + live release still need a human, `docs/03:212`) |
| 8 | **Terminal-output sanitiser** | `internal/render/sanitize.go:46-300`: resumable byte state machine, keeps SGR, strips OSC/DCS/APC/PM and query CSI, caps CSI params at 128, preserves split UTF-8; `repl/sanitize.go` mirrors it for the REPL | `utils.py` renders `display_data` with Rich `Text.from_ansi`; no terminal-query/OSC filtering; `docs/08 R-15` lists the risk upstream has not closed | Malicious or buggy notebook output cannot write the local clipboard/title or provoke a terminal reply | Proven by unit tests |
| 9 | **Windows and Termux as first-class targets** | `platform.go:25-40` (`%APPDATA%`), `:85-113` (`$TMPDIR`/`$PREFIX/tmp`, detection), `:144-186` (DNS + embedded-CA shim); `sshx/console_windows.go`, `resize_windows.go`, `signals_windows.go`, `npipe_windows.go`, `agent_windows.go`; `go.mod:24` embedded x509 roots; `docs/04` | `README`: "supports Linux and macOS only. Windows is not supported"; partial Windows code exists only in the SSH path; no Termux story | Users on the two largest unsupported platforms have a supported client | Proven by code + unit tests; **Unproven** on a real phone/Windows console |
| 10 | **Keyring tiering with a hard 5 s bound** | `internal/auth/tokenstore.go:34-70` (bounded probe), `:72-105` (probe once, cache, `ErrNotFound` = usable-but-empty), `:108-120` (one secret-free stderr notice), `:177-223` (0600 atomic file fallback) | `auth.py:59,66-86,130-141`: 30 s timeout, `_check_linux_keyring_locked` warns but still attempts the keyring; locked collections can stall (`docs/01 §10 D-10`) | Predictable start-up on headless Linux/Termux; no 30 s stall before the first command | Proven (fake backend tests) |
| 11 | **Streaming, chunked file transfer and safe `edit`** | `contents/contents.go:22-25,197-246` (1 MiB chunks, `chunk:-1`, single-shot fallback), `:160-177,508-545` (`ReadStream`), `cli/files.go:246` edits only on 404-new-file | `contents.py` reads the whole file, base64s it, one PUT (`docs/01 §10 D-4`); `files.py` swallows download errors and can overwrite the remote with an empty buffer (D-11) | Multi-hundred-MB uploads without RAM blow-up; no accidental clobber after a network blip | Proven by fake-server tests; chunk acceptance on the real server still 🔴 (`docs/02:189`, spike S6) |
| 12 | **Clean stdout contract for agents** | `docs/03:127-131` and `cli/root.go:133-150`: all diagnostics to `d.Stderr`; piped `ssh` suppresses the update check (`cli/root.go:160-163`); TTY detection is mockable (`platform.go:115-130`) | `docs/01 §7`: `[colab] …` goes to stdout for `exec`/`new`, inconsistent with `run`/`ssh` | Piping `golab exec` into `jq`/a script never mixes chatter with program output | Proven by golden tests |

### Things that are *not* a lead (be honest)

* **Public API migration.** `colabapi/client.go:434-532` (`List` dispatch, `listPublic`), `:635-645` (`CheckAPIAccess`), `state.go:61-63` (three settings fields) and `cli/root.go:116-119,192-204` already match upstream HEAD: *list and probe only*. Both still do `assign`/`unassign` on `/tun/m/*` (`client.go:360-432`). Upstream has the same gap and a detailed plan (`public_api_migration_plan.md §3`). Our advantage is architectural (one idiomatic client, plain values, in-process fakes), not functional. See proposal A1.
* **Test count.** 311 Go `Test*` functions vs upstream's 379. We have more *layers* (fake servers, `-race`, fuzz targets in `docs/07 §7`) but fewer ported cases. `docs/CONFORMANCE.md` does not exist and `testdata/` is empty — the conformance claim in `docs/09:6` is future tense.
* **Fire-and-forget.** Upstream removed its keep-alive daemon on 2026-09-25 (`CHANGELOG.md:99-108`); we have no daemon either. This is parity, not differentiation.
* **REPL.** Ours is an in-house raw-mode editor with history/highlighting (`repl/tty.go:25-88`); upstream uses `prompt_toolkit` + Pygments. Ours is embedded and Windows-aware; it is not yet feature-equivalent (no Vi mode, no fuzzy history, no paste bracketed-mode matrix run).

---

## 2. Prioritised innovation backlog

Priority ranks P0 → P3 mean *sequence*, not importance. Every item states a decision; rejected items still give the narrow accepted variant, a test-first plan and the metric that would reverse the decision. Sizes follow `docs/06 §1` (S ≤ 1 day, M 2–3 days, L 4–6 days).

### Summary

| Rank | ID | Proposal | Decision | Effort | Primary success metric |
|---|---|---|---|---|---|
| P0 | A1 | Complete Public API lifecycle ahead of upstream | **Accept** | L | `new→status→exec→stop` works with `UsePublicAPI=true` and **zero** `/tun/m/assign` calls; no orphan runtimes |
| P0 | A2 | `--output json` / NDJSON event stream | **Accept** | M | 100 % of P0/P1 commands emit schema-valid events; 0 secret fields in 10 k fuzzed events |
| P0 | A3 | `--explain` + `--yes` for billable ops | **Accept** | M | `--explain` makes 0 requests and matches the real request log on 20 scenarios; 0 POSTs without `--yes` when guarded |
| P0 | A4 | `golab doctor` (GET-only probe) | **Accept** | M | 0 POSTs in every probe test; kill switch blocks all live modes |
| P1 | A5 | `golab audit` (security UX) | **Accept** | S/M | Detects all 11 seeded hygiene defects; 0 secret substrings in output |
| P1 | A6 | `golab.yaml` profiles + accelerator budgets/cost guards | **Accept, with advisory-only estimator** | M | 0 allocation POSTs when a guard trips; no invented price constants |
| P1 | A7 | `golab sync` + git-friendly notebook diffs | **Accept (one-shot + diff); defer watch** | M/L | 100-file round-trip, 0 unrequested deletes; notebook diff ignores outputs/metadata by default |
| P1 | A8 | Terminal images: size caps + iTerm2 fallback | **Accept (Kitty/iTerm/file); sixel deferred** | S/M | 0 oversized escape sequences; piped stdout byte-identical |
| P1 | A9 | Parallel uploads + resume + progress | **Accept behind a flag, sequential default** | M | 1 GiB upload with bounded memory; 2 injected failures resume; 0 progress bytes on piped stdout |
| P2 | A10 | Reproducible execution bundles + provenance | **Accept, opt-in cache only** | L | Identical manifest hash on repeat; 0 cache hits for impure inputs |
| P2 | A11 | MCP server (stdio) for agents | **Accept after v1.0** | L | Agent completes `new→exec→stop` via MCP; 0 secrets in transcript; 100 % billable tools gated |
| — | A12 | Plugins / pre-post-exec hooks | **Reject as arbitrary code; accept data-only renderer selection** | S (narrow) | 0 `exec`/`plugin_load` calls in the CLI binary; renderer switch changes formatting only |

---

### A1 · Complete the Public API lifecycle ahead of upstream — **ACCEPT** (P0, L)

**Value proposition.** Upstream's own migration plan is unfinished (`public_api_migration_plan.md §1-3`): `assign()` and `unassign()` still call `/tun/m/*`; only `list` and the probe use `/v1beta/runtimes`. If Google retires the tunnel, upstream breaks; a client that already supports create/delete via the Public API becomes the only working CLI. This is the single most defensible "beat the source" claim.

**Target user.** Anyone whose credentials lack tunnel access but have `colaboratory.googleapis.com` (ADC with a quota project, custom OAuth client), and operators planning for tunnel deprecation.

**Concrete design.**
1. Extend `colabapi.Client` with `AssignPublic(ctx, AssignRequest)` and `UnassignPublic(ctx, name)` while keeping the existing interface methods for the tunnel. Wire dispatch on `Config.UsePublicAPI` through all verbs, not just `List` (`client.go:435-440`).
2. Implement the LRO per upstream's plan: `POST /v1beta/runtimes?runtimeId=r-<uuid>&requestId=<uuid>` with a `runtimeSpec` body; poll `GET /v1/{operation.name}:wait?timeout=30s` with a hard context deadline; map `operation.error.code` 9→`ErrTooManyAssignments`, 6→`GetRuntime` recovery, 3/7→typed 400/403 errors. **Do not** copy upstream's "catch every `Exception`" probe style.
3. Add `runtime_name` to `state.Session`; persist `runtimes/r-<id>` alongside `endpoint`; match by `runtime_name` first, `endpoint` second in `app.GetSession`/`SyncSessions` (`app.go:250-374`). Deleting by endpoint instead of resource name is the exact trap the plan calls out (`migration_plan §2, §4`).
4. Keep `ccu-info` on the tunnel in both modes (as upstream does) and make that explicit in the doctor output.
5. Auto-fallback: on a typed 403 from either backend, consult `Settings.PublicAPISupported`/`TunnelAPISupported` and offer the other path; never silently retry a billable POST.

**Files.** `internal/colabapi/{client.go,types.go,errors.go}`; `internal/state/state.go`; `internal/app/app.go`; `internal/cli/{session.go,root.go,utility_extra.go}`; `internal/testutil/fakecolab/fakecolab.go`; `docs/02`, `docs/COMPAT.md`.

**Test first.** In `colabapi/client_test.go`, before implementation: (a) fake server records zero `/tun/m/*` calls under `UsePublicAPI=true`; (b) an `Operation{done:false}` followed by `:wait {done:true,response:Runtime}` yields an `AssignmentResponse`; (c) error codes 9/6/3/7 map to the typed errors; (d) `unassign` sends `DELETE /v1beta/runtimes/r-…?allowMissing=true`; (e) legacy sessions without `runtime_name` still load. Then a CLI-level test: `new → sessions → stop` against a fake dual API with no orphans.

**Risks.** Response shapes are inferred, not captured (🔴); LRO polling can look like a hang and burn quota if the operation never completes — bound it and make `colab stop` idempotent. A wrong `runtimeId` encoding wastes a billable allocation. **Mitigation:** human live check (GET probes first) before any release; all fake fixtures tagged as inferred.

**Effort.** L. **Success metric.** On the fake dual API, a full lifecycle with `--public-api` issues zero `/tun/m/assign` calls and leaves zero assignments; on a live human check, one public-API session is created, refreshed and deleted with `colab sessions` empty at the end.

---

### A2 · Structured JSON / NDJSON output — **ACCEPT** (P0, M)

**Value proposition.** Upstream's only machine-readable surface is `history/*.jsonl` (per-session, after the fact). An agent or CI job today must parse `[colab]` prose and human tables. A versioned event schema on stdout makes `golab` composable and removes the temptation to scrape help text.

**Target user.** CI pipelines, coding agents, notebook orchestrators, dashboards.

**Concrete design.**
* Persistent root flag `--output {text,json,ndjson}` plus `COLAB_OUTPUT` env var; `text` is today's behaviour. `--output json` emits one JSON document (final result + summary); `--output ndjson` emits typed events as they happen: `{"schema":"golab/1","event":"session.created","ts":…,"data":{…}}`.
* A typed event catalogue mirrors `internal/history.EventType` with stable names: `session.created|terminated|pruned`, `exec.started|output|finished`, `file.op`, `api.probe`, `budget.denied`, `upgrade.available`. Schema version is a required field; adding fields is compatible, renaming/removing is a major version.
* **Diagnostics stay on stderr** (`docs/03 §7`) in both modes; JSON on stdout must be parseable with `jq` with no filtering. Tokens are `logx.Secret` and are *never* serialised — serialise a `hasToken: bool` and `expiresAt` instead.
* Implement at `cli` composition level over `d.Stdout`, not inside protocol packages; use one `output.Writer` so every command goes through the same encoder. `run` script output remains raw on stdout; with NDJSON, script output is an `exec.output` event with `stream` and `text` fields (base64 for non-UTF-8).
* Publish a JSON Schema file under `docs/` for consumers; gate it in CI.

**Files.** New `internal/output/{writer.go,events.go,schema.go}`, `internal/output/schema.json`; `internal/cli/{root.go,helpers.go,session.go,execution.go,run.go,files.go,utility_extra.go}`; `internal/history/event.go` (shared names); `docs/` new schema page or section in `docs/03`.

**Test first.** Golden test per P0/P1 command: run with `--output ndjson`, assert every line validates against the embedded schema, assert no line contains a known secret planted in the fake server response; assert stdout parses as a single JSON value in `json` mode; assert an empty/no-output command still emits `exec.finished`; assert `--output json` + piped `ssh` does not corrupt the byte stream (or is rejected with a clear exit 2).

**Risks.** Schema churn is a compatibility promise — freeze it before v1.0. Streaming events can interleave with script stdout; define and test the ordering. Secret leakage through free-form `error` strings — always pass through `logx.Redact`.

**Effort.** M. **Success metric.** 100 % of commands in `docs/05` emit schema-valid output; `jq -e .` succeeds on every golden; 0 secret substrings in a fuzz corpus of 10 k generated events; an external agent script can drive `new → exec → stop` using only events.

---

### A3 · `--explain` dry-run and `--yes` gating for billable operations — **ACCEPT** (P0, M)

**Value proposition.** Upstream allocates a real, billable VM on `new`, `run` and auto-creating `ssh` with no dry-run and no confirmation. `docs/01 §8.6` records that validation must happen before spending money, but a typo still costs a VM. A plan command and explicit confirmation are a genuine safety improvement no Python client has.

**Target user.** Automation authors, cost-conscious users, anyone debugging flags.

**Concrete design.**
* Extract a `Plan` struct from the same config path used by the real command (resolved session, chosen API path, method + path + redacted query keys, accelerator/shape, estimated cost source, cleanup action, whether a VM will be created). `--explain` prints it and exits **without any network I/O**, including no auth refresh and no update check.
* `--yes` (and `COLAB_ASSUME_YES=1`) is required for: `new`, `run` (unless reusing `-s`), `ssh` auto-create, `stop --all`, and any command tripped by an A6 budget guard. On a TTY without `--yes`, prompt; when stdin/stdout is not a TTY, fail with exit 2 and a one-line hint. Non-billable commands ignore the flag.
* Make `--explain` work with `--output json` so an agent can inspect the plan and then submit the identical command with `--yes`.

**Files.** New `internal/app/plan.go`; `internal/cli/{root.go,session.go,run.go,ssh.go,execution.go,helpers.go,errors.go}`; `internal/output` (from A2).

**Test first.** Fake control plane counts every request: `--explain` must make zero. Golden plan output for 20 scenarios (CPU/GPU/TPU, high-mem, `--public-api`, reattach, auto-create, `stop --all`). Test that a guarded `new` without `--yes` makes zero POSTs, that `--yes` makes exactly one, and that the request path/query in the plan equals the request log of the real run.

**Risks.** Plan/real drift if the plan is built by a different code path — mitigate by constructing `Plan` inside the existing command handlers and returning early. `--yes` can become muscle-memory; keep the gate only for irreversible/billable actions.

**Effort.** M. **Success metric.** 0 requests on all `--explain` paths; plan matches real request log on 20/20 scenarios; 0 POSTs in guarded flows without explicit consent.

---

### A4 · `golab doctor` — GET-only, redacting protocol probe — **ACCEPT** (P0, M)

**Value proposition.** `docs/02 §7` has eight 🔴 unknowns and `docs/08 §5` has seven open questions. Upstream's `test-public-api` answers one of them and nothing else. A built-in diagnostic that is *provably* read-only and *provably* secret-free turns research TODOs into repeatable checks, and gives users a first line of support.

**Target user.** Maintainers doing live capture, support triage, agents deciding which API path to use.

**Concrete design.**
* `golab doctor [--section {config,auth,api,update,wire}] [--live]`. Default is **offline**: config paths and modes, `runtime.GOOS/GOARCH`, Termux detection, keyring tier (name only, with probe duration), token-store file modes, presence/absence of a refresh token (never its value), update verification status from the last check, fixture inventory.
* `--api` (or `--live --api`) performs **GET-only** requests: `GET /v1beta/runtimes`, `GET /tun/m/assignments`, `GET /tun/m/ccu-info`, `GET` the token-info endpoint. Reuse `colabapi.CheckAPIAccess` (`client.go:635-645`) and persist the same settings fields upstream uses. No POST, no `assign`, no kernel calls.
* Hard kill switch: live mode requires both `--live` and `GOLAB_LIVE_PROBES=1`; CI/repo variable `LIVE_E2E_ENABLED=false` disables it (`docs/07:105`). Every request has a 10 s deadline; every body goes through `logx.Redact` and a 2 KiB cap (`colabapi/errors.go:65-76`).
* Output supports `--output json` (A2); every check has `{name, status: pass|fail|skip, detail, remediation}` and a stable exit code (0 all pass, 1 any fail, 2 usage/kill-switch).
* Be explicit about what it **cannot** do: it is not a MITM capture tool. Full S0 traffic capture still needs a human with a TLS key log and `mitmproxy` (`docs/07 §5`); `doctor` validates the resulting fixtures and the environment, not the wire bytes.

**Files.** New `internal/cli/doctor.go` and `internal/doctor/{check.go,api.go,report.go}`; `internal/cli/root.go` (flags + suppressed list at `:182-190`); `internal/testutil/fakecolab/fakecolab.go`; `docs/02` (🔴 checklist), `docs/07`.

**Test first.** Fake-server assertion: zero non-GET methods for every section, zero requests at all without `--live`, zero requests when the kill switch is off. Redaction golden with planted tokens in headers, bodies and `/assignments` fields. Timeout test with a hanging fake. Fixture-inventory test: report lists 0 captured wire files today (so nobody mistakes fakes for proof).

**Risks.** A "read-only" command that accidentally calls `assign` would allocate a billable VM — the request-method assertion is a blocking CI gate. Token-info is an external endpoint; treat its failure as `skip`, not `fail`.

**Effort.** M. **Success metric.** 0 POSTs across all doctor tests; kill switch blocks 100 % of live attempts; output contains 0 secret substrings; after the human S0 capture, `docs/02 §7` has 0 🔴 items and doctor validates all 8 fixtures.

---

### A5 · `golab audit` — security UX — **ACCEPT** (P1, S/M)

**Value proposition.** Our security controls are better than upstream's but invisible: a user cannot tell whether their token is in the keyring or a 0600 file, whether their ADC scopes are too broad, or whether the last update was signature-verified. An `audit` command makes the lead legible and gives support a reproducible report.

**Target user.** Security-conscious users, admins, support engineers.

**Concrete design.**
* Local-only by default: config dir and file modes (Unix `0600`/`0700`, Windows owner-ACL via a real ACL check, not the current no-op `platform.MakePrivate` at `platform.go:132-139`), history/log rotation sizes, keyring tier + probe latency, presence of a legacy `token.json`, session names against `state.ValidateName`, `--config` path sanity, updater config (pinned owner/repo, last verification result from settings cache).
* Optional `--online` performs the doctor token-info GET and compares granted scopes to `auth.Scopes`; warns on `cloud-platform`/`drive.file` being present when unnecessary — but does not log the token (`whoami` at `cli/utility_extra.go:193-207` is the pattern to follow).
* One finding per line with `{severity, id, detail, remediation}`; exit 0 clean, 1 warnings, 2 failed hard check. No "pass" for an unverifiable check — mark `unknown`.

**Files.** New `internal/cli/audit.go`, `internal/audit/{audit.go,findings.go}`; `internal/platform/platform.go` (Windows ACL read-back); `internal/update`; `internal/auth/scopes.go`; `docs/08`.

**Test first.** Build a fixture filesystem with 11 seeded defects (world-readable state, 0644 token file, invalid session name, legacy token file present, broad scopes, unverified update, etc.), run audit against it, assert 11 findings and stable exit codes; assert no secret substring for planted tokens; assert `unknown` for checks whose backend is unavailable.

**Risks.** "Scope minimisation" advice can be wrong without a live probe (DEC-10); phrase as advisory, never as a hard failure. Windows ACL inspection needs OS-specific tests.

**Effort.** S/M. **Success metric.** 11/11 seeded defects detected; 0 secret substrings in output; exit codes stable across Linux/macOS/Windows/Termux fixtures; users can answer "where is my token and how is it protected" from the report alone.

---

### A6 · Declarative `golab.yaml` profiles, accelerator budgets, cost guards — **ACCEPT (advisory estimator only)** (P1, M)

**Value proposition.** Upstream has flags and no notion of a project's policy. A checked-in config with profiles and budgets gives teams reproducible sessions and stops accidental spend — the highest-value workflow feature for CI.

**Target user.** ML teams, CI owners, agents running unattended jobs.

**Concrete design.**
* `golab.yaml` (or `--config-yaml PATH`) with `version: 1`, `profiles: {name: {accelerator, shape, timeout, env, remoteRoot, budget: {maxHourlyRate, maxSessionCount, requireYes}}}`, and a `defaults` block. Precedence: CLI flag > env > profile > built-in default; print the resolved source with `--explain`.
* Validate **before** any allocation, reusing the existing flag validation and `state.ValidateName`; fail with exit 2 on unknown keys, bad accelerators or an unknown `version`.
* Cost guard inputs are only things we actually know: `GET /tun/m/ccu-info` gives current balance, hourly rate and active count (`colabapi/client.go:534-552`). The guard denies (or requires `--yes`) when `maxSessionCount` would be exceeded, when `maxHourlyRate` is set and the account's current rate already exceeds it, or when the requested accelerator is not in the profile allowlist.
* **Reject a pre-allocation "estimated price per GPU" table.** No verified per-accelerator price list exists in the sources we read; inventing one would be misinformation. If a user supplies their own `rates:` map, compute an *advisory* estimate labelled `user-supplied, unverified` and never use it as a hard gate.

**Files.** New `internal/config/{config.go,validate.go}`; `internal/cli/{root.go,session.go,run.go}`; `internal/colabapi/types.go` (reuse `Consumption`); `go.mod` already has `go.yaml.in/yaml/v3` as an indirect dependency — promote to direct **and** add a one-line justification in `docs/DEPENDENCIES.md`.

**Test first.** Table-driven config validation tests (unknown keys, bad names, profile inheritance, precedence). Fake-server test: a profile with `maxSessionCount: 1` and one active assignment makes **zero** POSTs on `new`; `colab run` validates the script path before reading budgets (existing order preserved). Golden `--explain` output shows the resolved profile and the cost source. Test that an unverified user rate is labelled as such.

**Risks.** YAML adds a parser and a second config surface; keep it strictly optional. `ccu-info` is account-wide and may lag — never present the estimate as a charge. Upstream could add its own config; keep our schema small and versioned.

**Effort.** M. **Success metric.** 0 allocation POSTs when any guard trips, in fake tests; 100 % of config examples in the docs validate; no numeric price constants appear in our source or docs without a cited source.

---

### A7 · `golab sync` + git-friendly notebook diffs — **ACCEPT one-shot + diff; defer watch** (P1, M/L)

**Value proposition.** Upstream has file commands but no synchronisation and no notebook-aware diff; `history log` exports notebooks but does not make them reviewable. Git-friendly notebook diffs and a safe one-shot sync are a real developer-workflow win; a watch daemon is not (fire-and-forget mandate).

**Target user.** Notebook developers using git, teams moving data between local and VM.

**Concrete design.**
* `golab sync LOCAL REMOTE [--direction up|down|both] [--dry-run] [--delete]`: manifest in the cache dir keyed by `sha256(session name + remote path + size)`, with local mtime and remote model metadata. Default is `--dry-run`; `--delete` is required to remove anything. Remote paths use `path`, local paths `filepath` (`AGENTS.md:24`). Use `contents.Client` streaming (`contents.go:74-77`).
* Conflict rule: if both sides changed since the manifest, fail with exit 1 and show both timestamps — never overwrite silently. The Jupyter Contents API has no server version/ETag we can rely on, so "remote unchanged" must be verified by re-reading and hashing, which is O(n) but safe; document this.
* `golab nb diff A.ipynb B.ipynb [--include-outputs]`: canonicalise with `internal/nb` (stable key order, strip `execution_count`, `outputs`, volatile metadata, trailing whitespace), emit unified diff; `--include-outputs` opts back in. This is independent of a live VM and can ship early.
* **Watch mode is deferred to P2** and must be a foreground process tied to the command's lifetime (no background daemon); a 2 s polling fallback is acceptable when `fsnotify` is unavailable.

**Files.** New `internal/cli/sync.go`, `internal/sync/{manifest.go,plan.go,apply.go}`; `internal/nb/canonical.go`; `internal/cli` registration; new dependency `github.com/fsnotify/fsnotify` only if watch is implemented (justification in `docs/DEPENDENCIES.md`, `govulncheck` clean).

**Test first.** Fake contents service with deterministic bytes: 100-file round-trip, idempotence (second sync makes no writes), conflict case (both changed) does not clobber, `--delete` required for removals, traversal attempt (`../`) rejected. Notebook golden: two notebooks differing only in outputs/execution_count produce an empty diff; a code change produces a one-hunk diff.

**Risks.** Remote edits made in the Colab browser UI are invisible without re-hashing; sync is eventually consistent, not a VCS. Large directories mean many requests with explicit timeouts. Watch mode can leak goroutines — `goleak` gate.

**Effort.** M for one-shot + diff; L with watch. **Success metric.** 100-file round-trip with 0 unrequested deletes and 0 clobbered conflicts; 0 writes on an unchanged second sync; notebook diff ignores outputs by default; no goroutine leaks in watch tests.

---

### A8 · Terminal images: size caps, iTerm2 fallback, sixel deferred — **ACCEPT (partial)** (P1, S/M)

**Value proposition.** `render/image.go:42-110` emits Kitty escapes with no size limit and no terminal negotiation, and always writes a temp file. A 40 MB plot can flood a terminal or a PTY; iTerm2 and sixel users get nothing. This is a contained improvement on an existing differentiator (safe rendering).

**Target user.** Interactive REPL users on iTerm2/WezTerm/Kitty; anyone receiving large plots.

**Concrete design.**
* `render.Image` negotiates from `TERM`, `TERM_PROGRAM`, `KITTY_WINDOW_ID`, `WT_SESSION`, `TERMUX_VERSION`; emit Kitty when available, iTerm2 `OSC 1337;File=…` next, otherwise only the file path. Keep non-TTY suppression (`image.go:60-80`) byte-exact.
* Enforce a configurable byte cap (default 8 MiB) and a pixel cap where dimensions are known; on overflow save to `--output-image`/temp and print a one-line notice to **stderr**. Never emit an escape for an image that exceeds the cap.
* Sanitise any filename printed back to the terminal through `render.Sanitize` so a remote-controlled path cannot inject OSC.
* **Sixel deferred:** no maintained pure-Go sixel encoder is in `go.mod`; adding one needs a dependency justification and a terminal matrix run. Do not shell out to `chafa` (`AGENTS.md`: no external binary dependencies on hot paths).

**Files.** `internal/render/{image.go,image_test.go}`; `internal/platform/platform.go` (terminal detection); `docs/04` terminal matrix.

**Test first.** Golden bytes per terminal profile (Kitty, iTerm2, unknown, non-TTY), oversize input (assert no escape and a file path), split/malformed image data, sanitizer interaction with a malicious temp path; verify piped stdout is byte-identical to today's behaviour.

**Risks.** Terminal detection heuristics are imperfect; always prefer the file fallback when unsure. iTerm2's escape is an OSC — the sanitiser runs on *incoming* remote output, not on our own generated escapes; keep the two paths separate and test both.

**Effort.** S/M. **Success metric.** 0 escape sequences emitted above the cap; piped stdout byte-identical on 100 % of golden cases; manual matrix shows images in ≥ 2 terminal families; 0 new dependencies.

---

### A9 · Parallel uploads, resume, progress — **ACCEPT behind a flag; sequential default** (P1, M)

**Value proposition.** `contents.WriteStream` (`contents.go:197-246`) is streaming but sequential with no progress or resume. Large uploads are the slowest user-visible operation and the most likely to fail mid-way.

**Target user.** Users moving datasets/models to and from a VM.

**Concrete design.**
* Keep `chunk:1…N, -1` order as the default. Add `--parallel N` (default 1) and `--resume`; only allow out-of-order chunks when `--parallel > 1` **and** a fake/live test proves the server accepts it. Until spike S6 is done, parallel mode is experimental and labelled as such.
* Resume manifest in `platform.CacheDir()` keyed by `sha256(session + remote path + size + chunkSize)`, storing completed chunk indexes. On retry, skip those chunks; verify the final `chunk:-1` succeeds before deleting the manifest.
* Progress on stderr only, TTY-gated via `platform.IsStderrTTY`; a deterministic counter format for non-TTY logs (`[colab] upload 42/100 chunks`). No ANSI when piped.
* Apply the same resume logic to `download` via atomic temp + rename (already in `cli/files.go`).

**Files.** `internal/contents/{contents.go,resume.go}`; `internal/cli/files.go`; `internal/platform/platform.go`; `docs/02 §3.8`, `docs/07 S6`.

**Test first.** Fake contents server records chunk order; injected failure after chunk 5 then `--resume` must send only 6..N; a corrupt manifest is ignored; `--parallel 4` (feature-flagged) never exceeds 4 in-flight PUTs; progress bytes go to stderr and never to stdout; a 1 GiB sparse source upload keeps peak RSS under a fixed bound.

**Risks.** Out-of-order chunk semantics are 🔴 unverified — this is why parallel is opt-in. Resume manifests can go stale after a remote edit; include size+hash and treat mismatches as a fresh upload.

**Effort.** M. **Success metric.** 1 GiB upload with bounded memory; resume completes after 2 injected failures; 0 progress bytes on piped stdout; 0 unsolicited deletes of remote files.

---

### A10 · Reproducible execution bundles + provenance — **ACCEPT, cache strictly opt-in** (P2, L)

**Value proposition.** Upstream records events in `history/*.jsonl` but cannot answer "which code and environment produced this output?". Provenance is cheap to add because we already have `history`, `nb` and typed execution results.

**Target user.** Researchers, reproducible-build/CI users.

**Concrete design.**
* `golab bundle record --out bundle.json` captures: code SHA-256 (canonicalised), resolved file list + hashes, kernel fingerprint (`sys.version`, platform, `pip freeze` digest — fetched via one kernel execution), accelerator/shape from the session, `golab` version, upstream compat version, and output digests. Append a `bundle.recorded` event to history.
* `golab bundle replay` re-executes and compares input/output digests; `--cache` is **opt-in per bundle** and only caches when the bundle declares `pure: true` (user assertion, documented as unverifiable by us). No cache by default — kernel state, RNG, network and time make generic caching unsound.
* Store bundles in the config/cache dir with 0600 files; never embed tokens or URLs in a bundle.

**Files.** New `internal/cli/bundle.go`, `internal/bundle/{manifest.go,fingerprint.go,cache.go}`; `internal/history/event.go`; `internal/nb`; docs update.

**Test first.** Deterministic manifest golden (same inputs → identical bytes/hash on repeated runs); changing one input changes exactly the code hash; a `pure:false` bundle never yields a cache hit; a bundle with a token planted in session state emits 0 secret substrings; replay with a changed environment produces a `fingerprint.mismatch` event.

**Risks.** `pip freeze` is slow and may itself import side effects; make fingerprinting opt-in per bundle. Provenance is only as honest as the inputs — label it "recorded", not "verified".

**Effort.** L. **Success metric.** Repeat bundles have identical manifests; 0 cache hits for impure inputs by default; 0 secrets in bundles; replay mismatch detected in 100 % of seeded environment changes.

---

### A11 · MCP server / typed agent SDK — **ACCEPT after v1.0** (P2, L)

**Value proposition.** Agents are already the main consumers of CLIs. A stdio MCP server with typed tools and the same redaction/budget gates would make `golab` the safest way for an agent to drive Colab. Upstream has no SDK or MCP surface.

**Target user.** Coding agents, MCP-capable IDEs, orchestration systems.

**Concrete design.**
* `golab mcp` (stdio JSON-RPC) or a second binary `cmd/golab-mcp`, importing `internal/app` and protocol packages **directly**, never `internal/cli` (ADR: `internal/cli` may import everything; nothing imports `cli`). Pin an MCP spec version in docs.
* One tool per operation with a schema shared with A2's event schema: `doctor`, `audit`, `new`, `list`, `status`, `exec`, `upload`, `download`, `stop`. No shell tool, no arbitrary code, no raw request tool.
* Billable tools require an explicit `confirm: true` argument and return a `Plan` (A3) first; secrets never appear in tool results or errors (`logx.Redact` at the boundary); timeouts on all calls.
* **Do not publish `pkg/colab` before v1.0** (DEC-09): the public library is deliberately postponed. MCP uses internal packages and keeps the public API surface small.

**Files.** New `cmd/golab-mcp/main.go` (optional), `internal/mcp/{server.go,tools.go,schema.go}`; reuse `internal/output` and `internal/app`; `docs/03` ADR addition; `docs/DEPENDENCIES.md` if an MCP SDK is added (prefer in-house JSON-RPC framing to avoid a dependency).

**Test first.** stdio protocol test with scripted JSON-RPC frames; golden tool list; each tool's schema validated; fake-server end-to-end `new → exec → stop`; secret-leak test with planted tokens; billable tool without `confirm` makes 0 POSTs; malformed frames return typed JSON-RPC errors, never process crashes.

**Risks.** MCP spec churn → pin and test. An agent looping on `new` could spend money → budget guard (A6) plus per-process allocation cap. Publishing a stable SDK early creates a compatibility promise we are not ready to keep.

**Effort.** L. **Success metric.** Agent completes a full lifecycle via MCP with 0 secrets in the transcript; 100 % of billable tools require confirmation; tool schemas validate against the published JSON Schema; no `internal/cli` import in the MCP binary.

---

### A12 · Plugin / hook system — **REJECT arbitrary code; accept data-only renderer selection** (S narrow)

**Value proposition (claimed).** Extensibility for output renderers and pre/post-exec hooks.

**Why reject.** Arbitrary plugins would (a) break the "one static binary" advantage, (b) create a new code-execution and secret-exfiltration surface in a tool that holds broad Google scopes, (c) conflict with DEC-11 (single binary) and `AGENTS.md` rule 9 (never splice user strings into code that runs on the VM), and (d) make conformance impossible to promise. Upstream does not have plugins; copying a plugin API would not beat it.

**Accept instead.** A fixed registry of built-in renderers selected by config/flag (`text`, `markdown`, `json`, `ndjson`) plus user-supplied *templates* (data files, no code) for history/log export. This overlaps A2 and can be implemented there.

**Test-first plan (narrow variant).** Config selects each built-in renderer; unknown renderer → exit 2 with the list; a template with `{{.token}}` cannot resolve that field (no token in the data model); renderer switch changes formatting only and never the request log.

**Decision metric to revisit.** Only if ≥ 10 external contributors request a specific hook with a concrete use case that cannot be met by typed output events, and a sandbox/permission model has been designed and security-reviewed. Until then: **no dynamic code loading, no exec hooks**.

**Effort.** S (narrow). **Success metric.** 0 `plugin.Open`/`exec` calls in the CLI binary; renderer selection changes 0 bytes of wire traffic; 0 secret fields reachable from templates.

---

## 3. Phased roadmap

### Gate 0 — close parity gaps before innovating (blocking)

Nothing in §2 may be called "done" until these are closed; fake-server work may proceed in parallel but stays labelled unverified.

1. **Human live e2e** (`AGENTS.md:48-52`, `docs/09:3-8`): `auth`, interactive `ssh`, `drivemount`, a real `run` allocation, token refresh across expiry, chunked upload (S6). Record every endpoint, unassign everything, end with `colab sessions` empty.
2. **Wire fixtures**: `testdata/wire/` is empty. Complete S0 (`docs/07 §5`), convert all eight 🔴 items in `docs/02 §7` to fixtures, delete raw captures, update `docs/02` 🔴→🟢.
3. **Conformance goldens**: `testdata/golden/` is empty and `docs/CONFORMANCE.md` does not exist. Port the upstream test intents per `docs/07 §2`, create `DELTAS.yaml`, make CI fail on unlisted diffs.
4. **Finish signed updates**: full cosign/SBOM verification is implemented but TUF refresh and a real release verification need a human (`docs/03:205-212`). Close the documented SCT gap or keep it explicitly labelled.
5. **Platform proof**: `GOOS=<tier> go build ./...` for every tier; Termux device check; Windows console teardown on all exit paths.
6. **Windows ACL read-back** (`platform.MakePrivate` currently returns nil on Windows) and audit/audit findings from a real owner-only check.

### Phase A — "agent-safe v1.0" (after Gate 0, ≈ 2–3 weeks)

A4 `doctor` → A5 `audit` → A2 JSON/NDJSON → A3 `--explain`/`--yes` → A1 Public API lifecycle (last, because it needs a live check). Order rationale: the diagnostic and schema work is local and testable; the billable lifecycle change goes last so it inherits the safety gates. Exit criterion: an agent can run `doctor`, read a schema-valid plan, and execute a dual-API lifecycle with zero secrets in the transcript.

### Phase B — "workflow v1.1" (≈ 3–5 weeks)

A6 config/budgets → A7 one-shot `sync` + `nb diff` → A8 terminal images → A9 parallel/resume (behind flag). Exit criterion: a checked-in `golab.yaml` drives a budget-guarded `run`; a 1 GiB upload resumes; notebook diffs are clean in a real repo.

### Phase C — "ecosystem v2.0" (≈ 4–6 weeks)

A10 bundles/replay → A11 MCP server (+ optional watch mode for A7, still foreground). Exit criterion: reproducible manifest, agent-driven lifecycle via MCP, no daemon anywhere.

### Explicit "do NOT do this" list

| Item | Why not |
|---|---|
| Reintroduce a keep-alive daemon / warm VM pool | Violates fire-and-forget (`AGENTS.md:10`, upstream `CHANGELOG.md:99-108`); keeps billable VMs alive; upstream's old RPC 403'd for external users; the backend now maintains liveness from kernel activity |
| Spoof `X-Colab-Client-Agent: colab-cli` | R-03/DEC-03: dishonest and may break on allow-listing; send our honest agent (`colabapi/client.go:27`); if rejected, ask upstream, do not impersonate |
| Unverified updates / user-editable `update_url` | Fixes D-9; our signature+SBOM verification is a deliberate lead; never downgrade it to a "convenience" path |
| Live probes without a kill switch, or any POST in `doctor` | Billable VMs, quota limits, R-04; enforce `GOLAB_LIVE_PROBES=1` + `LIVE_E2E_ENABLED`, GET-only, cleanup sweeper (`docs/07 §9`) |
| Treat fakes as live proof | `docs/02` response shapes are inferred from pydantic models; `COMPAT.md:37-41` says so explicitly |
| Invent a per-accelerator price table | No primary source in the repo; an unverified estimate presented as fact would be harmful. Only user-supplied, clearly labelled rates |
| Cache kernel execution by default | Stateful kernels, RNG, time and I/O make results non-deterministic; opt-in `pure: true` only |
| Arbitrary plugins / exec hooks | Security surface + static-binary promise + DEC-11 (see A12) |
| Port `console`, or add a public `pkg/colab` before v1.0 | `console` was removed upstream; DEC-09 postpones the public library |
| `GOOS=android` artefacts | `docs/03` ADR-002: the no-cgo build is a PIE tied to `/system/bin/linker64`; ship static `linux/arm64` + netfix instead |

---

## 4. Measurement plan

### Per-innovation proof matrix

| ID | Unit / fake test (blocking CI) | Golden / conformance | Human live check | Threshold metric |
|---|---|---|---|---|
| A1 | Dual-API fake: zero tunnel calls under `--public-api`; LRO error-code mapping; `runtime_name` round-trip | `new/sessions/stop` goldens for both API paths | One public-API lifecycle + `colab sessions` empty at end | 0 tunnel `assign` calls; 0 orphans |
| A2 | Schema validation of every event; secret-leak fuzz | One NDJSON golden per command; `jq -e .` | Pipe `exec`/`run` output through `jq` on a real VM | 100 % schema-valid; 0 secret substrings |
| A3 | Zero-request assertion for `--explain`; guarded POST count | Plan vs request-log diff for 20 scenarios | One budget-tripped `new` on a throw-away account | 0 requests on explain; 0 POSTs without consent |
| A4 | Zero-POST method assertion; kill switch; redaction golden | Doctor report golden (pass/fail/skip/unknown) | Human S0 capture; doctor validates fixtures | 0 POSTs; 8/8 🔴 resolved after capture |
| A5 | 11 seeded hygiene defects | Audit report golden, 3 exit codes | Audit on Linux, Windows, Termux | 11/11 findings; 0 secrets |
| A6 | Config validation table; guard blocks before POST | Resolved-config `--explain` golden | One guard trip on a live account (GET-only check) | 0 POSTs when tripped; 0 unsourced prices |
| A7 | 100-file round-trip, conflict, idempotence, traversal | Notebook diff goldens | Sync a real notebook dir with the browser UI open | 0 unrequested deletes; 0 clobbered conflicts |
| A8 | Per-terminal escape goldens; size cap; non-TTY byte equality | Piped stdout golden | Terminal matrix (`docs/07 §10`) | 0 oversized escapes; byte-identical pipes |
| A9 | Chunk order, 2-failure resume, RSS bound, progress stream | Upload progress golden | 1 GiB live upload (if quota allows) | 2/2 resumes; bounded RSS; 0 stdout bytes |
| A10 | Manifest determinism, impure cache rejection, secret scan | Manifest + replay goldens | Replay on a real kernel | Identical hash on repeat; 0 impure hits |
| A11 | JSON-RPC scripted frames, tool schemas, confirm gate | Tool-list golden | Agent-run lifecycle on a throw-away account | 0 secrets; 100 % billable tools gated |
| A12 | Renderer-selection tests; no `exec`/`plugin.Open` | Formatting goldens | – | 0 dynamic code loads; 0 wire changes |

### Cross-cutting gates (measured on every PR / release)

* **Conformance.** Zero *unlisted* diffs vs the Python CLI on ≥ 40 scenarios (L4); `docs/CONFORMANCE.md` lists ported upstream test intents and the current count. Target: ≥ 360 equivalent cases before v1.0.
* **Protocol fidelity.** `testdata/wire/*.json` covers C1–C8, kernel REST, WS handshake, Contents and SSH upgrade; 0 🔴 items in `docs/02 §7` after capture; fixtures re-captured monthly and on upstream changes to `client.py`/`runtime.py`.
* **Security.** `logx` redaction golden + fuzz (0 secret substrings in logs, errors, history, JSON events, MCP transcripts); `gosec`, `govulncheck`, `go mod verify` clean; `grep` gate for `InsecureSkipVerify`, host overrides outside `-tags testhooks`, and string-matching of `"404"`/`"401"`.
* **State safety.** N concurrent writers + `kill -9` mid-write: file always parses, no lost sessions (property test in `docs/07 §7`); every `sessions.json`/`settings.json` write goes through `internal/state` (`AGENTS.md:7`).
* **Performance.** Benchmarks for `sanitize`, frame decode, config load and 1 GiB upload; record allocations/op and peak RSS; regression threshold = no >20 % slowdown without an ADR.
* **Platform.** `GOOS=<each tier> go build ./...` (or GoReleaser snapshot) on every platform-touching change; `CGO_ENABLED=0` in CI; no `GOOS=android` artefact; terminal matrix in `docs/07 §10` green for Tier 1.
* **Live safety.** Read-only probes preferred; every created endpoint logged; `defer unassign` + 60-minute sweeper; global live-job concurrency 1; `LIVE_E2E_ENABLED=false` kill switch; final `colab sessions` shows nothing (AGENTS.md:51, docs/07 §9).
* **Upstream sync.** Lag ≤ 2 weeks; weekly diff of upstream `CHANGELOG.md`; `docs/COMPAT.md` updated in the same change as any behavioural divergence.

## 5. Risks to this roadmap

| Risk | Effect | Mitigation / early warning |
|---|---|---|
| Upstream ships its own JSON/MCP surface | A2/A11 lose uniqueness | Keep the differentiator in *typed events + redaction + budget gates*, not the flag name; watch upstream weekly |
| Tunnel API is retired before Public API LRO is proven | A1 becomes urgent; current clients break | Do A1 fake work in Phase A regardless; ask for a human GET probe early; keep tunnel path for as long as it works |
| No live account available | Nothing can be called complete | Build all fake tests, label every claim **Unproven**, and hand the user a short human checklist; do not silently promote fakes to proof |
| `ccu-info` unavailable or lagging | A6 guard fails open or denies wrongly | Treat missing data as `unknown` + require `--yes`; never default to "safe to spend" |
| MCP spec changes | A11 rework | Pin a spec version; keep in-house framing; ship after v1.0 |
| Schema/version churn in A2 | Downstream agents break | Freeze `golab/1` before v1.0; additive-only changes; major version for removals |
| Conformance suite reveals deep divergence | Gate 0 slips | Triage into protocol/CLI/docs/irrelevant per `docs/06 §5`; fix protocol divergences first; record deliberate Δ in `docs/COMPAT.md` |

## 6. Bottom line

`golab` already wins on safety, portability and distribution. It does **not** yet win on protocol coverage, conformance evidence or live proof. The plan that actually beats the Python source is: close the evidence gap, then ship the five agent-safety features (Public API lifecycle, typed output, explain/yes, doctor, audit) while they are still unique; use workflow and ecosystem features to widen the lead only after that. Everything else — daemons, spoofing, unverified updates, arbitrary plugins, default caching — trades a real lead for a liability.
