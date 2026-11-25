# golab — Code Review Report

**Repo:** `github.com/20Youssef10/golab` @ `157ad27` (v0.2.0, `main` clean)
**Scope:** all 24 `internal/` packages + `cmd/golab`, docs pack, scripts, 5 workflows
**Mode:** read-only. No files in the repo modified, no live Colab call, no OAuth consent.

---

## 0. Verification evidence (what is actually proven)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0, clean |
| `go test -count=1 ./...` | exit 0 — 25/25 packages pass (552 `Test*`/`Fuzz*` in 91 files) |
| Total statement coverage | **74.4%** |
| `scripts/coverage-gate.sh` | **FAIL** — `state` 76.77%, `sshx` 74.35% below 80 (gate floored at 70 in CI) |
| `scripts/check-redaction.sh` / `check-paths.sh` | both OK |
| `scripts/goleak-check.sh` | **FAIL** — no dep, no `TestMain` in runtime/sshx/repl/update |
| `golangci-lint` | **not installed on this host** — lint step of `make check` unverified locally |
| `go test -race` | **not runnable here** — TSan aborts: `unsupported VMA range / Found 39 - Supported 48`. Every race finding below is by inspection, not by detector |
| `testdata/wire`, `testdata/golden` | contain only READMEs — **zero fixtures** |

Everything below is code-level evidence. Nothing in this report is live-certified.

---

## 1. Executive summary

The engineering core is genuinely strong: the control plane (XSSI strip, GET-then-POST XSRF, non-retried 503/412, final reconciliation), the frame decoder (offset validation, bounded buffers), the path gates (local `EvalSymlinks`+`Rel`, remote `..`/NUL rejection), the updater verification chain (digest before extraction, SCT/TLog/RFC3161, allowlisted archive members) and the redaction layer all hold up under adversarial reading. There are no TODO/FIXME markers, no `time.Sleep` in production code, and typed errors are used consistently instead of string matching.

The defects cluster in **four bands**:

1. **Spend safety is bypassable in four independent ways** — a paid VM can be orphaned, "stopped" while still billing, allocated without the budget guard, or the guard itself fails open. This is the most consequential cluster because the failure mode is money, not a crash.
2. **Silent false success** — six paths report success (exit 0) after dropping an error: script read failure after allocation, notebook save failure, NDJSON flush failure, `stop --all` per-session failure, `logout` with tokens remaining, `Store.Put` failure on the resume record.
3. **Resume is non-functional in its main scenario** — a run that lost the socket between idle and reply can never complete; it times out and loses an already-finished execution.
4. **Docs have drifted far enough to actively mislead** — README still says v0.1.0 and "no live VM was ever allocated" while CHANGELOG claims a live billable e2e; the binary is `golab` but 14 runtime hints tell users to run `colab`; `go install …/cmd/colab` does not exist.

Counts: **7 P0**, **19 P1**, **21 P2**, **14 P3**.

---

## 2. P0 — fix before the next tag

### 2.1 A paid VM is orphaned when the local record cannot be written
`internal/cli/session.go:117-119`

```go
if err := d.Store.Put(s); err != nil {
    return state.Session{}, err          // no compensating Unassign
}
```

`client.Assign` already succeeded, so a VM exists and is billing. A disk-full, permission or corrupt-store error returns an error with **no `Unassign`**, leaving a running runtime that is invisible to `golab sessions` until a remote sync reattaches it.

**Fix:** on `Put` failure, best-effort `client.Unassign(ctx, res.Endpoint)`, then return an error that names both failures. Add a fake-store test that injects a `Put` error and asserts `Unassign` was called.

### 2.2 `stop --all` reports success while swallowing every failure
`internal/cli/session.go:398-400`

```go
for _, n := range names {
    _ = stopOne(cmd.Context(), d, n, local[n], false)
}
fmt.Fprintf(d.Stderr, "[colab] Stopped %d session(s).\n", len(local))
```

The count printed is `len(local)`, not the number that actually stopped. A user who runs this to stop paying is told everything stopped while the failed ones keep billing. `session_test.go` has **no test for `stop --all` at all** — the most dangerous path is the least covered.

**Fix:** collect per-session errors, print each success, and `return exitf(1, …)` listing failures.

### 2.3 An unreadable script executes an empty payload after allocation
`internal/cli/run.go:64-67`

```go
func buildScriptPayload(script string, args []string, env []envVar) string {
    body, err := os.ReadFile(script)
    if err != nil {
        return ""            // silent
    }
```

