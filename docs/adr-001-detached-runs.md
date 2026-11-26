---
date: 2026-11-26
category: adr
status: accepted
tags: [detached-runs, jobs, wait, sync-watch, ttl, colab]
---

# ADR 001: Detached remote runs on Colab VMs

## Context

`golab run` submits a script to a freshly allocated VM and streams its output
over the kernel WebSocket until the cell finishes. That is correct for
interactive work, but it couples the lifetime of the local process to the
lifetime of the remote work: if the local CLI is interrupted, loses its socket,
or is closed, the user has no durable record of the work and no way to come
back to its output later.

Users need a second execution mode for long-running work (training, data prep,
batch jobs) where:

- the remote process outlives the cell that launched it and the local CLI
  invocation that submitted it;
- the user can list jobs and inspect output from a later, independent command;
- there is **no locally running daemon** and **no keep-alive** thread holding
  the VM open.

Any design that stores results locally or keeps a local process alive conflicts
with the repository's "fire-and-forget" invariant (AGENTS.md §Entrypoints): no
daemons or background services, and all I/O takes a `context.Context`.

## Decision

Add a **detached** execution mode for `golab run` plus three companion
commands, implemented in new CLI files and a small `internal/detach` package:

- `golab run --detach --ttl DURATION SCRIPT [ARGS...]` allocates a session
  exactly as `run` does, but instead of waiting on the kernel WebSocket it
  submits **one** launcher cell. The launcher:
  - creates `/content/.golab/runs/<id>/` on the VM (remote paths use `path`),
  - writes `meta.json` (id, script, session, start time, ttl), and
  - starts the user's script with Python `subprocess.Popen(...,
    start_new_session=True)`, redirecting stdout/stderr to `log` and writing
    the final exit code to `exit` when the child terminates.
  The launcher payload is built from JSON/base64- or `repr`-quoted values; no
  user-controlled string is ever spliced into Python source.
- A **local manifest** (a plain JSON file, written atomically) records only
  identifiers and metadata: run id, session name, script path, start time and
  ttl. It contains no credentials and no source. It is the local index that
  lets later commands find runs; it is not a daemon.
- `golab jobs` reads the local manifest and polls the **Contents API** for each
  run's `meta.json`, `log` and `exit`, printing status, exit code and duration.
- `golab logs ID [-f]` reads the run's remote `log`; `-f` follows it with
  bounded polling (interval × maximum iterations), never an unbounded tail.
- `golab wait -s NAME --for kernel|idle|ready --timeout 300` performs bounded
  polling over the **existing** readiness checks (kernel attach, exec-to-idle,
  session status). It allocates nothing.
- `golab sync --watch [--interval N]` wraps the existing one-shot sync engine
  in a context-cancellable polling loop with a bounded iteration count. No
  filesystem watcher (fsnotify) dependency is introduced.

### Non-goals (explicit)

1. **No local daemon / no keep-alive.** Detached jobs run **remotely** with
   `start_new_session=True`; the detached child is owned by the VM, not by the
   local CLI. Nothing persists locally except the small JSON manifest, and all
   observation is **on-demand polling** from `jobs`, `logs`, and `wait`. There
   is no background goroutine, no local process supervision, and no WebSocket
   held open after `run --detach` returns.
2. **No new dependency.** Polling replaces event notification everywhere; no
   fsnotify, no job-queue library.
3. **No live proof from fakes.** Unit tests use fakes; the remote behaviour is
   not "done" until a human runs a live e2e probe (billable, human-only).

### `--ttl` is mandatory with `--detach`

A detached process keeps the VM busy after the CLI exits. Without a bound, a
forgotten job would bill forever. Therefore:

- `--detach` **requires** `--ttl DURATION` (a strictly positive duration);
  the command is a usage error (exit 2) otherwise.
- The TTL is written into `meta.json` and into the local manifest, and is
  surfaced by `golab jobs`.
- The remote launcher records the start time so a future reaper can act on
  the deadline. In this change the TTL is advisory/informational — **it does
  not itself terminate the VM**; enforcing it is a follow-up. The important
  immediate property is that every detached job carries a visible deadline so
  it cannot be silently forgotten.

## Options considered

| Option | Pros | Cons | Verdict |
|--------|------|------|---------|
| **A. Remote detached child + polling Contents API** (chosen) | No local daemon; survives CLI exit; reuses Contents API and existing sync engine; matches fire-and-forget invariant | Needs VM to stay allocated; exit code only available after child finishes; polling latency | **Chosen** |
| B. Local daemon supervising the job | Instant output, easy cancellation | Violates no-daemon invariant; a mobile/Termux host kills background processes; credentials live longer | Rejected |
| C. Keep the kernel WebSocket open in a background goroutine | Reuses existing streaming code | Still a local keep-alive; dies with the CLI; not resumable from a new invocation | Rejected |
| D. Server-side scheduler / job service | Proper lifecycle and TTL enforcement | Requires a service that does not exist in this CLI; out of scope | Deferred |

## Consequences

### Positive

- Work survives CLI disconnect; results are inspectable later from any machine
  that has the manifest.
- No new dependencies, no local background processes, small credential-free
  local state.
- All polling loops are bounded and context-cancellable, so they cannot hang a
  test or a terminal.

### Negative / risks

- **A detached job requires the VM to stay allocated.** This is billable time.
  It must be paired with `--ttl`, and the user must still remember to stop the
  session (`golab stop`) when done; a future reaper should enforce the TTL.
- **Output visibility lag.** Polling means `logs -f` and `jobs` see output only
  on the next poll; the interval is bounded, not instantaneous.
- **`exit` is eventual.** The exit code appears only after the detached child
  finishes; until then `jobs` reports the job as running.
- **Manifest drift.** The local manifest can disagree with VM reality (VM
  pruned, files deleted). `jobs`/`logs` must treat missing remote files as
  "unknown/stale", never crash, and must not fabricate a success.
- **Payload safety is a hard requirement.** Any future change that embeds user
  strings directly into the launcher risks code injection on the VM; keep the
  JSON/base64/`repr` discipline and the security tests that pin it.

## Verification

Test-first with fakes: `run --detach` writes the manifest and spawns no local
process; `jobs` and `logs -f` poll and render remote state; `wait` covers all
three modes plus its typed timeout; `sync --watch` stops on context
cancellation. SecOps gates (`redaction`, `paths`, `goleak-check`) must stay
green. Live e2e (billable, human-only) remains required before calling the
feature live-proven.
