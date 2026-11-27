# golab safety reference

Load this before billable, destructive, non-interactive, credential-adjacent or human-gated work. Facts are from `docs/USAGE.md`, `docs/ADR-001`, `docs/MIGRATION.md`, `docs/CONFORMANCE.md` §6 and `AGENTS.md` in the v0.2.0 tree.

## Spend checklist (run in order)

1. **Inspect before acting.** `golab sessions`, `golab budget --json`, `golab cost --since 24h`, `golab usage`. These allocate nothing.
2. **Preflight a billable command.** `golab new -s NAME --gpu T4 --explain`, `golab run SCRIPT --explain`, or `golab ssh --explain`. `--explain` exits before authentication, state mutation and network calls and prints a redacted, deterministic plan (`[golab] explain: …`) safe to paste into a ticket. It applies to `new`, `run` and `ssh` only.
3. **Validate inputs locally first.** Script path must exist (a missing `run` script exits 2 with zero Colab requests); accelerators are validated before assignment; `--env` keys match `^[A-Za-z_][A-Za-z0-9_]*$`; `--ttl` must be positive.
4. **Set a reserve.** `golab run SCRIPT --max-cu 10 --enforce` interrupts and stops a foreground run on breach; without `--enforce` a breach is only reported; `--max-cu 0` disables monitoring. The CU endpoint is account-level, so do not read the delta as per-VM cost. `golab budget --enforce` is profile-driven and refuses an allocation at a tripped threshold.
5. **Do the work, then clean up.** `golab stop -s NAME [--verify]` or `golab stop --all`, then `golab sessions` must be empty. Orphans shown as `[?]`: `golab reap --dry-run` → `golab reap [--yes]`.
6. **Detached jobs are still billable.** `run --detach` requires `--ttl DURATION` (usage error exit 2 otherwise) and records an absolute `deadline_at` in the VM's `meta.json` plus a local credential-free manifest shown by `jobs`. A remote watchdog now **enforces** the deadline: it kills the detached job's process group at expiry, writes `ttl-reason` = `ttl_exceeded` with exit code `124`, and `jobs` reports `ttl=exceeded`. The watchdog stops the **job, not the VM** — you must still `golab stop -s NAME`. `--max-cu` and `--enforce` are rejected outright with `--detach` (foreground monitor only). Follow with `golab jobs` and `golab logs ID [-f --interval 2s --max 30]`; there is no local daemon or keep-alive socket, so observation is on-demand polling.

`--yes` (or `COLAB_ASSUME_YES=1`) suppresses non-destructive confirmations only — never a budget guard, never a browser/GCP step.

## Redaction and data handling

- Tokens, OAuth codes/verifiers, client secrets, and `Authorization`/`Cookie`/proxy-token headers use a secret type and must never appear in stdout, logs, argv, errors or trace files. Remote free-form text is sanitised before display (OSC/DCS/APC and query sequences stripped; SGR kept).
- Pass secrets with `--secret NAME` (process environment or OS keyring) and `--env-file PATH` (mode 0600). These are a local-only bridge: values are quoted, removed from stdout/stderr/history/JSON records, and Colab Web UI secrets are never read.
- Never splice user strings into Python sent to the VM. The CLI sends JSON/`repr`-quoted payloads; keep that discipline in any wrapper.
- For SSH, pin the runtime host key with `golab ssh --accept-host-key SHA256:…` (prefix optional). Obtain the fingerprint from a trusted source: it must match the live key on first use and the stored pin afterwards. A mismatch must fail closed, never be auto-accepted.
- State writes (`sessions.json`, `settings.json`) go through lock + temp file + fsync + atomic rename. A corrupt file aborts; never reset it to empty.
- `golab trace -- <cmd> [-o FILE]` writes redacted wire NDJSON (query keys, body shape and hash only). `--unsafe-bodies` adds redacted bodies labelled `UNSAFE` — review before sharing. Never print credentials from `audit --json`, `config list` or `whoami`.
- TLS verification is always on; release builds have no host override. No `--debug` flag exists — `COLAB_DEBUG=1` only. `COLAB_TEST_*` are internal hooks, not user settings.

