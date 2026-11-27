---
name: golab-operations
description: "Operate the golab CLI (unofficial Google Colab CLI) safely: session lifecycle, exec and run parity, file transfer, VM inspection, detached jobs, spend guards, redaction, stdout and stderr contract, TTY and --yes rules, path semantics, and human-only steps. Use when asked to create, inspect, run code on, sync files with, or stop Colab sessions with golab, or to diagnose billable-spend, credential, confirmation, or remote-versus-local path issues."
metadata:
  version: "1.1.0"
  owner: golab-maintainers
---

# golab operations

`golab` is a single static binary, an unofficial Go port of the Google Colab CLI (v0.2.0, 54 top-level commands). This skill is the safe operating procedure for an agent driving it. Command/flag facts below were checked against `golab --help` and `<command> --help`; re-check the installed binary with `golab version` because flags can drift between versions.

## Golden rules (apply on every invocation)

1. **stdout is data, stderr is chatter.** Anything a program should consume is on stdout; every `[colab] …` diagnostic is on stderr. Never filter stdout with a regex expecting diagnostics. `--output text|json|ndjson` wraps stdout in the `golab/1` schema (`info`, `output`, `error` events); diagnostics stay on stderr. `--logtostderr` moves program output to stderr too — avoid it in pipelines.
2. **Validate before allocating.** A typo in a script, accelerator, key or flag must fail before a VM is assigned. `golab run` reads the local script before any request; `golab new` validates `--gpu`/`--tpu`/`--high-mem` locally. Use `golab --explain` on `new`, `run` and `ssh` for a redacted plan that exits before auth, state writes or network calls.
3. **Assume no TTY.** `stop --all` prompts only when stdin is a TTY; without one it refuses and exits 2. Pass `--yes` or set `COLAB_ASSUME_YES=1` for non-interactive runs. `--yes` never bypasses a budget guard. `exec` with no input and a TTY fails; `repl` picks its rich frontend only on a TTY.
4. **Secrets are local-only.** Use `--secret NAME` (process env or OS keyring) and `--env-file PATH` (mode 0600). Tokens, codes, client secrets and `Authorization`/`Cookie`/proxy-token headers are redacted everywhere; never echo them, paste traces, or use `--debug` (it does not exist — use `COLAB_DEBUG=1`). Never splice user strings into code executed on the VM; the CLI sends quoted payloads.
5. **Remote paths are Linux paths.** Use `content/data.csv`, never `content\data.csv`. The kernel cwd is `/content` for `exec`/`run` code. Remote file commands resolve relative paths against the VM root (`/`); `ls` defaults to `content`, and `find NAME` always searches `/content`. Local paths use host conventions. Remote and local path code paths must never be mixed.
6. **Always finish the money loop.** After any billable work, `golab stop -s NAME` (or `stop --all`) and confirm with `golab sessions` that nothing is left allocated. For orphans marked `[?]`, use `golab reap --dry-run` then `golab reap [--yes]`.

## Quick classification

| No allocation (safe to probe) | Reads a running VM | Allocates / consumes billable time |
|---|---|---|
| `doctor`, `audit`, `budget`, `cost`, `usage`, `config`, `state`, `wait`, `compat`, `version`, `help`, `agentout`, `completion` | `status`, `ls`, `cat`, `tail`, `grep`, `du`, `find`, `ps`, `fetch`, `exec`, `repl`, `resume`, `install`, `env freeze`, `sync`, `logs`, `jobs` | `new`, `run`, `ssh` auto-create, `attach` (attach only), `reap` (unassign), `stop`, `bundle verify --env`, `drivemount` |

`--explain` performs no network call and is safe to run on any billable command. `doctor` is GET-only; `doctor --live` additionally requires `GOLAB_LIVE_PROBES=1` and `LIVE_E2E_ENABLED` must not be `false`.

## Core workflow

1. **Orient:** `golab version`, `golab sessions`, `golab budget [--json]`, `golab usage`. Session selection order is `-s NAME` → `GOLAB_SESSION` → `golab use` default → unique-session fallback; with several sessions and no name, the command asks you to pass `-s`.
2. **Plan a billable step:** `golab new -s NAME [--gpu T4|--tpu V5E1] [--high-mem] [--ttl 2h] [--ttl-action warn|stop] [--wait-capacity 5m] --explain`. `--ttl` is a *local* expiry checked on each invocation; there is no daemon, and it does not change the server-side assignment. `--ttl-action stop` makes a best-effort unassign on expiry. If both `--tpu` and `--gpu` are given, TPU silently wins.
3. **Do the work:** prefer `golab run SCRIPT [flags]` for one-shot jobs (allocates, uploads, runs, then unassigns unless `--keep`), or `golab exec -s NAME -f FILE` for code in an existing session. `run` reads `SCRIPT` before allocating; flags after `SCRIPT` belong to the script (only `--env KEY=VALUE` is accepted on either side). Both support `--env`, `--env-file`, `--secret`, `--timeout`, `--output-image` and per-cell `--json`. `.ipynb` inputs run code cells in order and write `<name>_output.ipynb` with mode 0600.
4. **Transfer and inspect:** `upload`/`download` need `-r` for directories; `edit` opens `$VISUAL`/`$EDITOR` and uploads only if the SHA-256 changed. `ps` (alias `top`), `cat`, `tail`, `grep`, `du`, `find`, `fetch` poll server-side and never mount a local filesystem. `tail -f` is a live view, not a complete log archive across rotation. `fetch --sha256 HEX` verifies bytes.
5. **Stop and verify:** `golab stop -s NAME [--verify]`, then `golab sessions` (stdout empty means nothing allocated). `stop --all` requires a TTY or `--yes`/`COLAB_ASSUME_YES=1`.

