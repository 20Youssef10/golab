<!-- Generated from docs/adr-001-detached-runs.md §Context, Decision, Consequences, Verification by docs-site/tools/sync-sources.py. Do not edit. -->

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
  - writes `meta.json` (id, script, session, start time, ttl and the absolute
    `deadline_at` fixed at submission),
  - starts the user's script with Python `subprocess.Popen(...,
    start_new_session=True)`, redirecting stdout/stderr to `log` and writing
    the final exit code to `exit` when the child terminates, and
  - starts a **remote TTL watchdog**, also with `start_new_session=True`, from
    a fixed Python source that reads the deadline and polling interval from the
    same base64/JSON payload (see "TTL enforcement" below).
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

### `--ttl` is mandatory and enforced with `--detach`

A detached process keeps the VM busy after the CLI exits. Without a bound, a
forgotten job would bill forever. Therefore:

- `--detach` **requires** `--ttl DURATION` (a strictly positive duration);
  the command is a usage error (exit 2) otherwise.
- The absolute deadline (`started_at + ttl`, UTC, fixed at submission) is
  written into `meta.json` on the VM and into the local manifest. `golab jobs`
  reports `ttl=ok|exceeded|unknown` and the `ttl_exceeded` status.
- **Enforcement is remote, not advisory.** The launcher starts a small Python
  watchdog in its own session. It reads `deadline` and `watch_interval` from
  the base64/JSON payload, polls `time.time()` until the deadline, checks the
  `exit` marker first (a completed job is left alone), then checks that the
  recorded child pid is still alive and kills the child's **process group**
  with `SIGKILL` (the child was started with `start_new_session=True`, so its
  pid is the pgid), finally writing `exit=124` and `ttl-reason=ttl_exceeded`.
  `golab jobs` reads that reason marker; `golab logs` still reads `log`.
  No local process, goroutine or timer persists after `run --detach` returns.

### `--max-cu` is foreground-only

`--max-cu` (and its `--enforce` companion) sample `GET /tun/m/ccu-info` inside
the **foreground** run loop. A detached CLI returns after submitting one cell,
so the sampler would never run. We chose the smaller, honest option: the
`--detach` registration wrapper rejects an explicitly supplied `--max-cu` or
`--enforce` with a usage error (exit 2) before any allocation, stating that
these flags apply to foreground runs only. No detached run silently ignores a
CU guard. Wiring a remote CU sampler is deferred: it would need a second
remote watchdog and a policy for which account-level balance to guard.

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
  `--ttl` is required and now enforced, but the watchdog is a *process on that
  VM*: if the VM is unassigned/reclaimed before the deadline, the watchdog dies
  with it and no local reaper notices. `golab stop` still releases the VM (and
  therefore stops the job and the watchdog) as before.
- **Polling granularity.** The watchdog checks every 5 seconds (`watch_interval`
  in the payload), so expiry can be up to one interval late; the deadline itself
  is rounded up to whole seconds so it never fires early. `jobs` also reports
  `ttl=exceeded` from `deadline_at` when the reason marker is absent, which
  covers a watchdog that never ran.
- **The kill is abrupt.** The watchdog sends `SIGKILL` to the child process
  group and records `exit=124` plus `ttl-reason=ttl_exceeded`. There is no
  graceful shutdown hook, and a normal exit that races the deadline can be
  relabelled; this is a deliberate trade-off for a bounded job.
- **Output visibility lag.** Polling means `logs -f` and `jobs` see output only
  on the next poll; the interval is bounded, not instantaneous.
- **`exit` is eventual.** The exit code appears only after the detached child
  finishes (or the watchdog kills it); until then `jobs` reports the job as
  running.
- **Manifest drift.** The local manifest can disagree with VM reality (VM
  pruned, files deleted). `jobs`/`logs` must treat missing remote files as
  "unknown/stale", never crash, and must not fabricate a success.
- **Payload safety is a hard requirement.** Any future change that embeds user
  strings directly into the launcher or watchdog risks code injection on the
  VM; keep the JSON/base64/`repr` discipline and the security tests that pin it.
- **Foreground-only CU guard.** `--detach --max-cu` is a usage error, not a
  silent no-op; users who need CU enforcement must use foreground `golab run`.

## Verification

Test-first with fakes: `run --detach` writes the manifest and spawns no local
process; the launcher embeds the absolute deadline in `meta.json` and a quoted
watchdog snippet; `jobs` reports `ttl=exceeded`/`ttl_exceeded` from the remote
reason marker and deadline; `stop` still releases the VM; `--detach --max-cu`
and `--detach --enforce` exit 2 before allocation; `jobs` and `logs -f` poll and
render remote state; `wait` covers all three modes plus its typed timeout;
`sync --watch` stops on context cancellation. SecOps gates (`redaction`,
`paths`, `goleak-check`) must stay green. Live e2e (billable, human-only)
remains required before calling the feature live-proven: confirm on a real VM
that the watchdog kills the process group at the deadline and that `jobs`
renders the marker.
