# golab user guide

This is the day-to-day reference for `golab`, the unofficial Go Colab CLI. It assumes the binary is on your `PATH`; replace `golab` with `./golab` if you built it locally. For installation and a short tour, see the [README](../README.md).

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

`--runtime NAME` is the runtime name from the Public API (`runtime.name`); `--session NAME` is the local name you will pass to `-s` / `--session` on other commands. This requires `COLAB_CLI_PUBLIC_API=1` or `--public-api`, plus the GCP quota-project prerequisites in [MIGRATION.md](MIGRATION.md#migrating-to-the-public-api). It attaches to the existing runtime; it does not allocate a new one. Run `golab attach --help` to confirm the flag surface in your build.

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

## 2. Executing code

### `golab exec`

Run Python from a file, from a notebook, or from piped stdin.

```bash
golab exec -s train -f solve.py
golab exec -s train -f solve.py --env DATASET=/content/data.csv --env EPOCHS=3
echo 'print(2 + 2)' | golab exec -s train
cat analysis.py | golab exec            # unique-session rule applies
golab exec -s train -f notebook.ipynb
golab exec -s train -f solve.py --json          # NDJSON records on stdout
golab exec -s train -f solve.py --secret API_KEY --env-file .env
```

| Flag | Default | Meaning |
|---|---|---|
| `-s`, `--session` | auto | Target session. |
| `-f`, `--file` | stdin | File to execute. |
| `--env KEY=VALUE` | none | Set a variable in the remote kernel before the code runs; repeatable. Keys must match `^[A-Za-z_][A-Za-z0-9_]*$`. |
| `--timeout SECONDS` | `30` | Seconds without output before failing; `0` disables the idle timeout. |
| `--output-image PATH` | none | Save a displayed image to this path (otherwise images render inline on a TTY). |
| `--json` | off | Emit one NDJSON record per cell on stdout (`cell_index`, `cell_id`, `status`, `stdout`, `stderr`, `data`, `images`, `error`); progress stays on stderr and exit codes are unchanged. |
| `--secret NAME` | none | Resolve a local secret from the process environment or OS keyring and inject it into the kernel. Never persisted, logged or printed. Repeatable. |
| `--env-file PATH` | none | Load `KEY=VALUE` pairs from an owner-only (0600 on Unix) file; malformed keys exit 2. Repeatable. |

Details worth knowing:

- Every execution first runs `os.makedirs('/content', exist_ok=True); os.chdir('/content')`, so relative paths are relative to `/content` on the VM.
- With no `-f` and a TTY on stdin, `golab exec` refuses to wait: you get `Error: No input provided. Pipe code or provide a file.` and exit 1. Empty input exits 0 without allocating a kernel.
- Values passed via `--env` are emitted as quoted Python literals, never spliced as raw source.
- `--secret` and `--env-file` are a **local-only** bridge: values are injected with the same safe quoting, but are removed from stdout, stderr, history and JSON records. Colab Web UI secrets are deliberately not read (no browser/cookie access).
- With `--json`, each cell produces one machine-readable record on stdout; the event writer redacts secret values the same way as text mode.
- For a `.ipynb` input, code cells run in order. Progress is printed to stderr as `Executing cell i/N - <label>...`, where the label is a cell's `# @title …` line or its cell ID. Outputs are written back into the cells and saved next to the input as `<name>_output.ipynb` with owner-only permissions.
- Exit codes follow CPython semantics: `sys.exit(N)` → `N`, `sys.exit("msg")` → stderr + exit 1, uncaught errors → exit 1, clean cells → exit 0. A notebook stops at the first failing cell but still writes the output file.
- Output streaming is sanitised: unsafe terminal control sequences are stripped, SGR colour is kept.
- A terminal 401/404 prunes the lost session from local state and reports it on stderr.

### `golab repl`

```bash
golab repl -s train                 # interactive, syntax-highlighted
printf '1+1\n' | golab repl -s train # piped mode, exits on EOF
golab repl -s train --output-image plot.png
```

Piped mode is CI-safe; interactive TTY mode needs a real terminal. Both record `execution` events in the session history.

### `golab run`

One-shot: validate the script locally, create a runtime, upload and execute it, then unassign unless `--keep` is given.

```bash
golab run train.py --gpu t4
golab run train.py --gpu t4 --env EPOCHS=5 -- --data /content/data.csv
golab run train.py --keep -s longlived
golab run analysis.ipynb --gpu t4      # code cells only; outputs saved to analysis_output.ipynb
```

| Flag | Meaning |
|---|---|
| `-s`, `--session NAME` | Ephemeral session name (generated if omitted). |
| `--gpu`, `--tpu`, `--high-mem` | Hardware for the runtime it creates. |
| `--env KEY=VALUE` | Repeatable; also accepted after the script name. |
| `--timeout SECONDS` | Idle timeout, default `30`; `0` means no limit. |
| `--keep` | Leave the session running after the script finishes. |
| `--max-cu FLOAT` | Minimum compute-unit balance to maintain during the run (`0` disables). Samples `GET /tun/m/ccu-info`, which is **account-level, not per-session**; no prices are computed. |
| `--enforce` | Interrupt and stop the run when `--max-cu` is breached. Without it a breach is only reported. |
| `--detach`, `--ttl DURATION` | Start the script remotely detached; `--ttl` (strictly positive) is **required** with `--detach` and is now **enforced** by a remote watchdog. `--max-cu` / `--enforce` cannot be combined with `--detach` (exit 2). See "Detached runs" below and [ADR-001](adr-001-detached-runs.md). |
| `--lockfile PATH` | Verify and install this environment lockfile before running. |
| `-p`, `--param NAME=VALUE` | Notebook parameter, repeatable. |
| `--params-file PATH` | JSON/YAML notebook parameters file. |
| `--bundle`, `--out PATH` | Record a provenance manifest (`--out` defaults to `run.json`). |
| `--json`, `--secret NAME`, `--env-file PATH`, `--output-image PATH` | Same semantics as `exec` (per-cell NDJSON, local-only secret bridge, saved image). |

Script semantics:

- The script must exist locally **before** anything is allocated; a typo exits 2 without touching Colab.
- A leading `#!…` shebang is stripped. `sys.argv` is set to the script basename followed by your arguments, and `__name__` is set to `'__main__'`, so `if __name__ == "__main__":` blocks work.
- Flags after `SCRIPT` belong to the script, not to `golab`. Put `golab` flags **before** the script name; the sole exception is `--env KEY=VALUE`, which is recognised on either side.
- A `.ipynb` input is parsed **before** any VM allocation: code cells run in order on one kernel with shared state, markdown is skipped, and outputs are written next to the input as `<name>_output.ipynb` with mode 0600. Extra script arguments are warned about and never become `sys.argv` for notebooks. A failing cell stops the run with the cell's exit code.
- Exit codes follow CPython: `sys.exit()` / `sys.exit(0)` → 0, `sys.exit(N)` → N, `sys.exit("message")` → 1 with the message on stderr, an uncaught exception → 1 with a traceback.
- SIGINT, SIGTERM and SIGHUP tear the runtime down; an interrupt while piped exits 130. `--keep` disables teardown.
- A transport interruption (connection lost or timeout) does **not** tear the runtime down: the command reports a typed output-gap error with a `colab resume -s NAME` hint and keeps the session for recovery. See `golab resume` below.

### Spend guard for `run`

```bash
golab run train.py --max-cu 10 --enforce
```

`--max-cu FLOAT` is a **minimum reserve** sampled from `GET /tun/m/ccu-info`. That endpoint is **account-level**, so the sampled balance and any delta belong to the account, not to this one VM; no price is inferred. Without `--enforce`, a breach is reported; with it, the run is interrupted and the session stopped. `--max-cu 0` disables the monitor. The guard is **foreground-only**: `run --detach` returns after submitting the remote child and does not keep the in-run monitor alive, so supplying `--max-cu` or `--enforce` together with `--detach` is a usage error (exit 2) before any allocation. Wiring a remote CU sampler is deferred; see [ADR-001](adr-001-detached-runs.md).

### Detached runs

```bash
golab run --detach --ttl 1h train.py     # --ttl is mandatory
golab jobs                               # status, exit code, duration
golab logs <ID>                          # remote log
golab logs <ID> -f --interval 2s --max 30  # bounded follow
```

The job is a **remote** child process on the VM. There is no local daemon, no keep-alive thread and no WebSocket held open after submission; `jobs`, `logs` and `wait` poll on demand. The VM **stays allocated and billable** until you `golab stop` it.

`--ttl` must be strictly positive (otherwise exit 2). It is written to the VM's `meta.json` and to a small credential-free local manifest and shown by `jobs`. It is now **enforced, not advisory**: the launcher starts a small remote watchdog (own session, `start_new_session=True`) that reads the deadline and a 5-second poll interval from the run metadata. At the deadline it kills the whole process group and records `exit=124` with `ttl-reason=ttl_exceeded`. The watchdog stops the **job**, not the runtime — the VM stays allocated and billable until you `golab stop` it, and a watchdog cannot survive VM reclamation or unassignment. The local manifest can drift from VM reality; a missing remote file is reported as unknown/stale, never as success. Follow mode is bounded by `--interval` × `--max`; remote output is visible only on the next poll. The design, amendment and explicit non-goals are in [ADR-001](adr-001-detached-runs.md). The watchdog is fake-tested; a billable CPU e2e covered `run --detach` + `jobs`/`logs`, but not a deadline expiry (see [CONFORMANCE.md §6](CONFORMANCE.md#6-human-only--external-steps-blocking-done)).

### Reproducibility

```bash
golab env freeze -s train -o env.lock.json        # Python, pip freeze, GPU
golab run train.py --lockfile env.lock.json
golab run notebook.ipynb -p EPOCHS=3 --params-file params.json
golab run train.py --bundle --out run.json
golab bundle record --out run.json --file train.py --file data.csv --env KEY=VALUE
golab bundle verify run.json --files-root .
golab bundle verify run.json --env              # re-check in a FRESH VM
```

`env freeze -s NAME -o FILE` records the session's Python version, `pip freeze` output and GPU into a deterministic lockfile. `run --lockfile` verifies and installs it before the script runs. `-p` / `--params-file` set notebook parameters. `bundle record` writes a deterministic manifest of local inputs; `bundle verify` without `--env` checks **recorded metadata only** (and re-hashes local files when `--files-root` is given). `--env` actually allocates a fresh VM and re-checks the environment there — that is the only form that executes anything. No VM is allocated by `record` or metadata-only `verify`.

## 3. Files

Remote paths are relative to the VM root; the default for `ls` is `content`.

```bash
golab ls -s train                  # list /content, directories first with a trailing /
golab ls -s train content/data     # a directory
golab rm -s train content/old.csv
golab upload -s train data.csv content/data.csv
golab upload -s train -r ./dataset content/dataset
golab download -s train content/results.csv ./results.csv
golab download -s train -r content/checkpoints ./checkpoints
golab edit -s train content/notes.txt
```

- `upload` streams a single file into the Contents API in bounded chunks; small files use one atomic `chunk:1` PUT. Directories require `-r` / `--recursive`, with symlink and traversal guards.
- `download` writes to a private temporary file in the destination directory and renames it into place, so an interrupted transfer cannot leave a partial file. Directories require `-r` / `--recursive`.
- `edit` downloads to a private temp file (keeping the remote extension), opens `$VISUAL`, then `$EDITOR`, or a platform default (`notepad` on Windows, `nano` on Termux, `vi` elsewhere), compares SHA-256 before and after, and uploads only if the file actually changed. Only a 404 is treated as "new file"; any other download error aborts rather than risking overwriting the remote file with an empty buffer.
- These operations record `file_operation` events in the session history.

### VM inspection

These commands read the running VM through server-side execution/polling; they do not mount a local filesystem or hold a WebSocket open. Relative paths for `tail`, `grep`, `du` and `fetch` are resolved against the VM root (`/`), matching `upload` and `cat`; already-absolute paths are unchanged. `find NAME` always searches `/content`.

```bash
golab ps -s train --json                     # alias: golab top
golab cat -s train content/out.txt
golab tail -s train content/train.log -n 50 -f --interval 2s
golab grep -s train ERROR content/ -i --glob '*.log' --max-matches 50
golab du -s train content/ --max-entries 100
golab find -s train checkpoint --max-results 50
golab fetch -s train https://example.com/data.zip content/data.zip \
      --sha256 <hex> --timeout 600
```

| Command | Flags (beyond `-s`) |
|---|---|
| `ps` / `top` | `--json`, `--timeout` (idle seconds, default `20`). Processes, GPU, memory and disk snapshot. |
| `cat REMOTE` | none; binary-safe. |
| `tail REMOTE` | `-n`/`--lines` (default `10`), `-f`/`--follow`, `--interval` (default `2s`), `--timeout` (default `15`). |
| `grep PATTERN PATH` | `-i`/`--ignore-case`, `--glob`, `--max-matches` (default `200`). |
| `du PATH` | `--max-entries` (default `200`). |
| `find NAME` | `--max-results` (default `200`); searches under remote `/content`. |
| `fetch URL [DEST]` | `--sha256 HEX`, `--timeout` (seconds, default `600`); `wget` with a `curl` fallback. |

`tail -f` polls server-side with a byte offset; it is **not byte-perfect across log rotation** — a truncated or replaced file can skip early lines or re-read unrelated bytes. Treat it as a live tail for observing new output, not as a complete log archive. `fetch --sha256` verifies the downloaded bytes against the expected digest. All output is sanitised before it reaches the terminal.

## 4. Remote access: `ssh`

`golab ssh` uses an embedded SSH client over the runtime's WebSocket. You do not need a system `ssh`, `ssh-keygen`, or an OpenSSH installation.

```bash
golab ssh                              # interactive; auto-creates a runtime if you have none
golab ssh -s train                     # attach to a named session
golab ssh -s train -- pwd && ls        # run a command; returns the remote status
golab ssh -s train -- 'python -c "print(1)"'
golab ssh --proxy-mode -s train        # stdio bridge for an external OpenSSH ProxyCommand
golab ssh -s train --force             # kill stale sshd children first
golab ssh --gpu t4 --rm                # auto-create, then unassign on exit
golab ssh -s train --accept-host-key SHA256:AbCdEf... -- echo ok
```

| Flag | Meaning |
|---|---|
| `-s`, `--session NAME` | Target session; see auto-create rules below. |
| `-i`, `--identity PATH` | Public `.pub` or private key path used for the `X-Colab-Ssh-Pubkey` header. |
| `--gpu`, `--tpu`, `--high-mem` | Hardware, **only** when `ssh` creates the runtime. Otherwise a warning is printed. |
| `--rm` | Unassign the runtime when the session ends. Applies to auto-created runtimes and proxy bridges; ignored with `-- CMD` and with a reused interactive session. |
| `--force` | Kill existing SSH sessions on the runtime via the kernel (10-second bound) before dialling. Diagnostics go to stderr. |
| `--proxy-mode` | Act as a WebSocket-to-stdio bridge compatible with OpenSSH `ProxyCommand`. Takes no command. |
| `--accept-host-key SHA256:FP` | Explicitly pin the runtime host key by SHA-256 fingerprint (the `SHA256:` prefix is optional). The value is validated before allocation; the pin is stored only after the live handshake matches it, and a stored pin must match the explicit value. A mismatch or malformed fingerprint is a usage error (exit 2). This is an explicit alternative to trust-on-first-use, not a way to bypass verification. |

Modes and auto-creation:

- **Interactive** (default) auto-creates a runtime only when your local store is empty. The shell lands in `/content`.
- **`ssh -- CMD…`** requires an existing session — it never allocates. One argument is passed as a shell line; several arguments are POSIX-quoted into an argv. The remote command's exit status is returned.
- **Proxy mode** creates a named session on first connect if that name is not known locally. It is intended for tools that speak raw SSH over stdio.
- Key discovery order is `--identity`, then an SSH agent, then `~/.ssh/id_ed25519` / `~/.ssh/id_ecdsa`. RSA keys are rejected locally with guidance to use Ed25519.

If Colab reports **429** ("too many SSH sessions"), first try `golab ssh --force -s NAME`; if that does not help, stop stale sessions with `golab stop`. Piped stdout is kept byte-exact: progress goes to stderr, and the automatic update check is skipped for piped `ssh`.

## 5. Automation inside the VM

```bash
golab install -s train numpy pandas
golab install -s train -r requirements.txt
golab auth -s train                    # hidden command; interactive
golab drivemount -s train              # default mount: /content/drive
golab drivemount -s train /content/mydrive
```

- `install` prefers `uv pip install --system` and falls back to `python -m pip`. Package names are embedded as a JSON list decoded remotely, never spliced into generated Python. `-r FILE` uploads a local requirements file to `content/` first.
- `auth` (hidden) runs `google.colab.auth.authenticate_user()` with `USE_AUTH_EPHEM=0`. It is interactive and bounded to a 10-minute idle timeout.
- `drivemount` runs `drive.mount(PATH)` and intercepts Colab's `dfs_ephemeral` request. It prints and tries to open the consent URL, then waits for you to press Enter before confirming propagation. Failures are reported as a short status plus a sanitised detail string — raw HTML from the server is never echoed. Approving Drive access with a **different** Google account than the one `golab whoami` reports is a common cause of propagation failure.
- `--wait-for-consent` replaces the Enter prompt with bounded polling of the propagation endpoint (2 s interval, 5 min limit); global `--yes` also skips the prompt. The browser approval itself is still human-only — this only automates the wait.

Both `auth` and `drivemount` are interactive flows that still need a human live verification; see [REMEDIATION.md](REMEDIATION.md).

## 6. Utilities and Go-only extras

### Upstream-compatible utilities

| Command | What it does |
|---|---|
| `golab usage` | Print compute-unit balance, hourly rate and active assignments. |
| `golab cost [--since DURATION] [--json]` | Report observed compute-unit samples (CU only, no prices) from a private local ledger fed by `GET /tun/m/ccu-info`; account-level, not per-session. `--since` defaults to `24h`. |
| `golab url -s train [--host URL] [--open]` | Print the browser URL that connects to a session; `--open` also launches it via `termux-open-url`, `wslview`, `xdg-open`, `open` or `rundll32`. |
| `golab log -s train [-n 20] [-t execution] [-o report.md]` | View history. `-o` chooses the exporter by extension: `.ipynb`, `.md`, `.txt` or `.jsonl`. |
| `golab pay` | Open the Colab compute-unit sign-up page. |
| `golab whoami` | *(hidden)* Print auth provider, email, audience, scopes and token expiry — never the token. |
| `golab readme` / `golab skill` | Print the bundled project README / agent skill text. Uppercase aliases are hidden. |
| `golab help [COMMAND]` | Help for a command; unknown names exit 2. |
| `golab completion bash\|zsh\|fish\|powershell` | Generate shell completion scripts. |
| `golab version` | `golab 0.4.0 (compatible with google-colab-cli 0.8.x+HEAD)`. |
| `golab update [--install] [--verify-only PATH]` | Check GitHub Releases; with `--install`, verify checksums, cosign signature and signed SBOM before replacing the binary. `--verify-only PATH` verifies a **local** artifact (checksum + cosign + SBOM; `--checksums`, `--signature`, `--sbom` override the sibling defaults) without installing anything. |
| `golab logout` | Clear the local OAuth token, cached proxy tokens and capability probes so you can switch accounts. Under `--auth=adc`, print `gcloud auth application-default revoke` guidance instead. |
| `golab test-public-api` | *(hidden)* Probe Public and Tunnel API reachability and cache the result. |

### Go-only extras

These are additive commands with no Python counterpart:

| Command | What it does |
|---|---|
| `golab doctor [--live] [--json] [--section SECTION] [--timeout D] [--fixtures DIR] [--fix [--apply]]` | GET-only, redacting connectivity and configuration probe. `--live` additionally requires `GOLAB_LIVE_PROBES=1` and is disabled when `LIVE_E2E_ENABLED=false`. Sections: `config`, `auth`, `api`, `update`, `wire`. `--fix` plans exact local remediations; it is **dry-run by default and makes no network call**. `--apply` applies only the safe, reversible fixes it identifies (never deletes files or widens permissions). |
| `golab audit [--json]` | Local-only report on token-store tier, file modes, API capability cache and update verification; no request is made and no credential value is printed. |
| `golab budget [--profile F] [--profile-name N] [--max-balance N] [--max-hourly N] [--enforce] [--json]` | Advisory compute-unit decision from `GET /tun/m/ccu-info`; never allocates. Without a profile it reports the current usage. `--max-balance` is a minimum reserve floor (deny at or below); `--max-hourly` is a rate ceiling (deny at or above). |
| `golab bundle record [--out F] [--file P …] [--env K=V …] [--root DIR] [--shape S] [--variant V] [--accelerator A]` / `golab bundle verify MANIFEST [--files-root DIR] [--env]` | Create and verify a deterministic provenance manifest of local inputs. Verify is metadata-only unless `--env` is given, which allocates a fresh VM to re-check the recorded environment. |
| `golab sync LOCAL REMOTE [-s NAME] [--direction up\|down\|both] [--apply] [--delete] [--diff] [--include-outputs] [--watch] [--interval D] [--iterations N]` | One-shot directory sync with conflict detection. Dry-run is the default; `--apply` writes. Conflicts fail without changing anything. `--diff` compares two notebooks with volatile metadata and outputs stripped. `--watch` re-runs sync with bounded polling (no fsnotify). |
| `golab compat report [--json] [--snapshot FILE]` | Compare the live command tree against an **embedded self-snapshot** baked into this binary, or against `FILE` from `compat refresh`. By default it performs no network I/O and is not a comparison with the Python CLI or a live upstream release; the report labels the snapshot kind and source. |
| `golab compat refresh [--out FILE] [--ref REF]` | Fetch the official upstream Typer CLI source from `raw.githubusercontent.com` at a pinned ref and write a provenance-tagged, explicitly **partial** surface snapshot. Dry-run by default (`--out -` prints JSON to stdout); never synthesises flags from parameter names. This is a source parse, not a live upstream comparison. See "Compatibility snapshots" below. |
| `golab trace -- <command> [args] [-o FILE] [--unsafe-bodies]` | Wrap a command and append a redacted wire trace as NDJSON. Headers and **query values are never written**; only query *keys*, body shape and a SHA-256 hash are recorded. `--unsafe-bodies` additionally includes redacted JSON bodies and labels those events `UNSAFE` — review the file before sharing it. |
| `golab agentout [--format text\|json\|ndjson] [--type info\|output\|error] [text …]` | Emit one typed structured-output event; handy for testing a schema pipeline without a VM. |

### Compatibility snapshots

`compat report` still compares against a self-snapshot by default. To check
against the official upstream surface instead:

```bash
golab compat refresh --out upstream.json --ref v0.8.0   # pinned ref; dry-run without --out
golab compat report --snapshot upstream.json --json
```

The refresh fetch is bounded (2 MiB, explicit connect/TLS/header/idle timeouts,
redirect cap) and uses only `https://raw.githubusercontent.com` — no flag or
environment variable can redirect a release build to another host. The snapshot
records its ref, source URL, SHA-256 of the fetched source, capture time and the
extraction method (`typer-source-literals`), and is labelled
`upstream-fetched`. It is deliberately partial: only literal long options in
explicit `typer.Option(...)` calls and explicit command registrations are
captured, so "missing" flags in a report can be extraction gaps. Failure to find
any recognisable command surface is a loud error, never an empty report. The
default `--ref` is compatibility text (`0.8.x+HEAD`), not a guaranteed git tag;
pass a real tag such as `v0.8.0` for a reproducible snapshot.

## 7. Structured output, plans and confirmations

### `--output text|json|ndjson`

The global `--output` flag (or `COLAB_OUTPUT`) wraps stdout in the stable **`golab/1`** schema. In `text` mode output is unchanged. In `ndjson` mode each stdout write becomes one line:

```json
{"schema":"golab/1","type":"output","text":"[train] m-s-abc123 | Hardware: T4 | Shape: Standard | Variant: GPU\n"}
```

In `json` mode stdout is a single document finalised at exit:

```json
{"schema":"golab/1","events":[{"schema":"golab/1","type":"output","text":"..."}]}
```

There are exactly three event types: `info`, `output` and `error`. Diagnostics written by `golab` itself always stay on stderr and are not part of this schema. Every text value passes through the redaction filter and there is no token field. Adding a field is compatible; renaming or removing one requires a new schema version.

### `--explain`

`--explain` prints a redacted, deterministic description of what a billable command would do, then exits **before authentication, state mutation or any network call**:

```bash
golab new -s train --gpu t4 --explain
# [golab] explain: accelerator=T4 api=tunnel billable=true command=golab new creates-session=true method=POST operation=assign session=train shape=STANDARD variant=GPU
```

It currently applies to `new`, `run` and `ssh`. The output contains no token, URL or request body, so it is safe to paste into a ticket.

### `--yes`

`--yes` (or `COLAB_ASSUME_YES=1`) suppresses non-destructive confirmations such as `stop --all`. It **never** bypasses a budget guard; enforcement is controlled only by an optional `golab.yaml` profile (see `GOLAB_BUDGET_FILE` below), which accepts `defaults.budget.enforce: true` and per-profile overrides. The persistent `--budget-profile NAME` flag (or `GOLAB_BUDGET_PROFILE`) selects which named profile the guard loads; it defaults to `default`.

## 8. Exit codes and piping

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | Generic failure: unknown command, lost session, API failure, verification failure, failed budget guard. |
| `2` | Usage or missing prerequisite: bad flag value, missing script, unusable SSH key, invalid budget profile, `help` with an unknown name. |
| `130` | Interrupted (SIGINT / context cancellation). |
| Remote / N | `ssh -- CMD` returns the remote status (`255` for an SSH-level error or signal-killed remote); `run` mirrors the script's `sys.exit(N)`. |

Piping rules:

- Code for `exec` comes from a file (`-f`) or stdin. Piped REPL input exits on EOF. `run` accepts a local script path only.
- When stdout is not a TTY, inline-image escape sequences are suppressed; use `--output-image` if you want the image on disk.
- `ssh --proxy-mode` and `ssh -- CMD` treat stdout as a raw byte stream; the update notice is skipped and all diagnostics go to stderr.
- The throttled update check (at most once every 24 hours) never writes to stdout and never blocks the command.

TTY behaviour matters in exactly three places: `exec` with no input errors when stdin is a TTY; `stop --all` only prompts when stdin is a TTY; interactive `repl` selects its rich frontend only when stdin is a TTY.

## 9. Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `COLAB_AUTH` | `oauth2` | Auth provider: `oauth2` or `adc`. |
| `COLAB_SCOPES` | `full` | Default for `--scopes`: `full` or `minimal`. |
| `COLAB_DEBUG` | unset | `1`/`true` enables debug logging on stderr. |
| `COLAB_CLI_PUBLIC_API` | `0` (tunnel API) | Use the Public API control plane; `--public-api` / `--no-public-api` override it. Prerequisites: [MIGRATION.md](MIGRATION.md#migrating-to-the-public-api). |
| `COLAB_OUTPUT` | `text` | Default for `--output`. |
| `COLAB_ASSUME_YES` | unset | Default for `--yes`. |
| `GOLAB_BUDGET_PROFILE` | `default` | Named `golab.yaml` profile used by the budget guard on `new`/`run`/`ssh`; the persistent `--budget-profile` flag overrides it. |
| `GOLAB_SESSION` | unset | Default session name, between an explicit `-s` and the `golab use` setting in resolution order (`-s` > env > default > unique). |
| `GOLAB_TRACE` | unset | Path to an NDJSON wire trace; any command appends redacted events when this is set. `trace -- <cmd>` passes the same configuration without mutating the process environment. |
| `GOLAB_TRACE_UNSAFE_BODIES` | unset | `1`/`true`/`yes`/`on` adds redacted JSON bodies to the trace; every such event is labelled `UNSAFE`. Never share an unsafe trace unreviewed. |
| `COLAB_CONFIG_DIR` | OS-specific | Override the `colab-cli` configuration directory. |
| `COLAB_SESSIONS_FILE` | `<config dir>/sessions.json` | Override the session-state file. |
| `COLAB_CLI_REQUEST_TIMEOUT` | `60` (seconds) | Per-request timeout, clamped to `[1, 86400]`. |
| `COLAB_CLI_ASSIGN_RETRIES` | `3` | Assignment retries, clamped to `[0, 10]`; `0` disables retries. |
| `COLAB_CLI_RETRY_BACKOFF` | `10` (seconds) | Retry delay, clamped to `[0, 300]`; `0` disables it. |
| `GOLAB_BUDGET_FILE` | `golab.yaml` | Budget profile consulted before billable commands. |
| `GOLAB_LIVE_PROBES` | unset | `1` allows `golab doctor --live`. |
| `LIVE_E2E_ENABLED` | unset | `false` is a kill switch for live probes. |
| `NO_COLOR` | unset | Disable coloured terminal output. |
| `VISUAL`, `EDITOR` | platform default | Editor used by `golab edit`. |
| `TMPDIR`, Termux `PREFIX` | OS temp dir | Location for temporary files. |
| `XDG_CACHE_HOME` | `~/.cache` | Base for the cache directory. |
| `SSH_AUTH_SOCK` | — | Agent socket consulted for SSH keys. |

`--scopes minimal` requests only `openid`, `email` and `colaboratory`; it cannot mount Drive (no `drive.file`). Switching modes forces a fresh browser consent on the next command, and existing full-mode tokens are never silently downgraded. MCP-style per-notebook approval is not implemented.

There is deliberately no `--debug` flag; debug logging is enabled only with `COLAB_DEBUG=1` (or `true`).

Variables named `COLAB_TEST_*` are internal test hooks and are not user settings.

## 10. Security and privacy

- Tokens, OAuth codes/verifiers, client secrets and `Authorization` / `Cookie` / proxy-token headers are wrapped in a secret type. They never appear in stdout, logs, error messages or argv. Free-form remote text is redacted defensively before display.
- Only the refresh token is persisted; access tokens are minted on demand. The keyring is probed with a 5-second timeout and falls back to a `0600` file. See [FAQ.md](FAQ.md#where-are-my-credentials-stored) for paths.
- State is written under a lock to a temp file, fsynced and atomically renamed. A corrupt file aborts loudly; it is never treated as empty.
- Untrusted remote output is sanitised before it reaches the terminal (OSC/DCS/APC and query sequences stripped, SGR retained). `edit` aborts on non-404 download errors; uploads are bounded and verify final size.
- TLS verification is always on. There is no host override in release builds.

## 11. Example workflows

### Run a training script on a T4 and clean up

```bash
golab new -s train --gpu t4
golab upload -s train data.csv content/data.csv
golab exec -s train -f setup.py --env EPOCHS=10
golab download -s train content/model.pt ./model.pt
golab stop -s train
golab sessions                 # should be empty
```

Or as a single one-shot command: `golab run train.py --gpu t4 --keep` if you want to keep the VM afterwards.

### Mount Drive and execute a notebook

```bash
golab new -s drive --gpu l4
golab drivemount -s drive /content/drive     # follow the consent URL, press Enter
golab exec -s drive -f analysis.ipynb        # outputs saved to analysis_output.ipynb
golab stop -s drive
```

### Agent / CI pipeline with JSON output

```bash
export COLAB_OUTPUT=ndjson COLAB_ASSUME_YES=1
golab new -s ci --gpu t4 --explain           # inspect the plan, no allocation
golab run validate.py --gpu t4               # machine-readable output on stdout
golab stop --all
golab audit --json                           # credential/file-mode report, offline
```

### Spend-safe batch run

```bash
golab reap --dry-run                          # inspect orphans before doing anything
golab new -s batch --ttl 2h --ttl-action warn
golab run validate.py --max-cu 10 --enforce   # guarded foreground run
golab run --detach --ttl 1h train.py          # remote child; no local monitor
golab jobs                                    # find the detached run ID
golab logs <ID> -f --interval 2s --max 30
golab stop -s batch                           # the watchdog ends the JOB at TTL; the VM does NOT stop itself
golab sessions                                # should be empty
```

`cost` can be consulted at any time for the account-level CU history; `wait --for ready --timeout 120` is a bounded readiness check that allocates nothing.

All stdout in the JSON pipeline above is `golab/1` JSON; `[colab] …` progress stays on stderr, so `jq`, a log shipper or an agent can consume the stream without filtering chatter.

## 12. MCP server (`golab-mcp`)

`golab-mcp` is a **separate static binary** that exposes the CLI as a Model
Context Protocol server. It is not a subcommand of `golab` and is not shipped in
the release archives yet (see the caveat below).

```bash
go install github.com/20Youssef10/golab/cmd/golab-mcp@latest

golab-mcp                                   # stdio transport (default)
golab-mcp --transport stdio
golab-mcp --transport http --addr :8080
golab-mcp --golab-bin /path/to/golab         # binary invoked by tool calls
```

| Flag | Default | Meaning |
|---|---|---|
| `--transport` | `stdio` | `stdio` for desktop agents, or `http` for streamable HTTP. Any other value exits 2. |
| `--addr` | `:8080` | HTTP listen address; used only with `--transport http`. |
| `--golab-bin` | `golab` | Path to the `golab` binary that tool calls invoke. A missing binary is a startup warning, not a fatal error: `tools/list` must still work. |

Go's `flag` package accepts both `-flag` and `--flag`; the forms above are
equivalent. In stdio mode nothing but SDK JSON-RPC is written to stdout — all
diagnostics go to stderr (`COLAB_DEBUG=1` enables debug logging).

**Tools and safety.** The server exposes 22 allow-listed `colab_*` tools: 15
read-only (`colab_sessions`, `colab_status`, `colab_usage`, `colab_cost`,
`colab_ps`, `colab_cat`, `colab_tail`, `colab_grep`, `colab_du`, `colab_find`,
`colab_logs`, `colab_wait`, `colab_jobs`, `colab_doctor`, `colab_compat`) and 7
that mutate billable/remote state (`colab_new`, `colab_run`, `colab_exec`,
`colab_stop`, `colab_reap`, `colab_fetch`, `colab_resume`). There is no shell
tool, no arbitrary-code tool and no raw-request tool. **Every mutating tool
requires an explicit `confirm: true` in its arguments**; without it the call
fails before invoking `golab`. `colab_reap` is dry-run by default — it needs both
`dry_run: false` and `confirm: true` to delete records. Code for `colab_exec` is
passed on stdin, never in argv or a shell. Tool errors and output are redacted
with the same filter as the CLI.

**Resources and transport.** Two read-only resources are available:
`golab://docs/skill` and `golab://docs/usage`. Over HTTP the streamable MCP
endpoint is `/mcp` (the root `/` is also accepted for client compatibility);
there is **no** legacy `/sse` endpoint. The implementation uses the official
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
(v1.7.0), which negotiates the protocol version; tests drive both the modern
handshake and a legacy client requesting `2025-06-18`, confirming the same tool
list.

**Packaging:** as of the v0.4.0 release, `.goreleaser.yaml` builds
`golab-mcp` alongside `golab` and ships both binaries plus the two Agent Skills
in every signed archive. `install.sh` / `install.ps1` install both binaries and
the skills (skip with `--no-skills` / `-NoSkills`); the GHCR image carries them
too. `golab-mcp` has no separate `update` command — replace it with the next
signed release archive. `go install …/cmd/golab-mcp@latest` remains available.
Tool behaviour is covered by fake-backed tests; that is not a claim of live
Colab proof. See [CONFORMANCE.md §6](CONFORMANCE.md#6-human-only--external-steps-blocking-done).

## 13. Agent Skills discovery

Two portable Agent Skills live in the repository for agents that load skills
from a filesystem. They are also shipped in release archives, installed by the
release installers, and bundled in the GHCR image under
`/usr/share/golab/skills`:

| Path | Purpose |
|---|---|
| `.agents/skills/golab-operations` | Safe operating procedure for the CLI: session lifecycle, `exec`/`run` parity, file transfer, VM inspection, detached jobs, spend guards, redaction, stdout/stderr contract, TTY and `--yes` rules, and remote-vs-local path semantics. |
| `.agents/skills/colab-mcp` | Setting up and verifying `golab-mcp` from MCP clients: stdio and streamable HTTP, Claude Desktop `mcpServers` config, tool categories, `confirm: true` gating, redaction, and MCP Inspector checks with modern and legacy protocol eras. |

Each is a directory with a `SKILL.md` frontmatter (`name`, `description`,
`metadata`) and a `references/` subdirectory. Both validate with the official
`agentskills` CLI:

```bash
agentskills validate .agents/skills/golab-operations
agentskills validate .agents/skills/colab-mcp
```

These skills carry no live proof themselves: they document the CLI surfaces
above, whose verification status is recorded in
[CONFORMANCE.md](CONFORMANCE.md). Command and flag facts in them were checked
against `golab --help` and `<command> --help`; re-check the installed binary with
`golab version`, because flags can drift between versions.