## Detached jobs and spend guards

- `golab run --detach --ttl 1h SCRIPT` enforces the deadline **at submission** (`--detach` without a strictly positive `--ttl` is a usage error, exit 2) and **remotely** from then on: the launcher writes an absolute `deadline_at` into the VM's `meta.json` and starts a bounded watchdog that kills the detached job's process group at the deadline, writes `ttl-reason` = `ttl_exceeded` and exit code `124`; `golab jobs` reports `ttl=exceeded`. There is still no local daemon or keep-alive socket — `jobs`, `logs ID [-f]` and `wait --for kernel|idle|ready` poll on demand. **The watchdog stops the job, not the VM** — the runtime stays allocated and billable until you `golab stop` it.
- `--max-cu` and `--enforce` are now **rejected outright with `--detach`** (usage error): they sample CU in the foreground run loop only. Use them on a foreground `golab run SCRIPT --max-cu 10 --enforce`; `--max-cu 0` disables the monitor. `golab budget --enforce` refuses an allocation when a profile threshold is tripped; `--max-balance` is a reserve floor and `--max-hourly` a rate ceiling. `--yes` never overrides either guard.
- `golab logs ID -f --interval 2s --max 30` is always bounded. A missing remote file is reported as unknown/stale, never as success; an expired run is reported as exceeded, never as success.
- `golab cost [--since 24h] [--json]` reports observed CU samples from a private local ledger; account-level, no prices. `golab reap --dry-run` previews orphan cleanup before the billable unassign.

## Recently landed surfaces

- **SSH host-key pinning:** `golab ssh -s NAME --accept-host-key SHA256:…` accepts a fingerprint with or without the `SHA256:` prefix. It must match the live runtime key on first use and the stored pin afterwards; a mismatch is a hard failure, so obtain the fingerprint from a trusted source rather than blindly copying a prompt.
- **Compatibility snapshots:** `golab compat refresh --ref v0.8.0 --out FILE` fetches the official upstream CLI surface into a provenance-tagged snapshot; dry-run by default, and `--out -` prints the snapshot JSON to stdout. `golab compat report [--json]` then compares the live command tree against a snapshot. `compat` does not allocate a VM.
- **Distribution channels:** `packaging/` holds Homebrew (`golab.rb`), Scoop (`golab.json`), winget (`20Youssef10.golab*.yaml`) and nfpm (`nfpm.yaml`, `.deb`/`.rpm`/`.apk`) configs, aligned with signed release v0.2.0. **winget is blocked**: releases publish only `.tar.gz` archives while winget's installer enum has no tar.gz type — a `.zip` repack of the same signed binary with new URLs/hashes is required before a `winget-pkgs` PR. Never weaken a hash check to unblock it. Package availability is not live-install proof.
- **MCP companion binary:** `go install github.com/20Youssef10/golab/cmd/golab-mcp@latest` installs the typed MCP server. For its flags, 22 tools and client config, use the sibling `colab-mcp` skill.

## Human-only — stop and ask a maintainer

Never automate or claim success for: browser **OAuth consent** (including `--scopes` mode-change re-consent and `golab auth` in the VM), **Drive approval** (`drivemount`; `--wait-for-consent` only automates the wait), **interactive TTY `repl`/`ssh`**, **real Termux/Windows devices** and console/ACL checks, **live probes** (billable; GET-only preferred, unassign and end with empty `sessions`), **signed-release verification** beyond a documented check, and **GCP-side prerequisites for the Public API** (enable `colaboratory.googleapis.com`, set a billing/quota project — no flag creates these). `golab attach` needs `--runtime NAME` plus `--public-api` (or `COLAB_CLI_PUBLIC_API=1`) and those GCP prerequisites.

## Gotchas

- `--max-cu` is account-level, not per-session; do not attribute a CU delta to one VM.
- `new --high-mem` is ignored for L4/V5E1/V6E1 (warns on stderr). Accelerator values are case-insensitive.
- `run` mirrors script `sys.exit(N)` codes; `ssh -- CMD` returns the remote status; usage/prerequisite failures exit 2; interrupts exit 130.
- Session names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` and cannot be Windows reserved names. Unnamed sessions get a random six-character hex name.
- `resume -s NAME` recovers an interrupted execution without resubmitting code; output emitted strictly while the socket was down is unrecoverable (`ErrOutputGap`).
- A globally installed Python `colab` can shadow the binary on PATH; use `golab version` to confirm which one you are driving.
- `trace -- <cmd> [-o FILE] [--unsafe-bodies]` records redacted wire NDJSON; `--unsafe-bodies` adds redacted bodies labelled UNSAFE and must be reviewed before sharing. Never share an unreviewed trace.

## References (load when the task needs them)

- **Command inventory and exact flags:** read `references/command-map.md` for all 54 top-level commands grouped by purpose, plus hidden commands.
- **Deep safety rules:** read `references/safety.md` before any billable, destructive, non-interactive, credential-adjacent or human-gated operation. It carries the spend checklist, redaction contract, TTY/exit-code table, path rules and environment knobs.

Version history: v1.1.0 — added verifiable follow-up surfaces (host-key pin, compat refresh, packaging channels, remote TTL enforcement, MCP install); v1.0.0 — initial release, flags verified against `golab 0.2.0 --help`.
