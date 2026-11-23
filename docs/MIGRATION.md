# Migrating from Python `colab` to `golab`

If you already use Google's Python CLI (`google-colab-cli`, command `colab`), you can switch with **no re-login and no state conversion**. `golab` targets upstream HEAD `0abf82d` (`v0.8.0` plus five commits) and deliberately shares its state layout and command surface.

> `golab` is unofficial. “Google” and “Colab” are trademarks of Google LLC.

## Quick switch

```bash
# 1. Install golab (choose one; see README.md)
go install github.com/20Youssef10/golab/cmd/golab@latest

# 2. Sanity-check that you are running the Go binary, not a stale Python one
which colab && colab version     # names the Python implementation
golab version                    # golab 0.1.0 (compatible with google-colab-cli 0.8.x+HEAD)

# 3. Your existing sessions should already be visible
golab sessions
golab status
```

A globally installed Python `colab` can shadow binaries on your `PATH`. Always invoke `golab` by its own name (or full path) during the transition; the two tools can coexist.

## Command mapping

The following are deliberate equivalents. Flags are the same where practical; differences are noted.

| Python `colab` | `golab` | Notes |
|---|---|---|
| `colab new` | `golab new` | Same `-s`, `--gpu`, `--tpu`, `--high-mem`. Names validated before any allocation. |
| `colab sessions` | `golab sessions` | Same output shape; server-only assignments show as `[?]`. |
| `colab status` | `golab status` | `-s`, `IDLE`/`BUSY`, last execution. |
| `colab stop` | `golab stop` | Adds `--all` with confirmation. |
| `colab restart-kernel` | `golab restart-kernel` | – |
| `colab exec` | `golab exec` | `-f`, `--env`, `--timeout`, `--output-image`; `.ipynb` cells and `# @title`. |
| `colab repl` | `golab repl` | Piped stdin supported; rich TTY frontend when interactive. |
| `colab run` | `golab run` | Shebang, argv and `sys.exit()` semantics preserved. |
| `colab ls` | `golab ls` | Default remote path `content`. |
| `colab rm` | `golab rm` | – |
| `colab upload` | `golab upload` | Adds `-r` / `--recursive` (upstream PR #267); streams and guards paths. |
| `colab download` | `golab download` | Adds `-r`; writes atomically with owner-only permissions. |
| `colab edit` | `golab edit` | Only a 404 means “new file”; other errors abort (upstream D-11 fix). |
| `colab install` | `golab install` | `-r REQUIREMENTS`; packages embedded as JSON, never spliced. |
| `colab ssh` | `golab ssh` | Embedded SSH client; adds `--force`. `--proxy-mode`, `-i`, `--rm`, `-- CMD` all present. |
| `colab usage` | `golab usage` | – |
| `colab url` | `golab url` | `--open` now really launches a browser (Termux/WSL aware). |
| `colab log` | `golab log` | `-n`, `-t`, `-o` with `.ipynb`/`.md`/`.txt`/`.jsonl` exporters. |
| `colab pay` | `golab pay` | – |
| `colab update` | `golab update` | `--install` verifies signed GitHub Releases instead of PyPI/pip. |
| `colab logout` | `golab logout` | Clears token + cached proxy tokens + capability flags (PR #266). |
| `colab auth` *(hidden)* | `golab auth` *(hidden)* | Interactive; 10-minute idle timeout. |
| `colab whoami` *(hidden)* | `golab whoami` *(hidden)* | Prints identity/scopes/expiry; never the token. |
| `colab readme` / `README` | `golab readme` / `README` | Bundled project text (uppercase alias hidden). |
| `colab skill` / `SKILL` | `golab skill` / `SKILL` | Our own agent skill text, not upstream's stale copy. |
| `colab test-public-api` *(hidden)* | `golab test-public-api` *(hidden)* | Probes both control-plane paths and caches the result. |
| `colab help` | `golab help` | Cobra-backed. |
| — | `golab completion bash\|zsh\|fish\|powershell` | New in Go. |
| — | `golab doctor`, `audit`, `budget`, `bundle`, `sync`, `agentout` | Go-only extras; see [USAGE.md](USAGE.md#6-utilities-and-go-only-extras). |
| `colab console` | — | Removed upstream; not ported. |

## State and schema compatibility

`golab` uses the same base directory name as the Python CLI, so an existing installation is found automatically.

| Platform | Base directory | Override |
|---|---|---|
| Linux / macOS / Termux | `~/.config/colab-cli/` | `COLAB_CONFIG_DIR` |
| Windows | `%APPDATA%\colab-cli\` | `COLAB_CONFIG_DIR` |

- **`sessions.json`** — same session records. `golab` additionally persists `runtime_name` (used by the Public API) and refreshes proxy tokens in place. `--config PATH` (or `COLAB_SESSIONS_FILE`) selects an alternate file, exactly the per-process isolation you may already be using.
- **`settings.json`** — same file, with added fields `use_public_api`, `public_api_supported`, `tunnel_api_supported`, plus the update-check throttle (`last_check`, `latest_version`, `enable_update_check`).
- **`history/`** — the same per-session JSONL event logs used by `colab log`. `golab` appends compatible events (`session_created`, `execution`, `file_operation`, `session_terminated`, automation and stdin events).
- **Token store** — the keyring entry uses service `google-colab-cli`, user `oauth_token`; a Python `token.json` in the config directory is imported once. New installs persist only the refresh token plus metadata and mint access tokens on demand.
- **Write safety** — writes are locked, fsynced and atomically renamed, with a `.bak` of the last good file. A parse failure aborts with `Corrupt state file` instead of silently resetting everything to `{}` (upstream defect D-1).

## Keeping existing sessions

1. Install `golab` without removing the Python package.
2. Run `golab sessions`. If your account can still list the assignments, they appear immediately. Proxy tokens near expiry are refreshed from a GET of `/tun/m/*`.
3. Use `-s <name>` exactly as before. Because the name and endpoint are read from the shared `sessions.json`, no re-authentication is needed as long as your OAuth token is still valid in the keyring or the legacy `token.json` is present.
4. If you changed accounts, run `golab logout` first. It clears the local token, blanks cached proxy tokens in `sessions.json` and resets the capability cache. It does **not** revoke the token at Google and does **not** stop any VM; under `--auth=adc` it prints `gcloud auth application-default revoke` guidance instead.

If a session was already gone on the server, `golab` prunes the local record and reports it; that is not a migration problem.

## What changes operationally

| Area | Python `colab` | `golab` |
|---|---|---|
| Binary / invocation | `colab` (Python entry point) | `golab` (single static binary) |
| Progress & errors | `[colab] …` lines on stdout | All chatter on **stderr**; stdout is data only |
| Install / update | `pip install google-colab-cli`, `colab update` via pip | `go install …@latest`, release archives, or signed `golab update --install` |
| Self-update trust | unverified PyPI path | checksums + cosign keyless signature + signed SBOM; downgrade and identity checks |
| SSH | needs a local OpenSSH client and key | embedded SSH client, no system `ssh`/`ssh-keygen`; `--proxy-mode` for external clients |
| SSH cleanup | stale sshd children can block connections | `ssh --force` kills them through the kernel before dialling |
| Default auth | docs say `adc`, code uses `oauth2` | **`oauth2`** to match the code; `COLAB_AUTH=adc` or `--auth adc` to opt in |
| Logging | always-on DEBUG in some builds | off by default; `COLAB_DEBUG=1` or `--logtostderr` |
| HTTP requests | unbounded in upstream | explicit per-request timeouts and bounded retries |
| State on parse error | silently wiped | aborts loudly; `.bak` retained |
| File transfer | full-file base64 | streamed chunks; `-r` for directories; atomic downloads |
| Structured output | none | `--output text\|json\|ndjson` with the `golab/1` schema, `--explain`, `--yes` |
| Platforms | Linux/macOS only upstream | Linux, macOS, Windows, Termux |

## `--public-api` vs `--no-public-api`

Upstream is mid-migration to the Colab Public API (`colaboratory.googleapis.com/v1beta/runtimes`) while keeping the legacy tunnel API (`/tun/m/*`). `golab` supports both control-plane paths and implements the full Public API create/poll/delete lifecycle ahead of upstream HEAD, which currently only lists runtimes through it.

- The CLI default is the **tunnel API**, unless `COLAB_CLI_PUBLIC_API=1` is set or `--public-api` is passed. `--no-public-api` forces the tunnel path for one command — useful when you exported `COLAB_CLI_PUBLIC_API=1` globally but a particular account or network works better on the tunnel API, or while the probe results are inconclusive.
- `golab test-public-api` (hidden) records `public_api_supported` / `tunnel_api_supported` in `settings.json`. `golab doctor --section api` reports reachability without allocating.
- Live behaviour of both paths is **unverified** in this release; treat a probe result as local evidence, not a guarantee. See [REMEDIATION.md](REMEDIATION.md) §Human-required items.

## Upstream drift and what we fix

The design docs were written against `v0.8.0+2`; upstream has since moved. The [compatibility declaration](COMPAT.md) and [audit index](AUDIT-INDEX.md) catalogue this in detail. Highlights:

- **#269 — `colab auth` hangs.** Upstream's `execute_interactive` discards the `stdin_hook` return value, so the kernel's `input()` never receives an `input_reply`, and it prints a raw message dict instead of `content.prompt`. `golab`'s runtime stdin hook explicitly sends `input_reply` with the prompt text from `content.prompt`. This is a *code-level* fix; it still needs a live human run before we claim it publicly.
- **#270 — proxy 411.** Upstream's body-less POSTs get HTTP 411 behind some forward proxies. Go's `net/http` emits `Content-Length: 0` for POST, but whether `golab` hits the same failure in a proxied environment is unverified and human-gated. If you see it, record the exact proxy setup and report it.
- **#273 / high-memory.** Paid accounts have reported 503 / timeouts for G4/A100/L4 and `--high-mem`. `golab` validates hardware before allocation, retries and reconciles assignments, and maps `412`/quota errors to actionable messages instead of raw failures. For L4/V5E1/V6E1, `--high-mem` is ignored with a notice because only one shape exists.
- **#266 / #267 / `ssh --force`.** `logout`, recursive transfers and the stale-sshd kill are implemented rather than left as upstream PRs.
- **Public API lifecycle.** Full LRO create/poll/delete and `runtime_name` persistence are implemented ahead of upstream's current list-only support.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `No active sessions found. Create one with 'golab new'.` | No local sessions, or they were pruned as gone. Run `golab sessions`, then `golab new`. |
| `No active sessions found on server.` | Server has no assignments; check your account's compute units with `golab usage`. |
| A session appears as `[?]` | Server-side assignment with no local record. Identify it via `golab sessions` and clean it up explicitly; do not leave orphans billing you. |
| `Allocation refused (precondition failed)` / HTTP 412 | Too many active sessions or a temporary capacity limit. `golab stop` a session, wait, or try another accelerator. |
| Backend rejected accelerator / quota error | Your account lacks entitlement for that GPU/TPU. Try `--gpu T4` or omit the flag for CPU. |
| `--high-mem ignored` | Expected for L4, V5E1 and V6E1 — those offer a single machine shape. |
| HTTP 429 on `golab ssh` | Too many SSH sessions on the runtime. Run `golab ssh --force -s NAME`; if that fails, stop stale sessions. |
| HTTP 411 behind a corporate proxy | Possibly upstream issue #270; our behaviour is unverified. Report your proxy setup; do not disable TLS verification. |
| `Corrupt state file` | Do not delete the file blindly. Inspect `<config dir>/sessions.json` and its `.bak`, fix the JSON, or move the file aside and re-create sessions. |
| `colab` still runs the Python tool | Expected — both can be installed. Use `golab` explicitly; `which colab && colab version` identifies the Python one. |
| `golab auth` appears to hang | Report it with the exact command and logs. This is the upstream #269 area we believe we fixed, but live proof is still pending. |
| Windows leaves `golab.exe.old` | `golab` removes it on startup; if permissions block cleanup it is non-fatal and never deletes the running executable. |

For bug reports and security issues, see [CONTRIBUTING.md](../CONTRIBUTING.md) and [SECURITY.md](../SECURITY.md). More background is in [FAQ.md](FAQ.md) and [COMPAT.md](COMPAT.md).