The `os.Stat` at `run.go:239` only narrows the window (FIFO, permission change, TOCTOU). On the error path the kernel runs `""` — exit 0, no output, no error. This is the worst class of bug in the repo: it looks like a successful run.

**Fix:** read the script **before** `createSession` (next to `readCodeBlocks`, which already does this correctly for `.ipynb`) and make the function return `(string, error)`.

### 2.4 `ssh` bypasses the budget guard entirely
`internal/cli/ssh.go:126-137` → `autoCreateSSHSession` (`:205-207` sets `CreatesSession: true, Billable: true`)

`guardBudget` is called only from `session.go:179` and `run.go:281`. `ssh` — including `ssh --proxy-mode NAME`, which is an auto-create path — allocates without consulting `golab.yaml`. An `enforce: true` policy is silently unenforced.

**Fix:** call `guardBudget` in the create path before `autoCreateSSHSession`; add a regression test asserting the guard is consulted.

### 2.5 The budget guard fails **open** under enforcement
`internal/cli/explain.go:64-71`

```go
client, err := d.ColabClient(ctx)
if err != nil { return nil }          // fail open
usage, err := client.Usage(ctx)
if err != nil { return nil }          // fail open
```

This contradicts `budget.Check` (`budget.go:64-71`), which *denies* when usage is unknown under `Enforce`. One transient 5xx disables the spend guard that the user explicitly enabled.

**Fix:** when `policy.Enforce`, return `exitf(1, "[colab] budget guard: usage unavailable…")`. Stay silent only when advisory.

### 2.6 A planted token blob can redirect the refresh token to any HTTPS host
`internal/auth/oauth2.go:180-196` (`allowedTokenURI` returns `true` for **any** `https://` host) and `:236-243`:

```go
if s.ClientID != "" { conf.ClientID = s.ClientID }
if s.TokenURI != "" && allowedTokenURI(s.TokenURI) {
    conf.Endpoint.TokenURL = s.TokenURI
}
```

Write access to the token blob without read access to the secret is realistic (cloud-synced config dir, restored backup, devcontainer/template provisioning, `COLAB_CONFIG_DIR` on a shared path). The next run POSTs a **non-expiring Google refresh token** to the attacker's endpoint. `client_id` is also taken from the blob, defeating the embedded-client pin.

**Fix:** allowlist `https://oauth2.googleapis.com/token` exactly (keep loopback for tests) and ignore a stored `client_id` that differs from the embedded `oauth_config.json`.

### 2.7 The release identity pin does not pin the ref
`internal/update/verify.go:169-170`

