<!-- Generated from docs/USAGE.md §Conventions used throughout, 1. Session lifecycle by docs-site/tools/sync-sources.py. Do not edit. -->

## Conventions used throughout

- **stdout is data, stderr is chatter.** Anything another program should consume — session lists, source output, URLs, JSON events — goes to stdout. Every `[colab] …` progress or diagnostic line goes to stderr. This is a deliberate deviation from the Python CLI and is what makes piping safe.
- **`-s NAME` selects a session.** Most commands accept `-s` / `--session`. Resolution order is **`-s` flag → `GOLAB_SESSION` → the default set by `golab use` → the unique-session fallback**. If a name resolves from `GOLAB_SESSION` or `golab use`, it is used without the unique-session check; a name that does not exist locally still errors. With no configured name and exactly one session, `golab` uses it and prints `Using unique session '<name>'` to stderr. With zero sessions it prints `No active sessions found. Create one with 'golab new'.` With several, it lists the names and asks you to pass `-s`.
- **Remote paths are Linux paths.** The VM is Linux even when your host is Windows or Termux. Use `content/foo.csv`, not `content\foo.csv`. Local paths use your host's conventions.
- **Session names** must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` and must not be Windows reserved names. Validation happens before any network call. If you do not name a session, `golab` generates a random six-character hex name.

## 1. Session lifecycle

### `golab new`

Allocate a runtime and persist it locally.

```bash
golab new                              # CPU, randomly generated name
golab new -s train --gpu t4            # named T4 session
golab new -s llm --gpu A100 --high-mem # accelerators are case-insensitive
golab new -s tpu --tpu v5e1
golab new -s batch --ttl 2h                 # local expiry warning after 2h
golab new -s batch --ttl 2h --ttl-action stop
golab new -s train --wait-capacity 5m       # retry 503 capacity refusals for up to 5m
```

| Flag | Meaning |
|---|---|
| `-s`, `--session NAME` | Session name (see naming rule above). |
| `--gpu {t4,l4,g4,h100,a100}` | Request a GPU. Validated before any request. |
| `--tpu {v5e1,v6e1}` | Request a TPU. **If both `--tpu` and `--gpu` are given, `--tpu` silently wins** (upstream behaviour). |
| `--high-mem` | Request a high-RAM machine shape. For L4, V5E1 and V6E1 there is only one shape, so `golab` prints `--high-mem ignored` to stderr and continues. |
| `--ttl DURATION` | Positive local lifetime (e.g. `2h`) stored on the session. **No daemon or timer is installed**: the check is a local `sessions.json` read on every invocation. It does not extend or shorten the server-side assignment. |
| `--ttl-action warn\|stop` | On expiry, `warn` (default) prints to stderr; `stop` additionally makes a best-effort unassign and removes the local record. An expired credential is not a reason to fail an unrelated command. |
| `--wait-capacity DURATION` | Retry capacity refusals for at most this duration before failing. |

Allocation failures get friendly messages: a `412` becomes advice to stop a session or try another accelerator; an accelerator your account is not entitled to becomes advice to try `--gpu T4` or omit the flag.

### `golab sessions`

Reconcile local state with the server, prune sessions that are gone, refresh expiring proxy tokens and print what is still allocated:

```text
[train] m-s-abc123 | Hardware: T4 | Shape: Standard | Variant: GPU
[?] m-s-def456 | Hardware: CPU | Shape: Standard | Variant: DEFAULT
```

A `[?]` in the name column is a server-side assignment with no matching local record — usually an orphan to clean up explicitly. If nothing is active, the message goes to stderr and stdout stays empty.

### `golab reap`

Unassign server runtimes that have no local session record (the `[?]` rows), and prune stale local records:

```bash
golab reap --dry-run                 # preview only, no mutation
golab reap --older-than 2h --yes     # unassign orphans, prune stale records
golab reap --json                    # machine-readable result
```

| Flag | Default | Meaning |
|---|---|---|
| `--dry-run` | off | Preview without any mutation. |
| `--older-than DURATION` | `2h` | Prune stale local records older than this duration. A server assignment with no local record is an orphan regardless of this value. |
| `--yes` | off | Confirm without the stderr prompt (non-interactive). |
| `--json` | off | Emit the result as JSON on stdout. |

This is a billable cleanup command: it calls the unassign endpoint, so preview with `--dry-run` before confirming.

### `golab attach`

A runtime created in the Colab browser UI has no local `golab` record. On the Public API control plane you can attach it by runtime name and record it locally:

```bash
golab attach --runtime NAME [--session NAME]
```

`--runtime NAME` is the runtime name from the Public API (`runtime.name`); `--session NAME` is the local name you will pass to `-s` / `--session` on other commands. This requires `COLAB_CLI_PUBLIC_API=1` or `--public-api`, plus the GCP quota-project prerequisites in [MIGRATION.md](https://github.com/20Youssef10/golab/blob/main/docs/MIGRATION.md#migrating-to-the-public-api). It attaches to the existing runtime; it does not allocate a new one. Run `golab attach --help` to confirm the flag surface in your build.

### `golab status`

```bash
golab status            # all sessions, or the unique one
golab status -s train
```

Each line shows name, endpoint, hardware, shape, variant and `IDLE` / `BUSY (<what>)`. The shape shown is the one reported by the server (overlaid on the locally requested value at print time); the on-disk record is unchanged. A separately indented `Last Execution:` line shows the most recent execution kind, cell label (for notebooks) and timestamp. A `-s` name that does not exist locally prints `Session '<name>' not found.` to stderr.

`golab status --watch [--interval 5s]` refreshes until interrupted; `--interval DURATION` sets the poll period (default `5s`). This is bounded client-side polling, not a server push or a background service.

### `golab stop`

```bash
golab stop -s train     # kernel (best effort) → unassign → remove local state
golab stop              # uses the unique-session rule
golab stop --all        # stop every active session
golab stop -s train --verify          # confirm no assignment remains on the server
```

`--all` asks for confirmation on stderr (`Stop N session(s)? [y/N]`) when stdin is a TTY; without a TTY it refuses to stop anything and exits 2 with a hint. Pass `--yes` (or `COLAB_ASSUME_YES=1`) to skip the prompt or to run non-interactively. It finishes with `Stopped N session(s).` on stderr, where `N` is the number that actually stopped — any per-session failure is reported and makes the command exit 1. Removing local state always happens, even when the backend is already gone. `--verify` re-reads `/tun/m/assignments` after unassigning and exits non-zero if a stopped runtime is still assigned (works with `--all` too); this is an extra GET and is opt-in.

### `golab resume`

```bash
golab resume -s train
golab resume -s train --timeout 0
```

Reattach to an execution interrupted by a transport error and wait for its remaining output and final result **without resubmitting the code**. A bounded in-memory journal (count and byte caps) deduplicates frames replayed after a reconnect and remembers an observed `status:idle`, so a drop between idle and the final reply resumes to a terminal result instead of timing out. If the kernel emitted output strictly while the socket was down, that output cannot be reconstructed — the original error is a typed `ErrOutputGap` and `resume` only recovers what the new socket actually delivers. With no pending execution it exits 1 with a usage hint. `--timeout SECONDS` uses the same idle semantics as `exec` (`0` = unlimited). Successful resume clears the pending record; it never auto-shuts down the session.

### `golab restart-kernel`

```bash
golab restart-kernel -s train
```

Restarts the kernel through the runtime's `/restart` endpoint. It does not change the assignment.

### `golab use`, `config`, `init` and `state migrate`

```bash
golab use train                      # persist default_session in settings.json
golab use --clear                    # clear it
GOLAB_SESSION=train golab status     # per-shell override, no state write
golab config get default_session
golab config set enable_update_check false
golab config unset default_session
golab config list                    # all supported keys and their values
golab config path                    # settings.json location
golab init [--force]                 # scaffold golab.yaml + .golabignore
golab state migrate --dry-run        # report only (the default)
golab state migrate --apply --json   # stamp schema_version, preserve records
```

`config` writes go through the same locked, temp-file + fsync + rename path as other state, and printed values are redacted. Supported keys: `use_public_api`, `enable_update_check` (alias `update_check`), `update_url`, `public_api_supported`, `tunnel_api_supported`, `default_session`. `init` refuses to overwrite existing scaffolding unless `--force` is given.

`state migrate` adds an additive top-level `schema_version` (currently `1`) to `sessions.json`: a legacy file reads as version `0`, a newer version aborts rather than being downgraded, and `--apply` only stamps the key — session records and any unknown fields are preserved verbatim. `--dry-run` is the default; nothing is deleted.

### `golab wait`

```bash
golab wait -s train --for kernel --timeout 60
golab wait -s train --for idle
golab wait --for ready               # default
```

`--for` accepts `kernel`, `idle` or `ready` (default `ready`); `--timeout SECONDS` defaults to `300`. It polls the existing readiness checks with a bounded deadline and allocates nothing.