## Streams, TTY and exit codes

- stdout = machine-consumable data. stderr = `[colab] …` diagnostics, prompts and update notices. `--output text|json|ndjson` wraps stdout in the `golab/1` schema (`info`/`output`/`error`, no token field); stderr is unaffected. `--logtostderr` sends program output to stderr — do not use it in a pipeline.
- TTY matters in three places: `exec` with no input on a TTY errors (`No input provided …`, exit 1); `stop --all` prompts only on a TTY (without one, refuses with exit 2); `repl` selects its rich frontend only on a TTY. Inline-image escapes are suppressed when stdout is not a TTY — use `--output-image`.
- Exit codes: `0` success; `1` generic/API/verification/budget-guard failure; `2` usage or missing prerequisite; `130` interrupt; remote/N for `ssh -- CMD` and `run` script exit codes.

## Path semantics

- Remote paths use Linux `path` semantics regardless of host (Windows/Termux included); local paths use host `filepath` semantics. `make paths` is the repo gate for this invariant.
- Each execution starts with `os.makedirs('/content', exist_ok=True); os.chdir('/content')`, so relative paths in code are relative to `/content` on the VM.
- Remote file commands (`cat`, `tail`, `grep`, `du`, `fetch`, `upload`, `download`, `rm`, `edit`) resolve relative paths against the VM root (`/`); `ls` defaults to `content`; `find NAME` always searches `/content`.
- Session names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` and may not be Windows reserved names. Without `-s`, resolution is `-s` → `GOLAB_SESSION` → `golab use` default → unique-session fallback; the unique-session notice goes to stderr.

## Human-only gates — never claim these are done by an agent

1. Live `auth` / OAuth consent, including re-consent when switching `--scopes full|minimal`; `golab auth` inside the VM is interactive and bounded to a 10-minute idle timeout.
2. Drive browser approval for `drivemount`. `--wait-for-consent` only replaces the Enter prompt with bounded polling; the approval itself is human. Approving with a different Google account is a common propagation failure.
3. Interactive-TTY `repl` and `ssh`; piped `repl` and `ssh -- CMD` are CI-safe but are not a TTY proof.
4. Real Termux/Windows devices, Windows console/ACL cleanup and macOS keychain checks; cross-builds and fakes are not device proof.
5. Live probes and billable e2e: prefer GET-only, log every endpoint, unassign before finishing, and end with an empty `golab sessions`. `doctor --live` requires `GOLAB_LIVE_PROBES=1` and `LIVE_E2E_ENABLED` must not be `false`.
6. Signed-release verification and `golab update --install`; confirm the version with the requester and never cut a release autonomously.
7. GCP-side prerequisites for the Public API: enable `colaboratory.googleapis.com`, configure a billing/quota project, and possibly re-consent. `golab` provisions none of these and no flag bypasses them.

## Environment knobs worth knowing

`COLAB_AUTH` (`oauth2`/`adc`), `COLAB_SCOPES` (`full` default; `minimal` cannot mount Drive), `COLAB_OUTPUT` (`text`), `COLAB_ASSUME_YES`, `COLAB_DEBUG`, `COLAB_CLI_PUBLIC_API`, `GOLAB_SESSION`, `GOLAB_BUDGET_PROFILE`, `GOLAB_BUDGET_FILE`, `GOLAB_TRACE`, `GOLAB_TRACE_UNSAFE_BODIES`, `COLAB_CONFIG_DIR`, `COLAB_SESSIONS_FILE`, `COLAB_CLI_REQUEST_TIMEOUT`, `NO_COLOR`, `VISUAL`/`EDITOR`, `TMPDIR`/Termux `PREFIX`. A globally installed Python `colab` can shadow the binary — verify with `golab version` when reproducing a report.