```go
sanRegex := fmt.Sprintf(`^https://github\.com/%s/%s/\.github/workflows/release\.yml@`, …)
```

The regex **ends at `@`** — no `$`, no ref prefix. Any run of `release.yml` on any ref (branch push, `workflow_dispatch` on `main`, an insider with branch-write but no tag rights) mints a certificate that satisfies the identity policy, and `verifySignature` accepts its artifact as an official release. Today this is latent only because the trigger is `refs/tags/v*` (`release.yml:4-6`) — a one-line trigger change turns it into a live hole. `release.yml:8-11` explicitly claims the SAN "must stay exactly `release.yml`"; the code does not enforce that.

**Fix:** `…release\.yml@refs/tags/v` and a negative test that a `refs/heads/main` SAN is rejected.

---

## 3. P1 — correctness, data loss, non-functional features

### 3.1 `resume` can never finish the scenario it exists for
`internal/runtime/kernel.go:100-128` (`Resume`), `:245-300` (wait loop), `internal/runtime/journal.go:18-44`

`gotIdle` is a **local** variable of one `execute` call. The journal persists `seen` ids and outputs, but **not** "idle was observed". Combined with the dedup gate at `kernel.go:250` (`if !j.observe(msg) { continue }` runs *before* the `status` handling at `:262-269`):

- A run that dropped the socket **after** `status:idle` but **before** `execute_reply` resumes with `gotIdle == false`.
- The reply arrives on shell → `pendingReply = msg` → the loop waits for a `status:idle` that already happened and will never be re-published.
- Result: `TimeoutError{WaitingFor:"reply"}` and the completed execution is lost.
- If frames *are* replayed, dedup drops the idle before `gotIdle = true` is ever set — same outcome.

**Fix:** persist `sawIdle` on the journal and treat a resumed reply as terminal when it is set (or simply: `if pendingReply != nil && j.sawIdle { reply = pendingReply; break }`). Add a fake-kernel test that drops the socket between idle and reply.

### 3.2 Output accumulation is O(N²) with unbounded retention
`internal/runtime/accumulator.go:18-27` + `kernel.go:259` (`j.setOutputs(acc.Outputs())`)

`Outputs()` deep-copies the entire slice on **every** iopub message, and `a.outputs` has no cap. A 10k-message stream run performs ~10k full-slice copies; a hostile or chatty kernel makes this a CPU/memory DoS.

**Fix:** ring-buffer `a.outputs` (as the journal already does) and notify the journal incrementally instead of re-snapshotting.

### 3.3 Journal caps are counts, not bytes
`internal/runtime/journal.go:10-13` — 256 outputs × up to 64 MiB base64 image (`frame.go maxFrameSize`) ≈ **multi-GB retention**. The cap gives a false sense of boundedness.

**Fix:** track retained bytes and evict on a byte budget in addition to the count.

### 3.4 One unbounded goroutine per `colab_request`
`internal/runtime/ws.go:171-178` — `go c.runColabHook(hook, req, msg)` with no context, no cancellation on `conn.close()`, no semaphore. A hook that blocks on a human lets a kernel spawn unbounded goroutines that outlive the connection.

**Fix:** single worker goroutine fed by a bounded channel (cap 1–4); drop+log beyond it; select on `ctx.Done()`/`c.done`.

### 3.5 Frames can be written to a retired socket
`internal/runtime/ws.go:229-239` snapshots `currentWS()` *before* taking `writeMu`; a concurrent `startGen` swap in between makes the write land on the dead socket with no retry. During the up-to-30 s redial window, `input_reply`/`interrupt`/`execute_request` can vanish and surface as a generic send failure.

**Fix:** take `writeMu` first, read `c.ws` under it, or retry once when the generation changed.

### 3.6 `max_balance` trips in the wrong direction and is untested
`internal/budget/budget.go:74-84`

```go
if balance >= policy.MaxBalance {   // trips when you have MORE credit
```

With the documented sample (`max_balance: 100`) and `enforce: true`, an account holding 1000 CU is **denied**. A spend guard should trip on spend (or on a balance *floor*, `<=`). `budget_test.go:31-46` only exercises the `max_hourly` trip (`balance 5 < max 10`), so the balance branch is never proven in either direction.

**Fix:** decide the semantics (`<=` as a reserve floor is the useful one), rename if needed, and test both directions.

### 3.7 Named budget profiles are unreachable from the guard
`internal/cli/explain.go:57` hardcodes `budget.LoadProfile(ctx, path, "default")`. `golab budget` has `--profile/--profile-name`, but `new`/`run`/`ssh` cannot select `profiles.ci` or `profiles.loose`. The `profiles:` block in `golab.yaml` is dead code for every guard-protected command, and there is no `--budget-profile` flag or env var.

**Fix:** add a `--budget-profile` persistent flag (and `GOLAB_BUDGET_PROFILE`) feeding `guardBudget`.

### 3.8 `logout` exits 0 with usable tokens still on disk
`internal/cli/logout.go:80-88` returns `nil` after failing to blank session proxy tokens — while the whole point of `logout` (per its own doc at `:18-24`) is to close that exposure.

**Fix:** accumulate failures and `return exitf(1, …)`; keep per-session warnings on stderr.

### 3.9 `[colab] …` diagnostics written to stdout
`internal/cli/logout.go:38,45,53,55`, `internal/cli/update.go:44,55`. Directly violates `README.md:12` and `AGENTS.md`; under `--output json` these become text events in a structured stream.

**Fix:** `Fprintln(d.Stderr, …)`; keep only genuine results on stdout.

### 3.10 Notebook save failures are discarded after announcing success
`internal/cli/execution.go:396-399`, `internal/cli/run.go:310-312` — `_ = nb.Marshal(...)` / `_ = os.WriteFile(...)`. The only copy of cell outputs is already gone; the user saw "Saving notebook with outputs to …".

**Fix:** write first, report second; return the error.

### 3.11 `exec --json` can truncate with exit 0
`internal/cli/execution.go:387` — `defer func(){ _ = cells.Close() }()`. An ENOSPC/EPIPE on the final flush yields a truncated NDJSON stream and a zero exit, breaking the "byte-exact, safe to pipe" promise.

**Fix:** fold `Close` into the named return error.

### 3.12 The pending-execution record can silently fail, disabling `resume`
`internal/cli/helpers.go:145,152,158`, `run.go:134,142,150`, `resume.go:61,68` — every `Store.Put` for `Running`/`Pending`/`LastExecution` is `_ =`. If the pending write fails, a later `colab resume` returns `ErrNoPendingExecution`: the recovery feature silently does not exist.

**Fix:** return the error from `writePending` and abort before submitting the cell; at minimum log at debug for the rest.

### 3.13 Stdin prompt replies are persisted verbatim
`internal/cli/automation.go:110-119` — `writeEvent(..., EventInputReply, {"value": value})`. `drivemount`/`auth` prompts (and any `getpass` in the same kernel) write typed credentials to a plaintext history JSONL.

**Fix:** store length-only or a redacted form; never the literal.

### 3.14 Upload verification treats a failed check as success
`internal/contents/contents.go:484-487`

```go
if err != nil || m.Size == nil || *m.Size == size { return nil }
```

Contradicts the doc comment immediately above it ("prefer a loud error over silent truncation"). An unverifiable upload is reported as verified.

**Fix:** return `*IncompleteUploadError{Detail:"verify: …"}` on GET failure.

### 3.15 Non-seekable streams are never size-verified
`internal/contents/contents.go:360,434` — `WriteStream(os.Stdin, …)` takes the prefix path with `size = -1`, so `verifyRemoteSize` is a no-op and truncation is undetectable. The chunk loop already knows `sent`; compare it against the server-reported size afterwards.

### 3.16 Double interrupt on Ctrl-C during socket loss
`internal/runtime/kernel.go:203-220` — the `lostChan` branch calls `interrupt()` when `ctx.Err() != nil` **without checking `interrupted`**. Two REST interrupts per Ctrl-C, and `idle.reset(k.graceful)` twice extends the graceful window, delaying the force-close.

### 3.17 Kernel create retries can double-allocate
`internal/runtime/runtime.go:126-147` — `retry` is timeout-only and wraps `createKernel` with no idempotency key. A request that succeeded server-side but timed out client-side leaves an orphan **and** creates a second kernel: two billable runtimes.

**Fix:** reconcile via `inspectKernel`/list before retrying creation; retry only the idempotent GETs.

### 3.18 `requestId` is regenerated on every retry
`internal/colabapi/public.go:88-91` — a fresh `uuid.NewString()` per POST attempt defeats AIP-155 idempotency. The GET reconcile at `:111` only covers "runtime already exists", not "operation created, `:wait` lost". Hoist the id out of the retry loop.

### 3.19 `ALREADY_EXISTS` reattaches to hardware that may not match the request
`internal/colabapi/public.go:191-204` — returns the existing runtime with `Reattached: true` **without comparing** variant/accelerator/shape. `run --gpu` after a CPU runtime was created under the same hash silently attaches to the wrong (and differently-priced) hardware.

---

## 4. P2 — contract, robustness, supply chain

| # | Location | Issue | Fix |
|---|---|---|---|
| 4.1 | `runtime/rest.go:130,148` | Raw kernel id interpolated into URL paths, while `ws.go:328` correctly uses `url.PathEscape` | escape both; a tampered `sessions.json` can inject `/` or `?` |
| 4.2 | `runtime/rest.go:94`, `colabapi/client.go:226` | `io.LimitReader` truncates **silently** — an oversized response decodes as valid-but-partial JSON | read `N+1`, return typed `ErrResponseTooLarge` |
| 4.3 | `runtime/accumulator.go:61-66` | `clear_output` / `update_display_data` mutate state but emit **no** `onOutput` event; streaming frontends diverge from the final `Result` | emit a synthetic `clear_output` event |
| 4.4 | `runtime/frame.go:99-102` | `if i+1 < nbufs` is always true (dead branch); no send-side overflow guard before `uint32` cast | drop it; reject `off > maxFrameSize` |
| 4.5 | `runtime/kernel.go:301` | `j.terminal = true` is written then the journal is deleted — dead field, and the missing "terminal" short-circuit is exactly the 3.1 fix | use it for 3.1 |
| 4.6 | `update/http.go:60-76` | No `CheckRedirect`: cross-host redirects and https→http downgrade are followed on a `rel.URL` from release metadata. Signature checks stop code execution, not the request | require https + host allowlist |
| 4.7 | `update/install.go:206-256`, `state/state.go:304-345` | temp file `Chmod`+`Close` without `Sync()`; parent dir never fsynced after `Rename` | `tmp.Sync()`, `dir.Sync()` |
| 4.8 | `.github/workflows/release.yml:12-14,21-29` | one job holds `contents: write` **and** `id-token: write` while running `go mod download`, goreleaser and syft — a compromised build dep can push tags with the same token that signs releases (the file itself records split-signing as deferred) | split build from signing |
| 4.9 | `auth/tokenstore.go:200-229` | `fileStore.Save` has no cross-process lock (unlike `internal/state`) — concurrent refreshes last-write-wins and, with rotation, invalidate the loser | flock around read-refresh-write |
| 4.10 | `auth/tokenstore.go:192`, `state/state.go:281-300` | token/session files read via `os.ReadFile`: follows symlinks, never validates existing mode, `MakePrivate` only on write | `Lstat`, reject non-regular/symlink, `MakePrivate` after read |
| 4.11 | `platform/platform.go:117-127`, `sshx/keys.go:281` | `COLAB_TEST_TTY`/`COLAB_TEST_STDIN_TTY`/`COLAB_TEST_STDERR_TTY`/`COLAB_TEST_AGENT` read **unconditionally in release builds**, flipping PTY/piped and agent-suppression behaviour | `//go:build test` or a `COLAB_TESTING=1` gate |
| 4.12 | `sshx/session.go:19-29` | `HostKeyCallback` accepts any key (`#nosec G106`). Defensible inside a TLS-authenticated tunnel — but the guarantee silently disappears for a user-supplied `ProxyURL` | TOFU: pin the fingerprint in `sessions.json`, warn on change |
| 4.13 | `sshx/keys.go:140-180` | private keys parsed with no permission check; a `0644` `id_ed25519` is used silently | warn when `mode&0o077 != 0` |
| 4.14 | `scripts/check-redaction.sh:5-21` | gate is 3 regexes over `*.go|*.yml|*.yaml|*.sh|*.json` with a ≥20-char body rule — a PAT in `.md`/`testdata`/`Makefile` passes; cannot see `gcp-…`, `sk-ant-…`, JWT or base64 blobs | add `gitleaks`/`trufflehog` over the tree; keep the grep as a fast pre-filter |
| 4.15 | `cli/session.go:388` | `stop --all` confirms via `platform.IsStdinTTY()` only; a non-TTY invocation destroys every session with no `--yes` | require `--yes`/`COLAB_ASSUME_YES` when stdin is not a TTY |
| 4.16 | `cli/root.go:202` + `update/check.go:184` | `autoUpdateNotice` runs in `PersistentPreRunE` **before** the `--explain` short-circuit and writes `d.Stderr` from a goroutine that outlives the command | skip when `explainRequested(cmd)`; synchronise the writer |
| 4.17 | `cli/secrets.go:52-70` | 2 s keyring probe spawns a goroutine with no cancellation — one leaked goroutine per `--secret` lookup in a long `repl` session | cancellable probe + single-flight "unavailable" cache |
| 4.18 | `cli/sync.go:352-362` | `filepath.Join(localRoot, …)` written/deleted with no `filepath.Rel` containment assertion; safety rests entirely on `cleanRemotePath` at collection time | assert `Rel` in `applySync` regardless |
| 4.19 | `cli/usage.go:20-22` | a recoverable `"[colab] Warning: …"` exits **1**, indistinguishable from failure; other advisories exit 0 | exit 0, or drop "Warning" and document exit 1 |
| 4.20 | `budget/budget.go:121-198` | hand-rolled YAML subset: a leading `---`, quoted scalars, block scalars or an inline map all fail with `ErrUnknownKey`; duplicate keys last-wins silently | use `gopkg.in/yaml.v3` into a strict struct with `KnownFields(true)` |
| 4.21 | `doctor/doctor.go:187-189` | `fixtureCheck` reports **`pass`** with `0 captured wire fixture(s)`; an empty `testdata/wire` looks healthy | `skip`/`warning` at zero |

---

## 5. P3 — polish

- `runtime/kernel.go:301` dead `j.terminal`; `frame.go:99` dead condition (also listed above because both mask the 3.1 bug).
- `cli/run.go:213-224` vs `:237-241`: duplicated `os.Stat`, the second one *after* credential init — weaker than it looks.
- `app/app.go:161` `dirOf()` is a hand-rolled dirname at **0% coverage**; returns `p[:1]` for a separator-less path. Use `filepath.Dir`.
- `render/image.go`: temp image files are never cleaned up (0600 perms are correct).
- `history/export.go`: `exportMarkdown` appends the closing fence directly after text lacking a trailing newline → malformed markdown for consumers; no code-fence escaping for cell content containing ```` ``` ````.
- `nb/nb.go:18-30`: `MultilineString` joins list-of-lines with `""` — correct per nbformat but silently merges a hand-authored list lacking terminators.
- `contents.go`: struct-level `m.Size == nil` case (4.14) also means a server omitting size is never flagged.
- `auditsec/auditsec.go:155-166`: no `history` case in `checkMode`, so a world-readable transcript dir reports the generic id `path.mode`.
- `version/version.go:10`: `Upstream = "0.8.x+HEAD"` — a range, not a pin; nothing detects when upstream moves.
- 47 × `_ = …Close()` in non-test code (mostly correct for read paths, but see 3.11).
- `.golangci.yml` excludes `G101/G115/G118/G204/G304/G703-G705` — the justifications are sound, but the exclusions are broad enough that a real finding in those rules is invisible.

---

## 6. Missing features and capability gaps

| Gap | Evidence | Why it matters |
|---|---|---|
| **No live e2e anywhere in CI** | `live-e2e.yml` exists but every CONFORMANCE note says the live proof is human-only; README:19 says no VM was ever allocated while CHANGELOG:27-31 says one was | The project's central honesty claim ("fakes are not live proof") is honoured in prose and unenforced in code |
| **Zero fixtures** | `testdata/{wire,golden}` contain only READMEs; `DELTAS.yaml` referenced by `docs/07:66` and `AGENTS.md:30` does not exist | The differential harness is structurally unable to detect wire drift; `doctor --fixtures` would report `pass` on an empty dir (4.21) |
| **`goleak` absent** | `scripts/goleak-check.sh` FAILs on 4 packages; ADVISORY in CI | In a codebase with explicit goroutine lifecycle (WS generations, hooks, probes), leak detection is the natural gate and it is off |
| **Coverage gate blind to 17 packages** | `scripts/coverage-gate.sh:22` checks only `colabapi runtime state sshx auth`; measured `platform` **27.7%**, `app` **46.0%** | `app` is the composition root and `platform` owns TTY detection + the Termux netfix — both security/behaviour-relevant, both ungated |
| **`run` lacks `exec`'s safety flags** | `execution.go:501-503` vs `run.go:426-434` | `run` cannot redact a secret it prints, and cannot emit `--json` — the same engine, two contracts |
| **No named-profile selection for the guard** | §3.7 | A documented `golab.yaml` feature is unreachable |
| **No budget cost model** | `budget` is CU-only by design | Deliberate and correct; note that users will still ask "what is this in dollars" and there is no `--explain` hint on where to look |
| **No install channels** | `docs/04:7-8,152-158` documents `install.sh`, `install.ps1`, brew/scoop/winget/nfpm; `.goreleaser.yaml:17-74` has builds/archives/checksum only | Documentation promises distribution that does not exist |
| **No SSH host-key pinning** | `sshx/session.go:19-29` | TLS covers the transport; a wrong host *inside* the tunnel is undetectable |
| **No `sessions.json` schema version** | `state/state.go` | No migration path if the on-disk schema ever changes; `ErrCorrupt` aborts rather than upgrades |
| **No `--debug` flag** | documented as intentional (`COLAB_DEBUG=1` only) | Fine, but `docs/USAGE.md:334` env table is broken (prose inserted mid-table; `TMPDIR`/`PREFIX`/`XDG_CACHE_HOME`/`SSH_AUTH_SOCK` orphaned), so the debug switch is hard to find |

---

## 7. Docs-vs-code drift (the report card was the weakest artifact)

The docs are unusually rich — and that is exactly why the drift matters: readers trust them.

### 7.1 Contradictions that undermine the whole status story
| Location | Claim | Reality |
|---|---|---|
| `README.md:15-19` | "Status (v0.1.0) … **no live Colab virtual machine was allocated** during implementation" | `version.go:7` = `0.2.0`; `CHANGELOG.md:27-31` documents a live billable CPU-VM e2e (`new`, `status`, `ls`, `exec`, final `sessions` empty). Two directly opposite claims in one repo |
| `docs/INDEX.md:25-26` | "No live Colab e2e has been run" | same |
| `CONFORMANCE.md:204-208` | `openURL` "implements only the three non-WSL/non-Termux launchers … owned by another agent" | `cli/utility.go:271-287` implements `termux-open-url`, `wslview`, `rundll32`, `open`, `xdg-open` — gap is closed, still filed as open |
| `REMEDIATION.md:33-41` | 7 "human-required" items open, tracker titled "v0.1.0-alpha.1 → v0.1.0" | 3 are done (`CHANGELOG:27-31`, `CONFORMANCE:182-195`, `client.go:201-208`); the whole 0.2.0 feature set is missing from the tracker |
| `docs/09:5-8` | "Remaining work: the second CLI command batch (SSH, automation, updater, utility), full cosign bundle verification…" | All implemented (`register.go:15-17`, `ssh.go`, `automation.go`, `update.go`, `utility.go`, `verify.go:162`) |
| `CONFORMANCE.md:12,22-25` | "~313 `Test*`/`Fuzz*` across 17 packages", "52 `*_test.go`" | Measured: **552 tests, 91 files, 22 packages** — understated ~1.8× |
| `CONFORMANCE.md:50-60` | coverage table measured 2026-11-23: colabapi 79.6, runtime 83.9, state 76.0, sshx 74.0, auth 83.0 | Measured now: 82.41 / 83.98 / 76.77 / 74.35 / 82.57 — and `platform` 27.7% / `app` 46.0% appear nowhere |

### 7.2 Broken or wrong instructions
- **`colab` vs `golab` in 14 runtime hints** — `errors.go:85,94,97,100`, `root.go:211`, `session.go:136`, `ssh.go:117,142,230`, `automation.go:435`, `sshx/errors.go:96,105,110,115,120-121`, `runtime/errors.go:101`. `README.md:40` insists the binary is `golab`; docs that quote the messages (`USAGE.md:63`, `FAQ.md:34`) use `golab`, so the quoted text does not match runtime output.
- **`go install …/cmd/colab@latest`** in `COMPAT.md:30`, `docs/03:5`, `docs/04:23` — only `cmd/golab` exists; the copy-pasteable command fails.
- **Exit-code contract** — `USAGE.md:300` says unknown command = 2; verified `./golab bogus` exits **1** (`errors.go:117-118`). Also `docs/05:38` says a missing session exits 1, but `session.go:251-253` / `:416-421` print the message and exit **0**.
- **`FAQ.md:51`** says body-less POST chunked encoding is "not verified"/unimplemented; `client.go:201-208` implements it and `CHANGELOG.md:74-75` claims the fix.
- **`docs/08:36` DEC-08** recommends `--gpu`+`--tpu` be an error (exit 2); `session.go:28-39` lets `--tpu` silently win, and `USAGE.md:29` documents the silent win. Mark DEC-08 rejected.
- **`docs/08:11`, `AUDIT-02:25,142`** claim SSH sends a second identity `colab-go/<ver>`; `sshx/wire.go:30` uses `version.ClientAgent()` = `github.com/20Youssef10/golab/<ver>` — one identity now. Close S-3.
- **`docs/07:66-67`** point at `testdata/golden/DELTAS.yaml` (absent) and say "update with `go test ./... -update`" — the real harness is `scripts/golden.sh --scenario X --update`.
- **`docs/03:35-59`** repo tree is fictional (`colab-go/`, `cmd/colab/main.go`, `workflows/termux.yml`, `testdata/notebooks`).
- **Version strings** — `USAGE.md:243`, `FAQ.md:11`, `MIGRATION.md:15`, `docs/09:54-56` still show `0.0.1-dev` / `0.1.0`.
- **`AGENTS.md` package-state claim** — "no package-level mutable state except the one `sync.Once` Termux net-fix" is false: `ssh.go:28,30`, `utility.go:266`, `tokenstore.go:84,87`, `platform.go:144` are function-var/`sync.Once` seams.
- **`CHANGELOG.md:353`** `[Unreleased]` link targets `v0.1.8`; there are no link definitions for `0.1.9`/`0.2.0`.

### 7.3 Missing from the README
`resume` (registered, documented in `USAGE.md:65-72`, absent from the README table), `--scopes full|minimal` (global flag + `COLAB_SCOPES`), `exec --secret/--env-file/--json`, and `ssh --colab-internal-mode`.

---

## 8. Test-suite gaps (the ones that would have caught the above)

| Missing test | Would have caught |
|---|---|
| `stop --all` (any test at all) | 2.2 |
| `Assign` ok + `Store.Put` fails → `Unassign` called | 2.1 |
| `buildScriptPayload` read failure | 2.3 |
| `guardBudget` consulted by `new`/`run`/`ssh`; fail-open branch under `Enforce` | 2.4, 2.5 |
| `budget.Check` with `balance >= max` | 3.6 |
| resume with socket loss between idle and reply (fake kernel) | 3.1 |
| `LimitReader` oversize → typed error | 4.2 |
| notebook marshal/write failure; `cells.Close` failure | 3.10, 3.11 |
| non-https/off-host redirect on the update client | 4.6 |
| SAN with `refs/heads/main` rejected | 2.7 |
| zero-fixture `doctor --fixtures` | 4.21 |

And two structural gaps: `platform` at 27.7% and `app` at 46.0% are outside the coverage gate entirely, so `dirOf`'s 0% and the untested `app.New` composition root will stay untested.

---

## 9. Recommended sequence

**Batch 1 — stop the money leaks (one PR, all in `internal/cli`)**
2.1 compensating `Unassign` · 2.2 `stop --all` error aggregation · 2.3 read script before allocate · 2.4 `ssh` calls `guardBudget` · 2.5 fail closed under `Enforce` · 3.8 `logout` exit 1 · 3.6 `max_balance` semantics + test.
*Why first: every item is small, local, and each one currently costs a user real money or hides that it did.*

**Batch 2 — trust anchors (security, one PR)**
2.6 allowlist the token endpoint + pinned `client_id` · 2.7 pin the SAN ref + negative test · 4.6 redirect allowlist · 4.8 split build/signing job · 4.11 gate `COLAB_TEST_*` · 4.14 add `gitleaks`.

**Batch 3 — resume & protocol integrity**
3.1 persist `sawIdle` (this is the feature's whole point) · 3.2 ring the accumulator · 3.3 byte-budget the journal · 3.4 bounded hook worker · 3.5 lock-then-read the socket · 3.16 idempotent interrupt · 3.17/3.18/3.19 allocation idempotency.

**Batch 4 — silent false success**
3.9 stderr discipline · 3.10/3.11/3.12 error propagation · 3.13 don't persist prompt replies · 3.14/3.15 upload verification · 4.19 exit codes.

**Batch 5 — docs reconciliation (mechanical, high value per minute)**
README status block → v0.2.0 and the real proof status · `colab`→`golab` in 14 hints · `cmd/colab`→`cmd/golab` · `resume`/`--scopes`/`--secret` into the README · re-measure the CONFORMANCE coverage table and add `platform`/`app` to the gate · recompute the test counts · close the five resolved entries in CONFORMANCE §5 / REMEDIATION / docs/09 · delete the fictional repo tree · add `DELTAS.yaml` or drop the reference.

**Batch 6 — gates that stay honest**
Add `platform`+`app` to `scripts/coverage-gate.sh` with per-package floors (`platform` 60, `app` 55 as a ratchet) · make `goleak-check.sh` non-advisory or delete it · fix `doctor --fixtures` to `skip` at zero · extend `TestNumberMatchesLatestChangelogRelease` to fail on a stale doc version string.

---

## 10. Residual risk

- **No live proof.** Every claim above is static. The control-plane hardening (XSSI, XSRF, reconciliation, typed 503) is well-reasoned but has never been exercised against real Google infrastructure in CI, and `testdata/` is empty, so wire drift is undetectable by construction.
- **No race detector on this host.** Concurrency findings (§3.1–3.5, 4.9) are inspection-only. `windows-11-arm` drops `-race` in CI too, so the matrix leans on the unix runners.
- **No lint on this host.** `golangci-lint` is absent; the exclusion list in `.golangci.yml` (G101/G115/G118/G204/G304/G703-G705) is broad enough that a new violation in those rules would pass CI silently.
- **Upstream is a moving target.** `version.Upstream = "0.8.x+HEAD"` is a range; with zero fixtures and no differential suite, the port can drift without any signal.
- **Termux/Android is a first-class target** (`internal/platform/netfix`, Termux guide) but has no device check in CI beyond `termux-nightly.yml`, and the two most Android-relevant packages (`platform`, `app`) are the two with the least coverage.

**Bottom line:** the protocol and security *code* is better than the project's own documentation says it is; the *spend paths*, the *resume path*, and the *status ledger* are worse. Fix Batch 1 and Batch 5 before the next tag — the first stops users losing money, the second stops the project losing credibility.
