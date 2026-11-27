# golab — the Colab CLI, in Go

`golab` is an **unofficial** Go implementation of Google's [`google-colab-cli`](https://github.com/googlecolab/google-colab-cli). It gives you a single static binary for creating Colab runtimes, executing Python on them, moving files, opening SSH sessions and scripting the whole workflow — from Linux, macOS, Windows or Termux. It targets upstream **HEAD `0abf82d`** (the `v0.8.0` release line plus five commits) and keeps the same on-disk state format, so you can move from the Python `colab` command without re-authenticating or losing sessions.

This is a community port, not a Google product. “Google” and “Colab” are trademarks of Google LLC.

## Why Go?

- **One static binary.** `CGO_ENABLED=0`, no Python runtime, no virtualenv, no system `ssh` or `ssh-keygen` to install.
- **Real cross-platform support.** First-class Windows (console handling, in-place self-update) and Termux (DNS/CA shim) alongside Linux and macOS — upstream officially supports only Linux and macOS.
- **Signed updates, not PyPI.** Releases are checked against published checksums, a cosign keyless signature and a signed SBOM before installation.
- **Safe to pipe.** Program output goes to stdout; every `[colab] …` status line goes to stderr. `golab exec < code.py`, `golab ssh -- CMD` and `--output json|ndjson` produce clean, byte-exact streams.
- **Loud about failures.** Atomic state writes, corrupt-state aborts, explicit HTTP timeouts and typed errors instead of string matching.

## Status (v0.2.0)

`v0.2.0` is published (release run `45489842618`: 13 archives with signatures and SBOMs). The full CI matrix is green, `golangci-lint` reports 0 issues and `govulncheck` is clean. The tree has 664 test/fuzz functions in 103 `_test.go` files across 25 packages (23 with tests), at 75.8% total statement coverage — above the CI floor of 70%, short of the 80% target (notably for `state`/`sshx`). A real signed self-update from `v0.1.9` → `v0.2.0` was verified and installed an executable binary.

**Live proof, with its limits.** A billable CPU-VM live e2e has been run (recorded in [CHANGELOG.md](CHANGELOG.md) under v0.2.0): `new`, `status`, `ls`, `exec` (text/JSON/secrets), `run` (script and notebook), `ssh`, piped `repl`, upload/download, `install`, `restart-kernel`, `log`, `stop --verify` and `resume` error paths; the final `golab sessions` was empty. That is a real end-to-end pass on one machine class — it is not certification of every platform, feature or failure mode.

**Still human-only / not proven:** OAuth re-consent, Drive browser approval, interactive-TTY `repl`/`ssh`, real Windows and Termux devices, and induced WebSocket-drop recovery. These are tracked in [docs/REMEDIATION.md](docs/REMEDIATION.md) and the layered ledger in [docs/CONFORMANCE.md](docs/CONFORMANCE.md); nothing here should be read as blanket live certification.

## Install

Pick one:

```bash
# 1. Go toolchain (canonical module path)
go install github.com/20Youssef10/golab/cmd/golab@latest

# 2. Prebuilt binary from GitHub Releases:
#    download the .tar.gz for your OS/arch, its checksums, the .sigstore.json
#    bundle and the *.sbom.sigstore.json attestation for your platform.
#    `golab update --install` verifies all of these for you.

# 3. Build from source (produces ./golab, CGO_ENABLED=0)
git clone https://github.com/20Youssef10/golab.git
cd golab
make build
```

The binary is named **`golab`** in all three cases. Put it on your `PATH` (on Windows, the installer/release archive contains `golab.exe`). To verify a release manually, see [docs/FAQ.md](docs/FAQ.md#how-do-i-verify-a-release).

## Quick start

```bash
golab new -s demo --gpu t4                      # allocate a named T4 runtime
echo 'print(2 + 2)' | golab exec -s demo        # run code from stdin
golab run train.py --gpu t4                     # fresh VM, run script, release it
golab sessions                                  # list what is still allocated
golab stop -s demo                              # shut it down
```

With only one active session you can omit `-s`; `golab` selects it automatically and tells you on stderr which one it picked.

## Commands at a glance

| Area | Commands |
|---|---|
| Sessions | `new` (`--ttl`, `--wait-capacity`), `sessions`, `status` (`--watch`), `stop` (`--all`, `--verify`), `restart-kernel`, `attach`, `reap` (`--dry-run`), `use`, `wait` |
| Execution | `exec` (`-f`, `--env`, `--timeout`, `--secret`, `--env-file`, `--json`, `--output-image`), `repl`, `run` (script or `.ipynb`; `--detach --ttl`, `--max-cu`, `--lockfile`, `--bundle`, `-p`), `resume`, `jobs`, `logs ID [-f]` |
| VM inspection | `ps` (alias `top`), `cat`, `tail [-f]`, `grep`, `du`, `find`, `fetch [--sha256]` |
| Files | `ls`, `rm`, `upload` / `download` (`-r`), `edit`, `sync [--watch]` |
| Reproducibility | `env freeze [-s -o]`, `bundle record`, `bundle verify [--env]`, `run --bundle --out` |
| Config & state | `init [--force]`, `config get\|set\|unset\|list\|path`, `use NAME\|--clear`, `state migrate [--dry-run\|--apply]` |
| Remote access | `ssh` (`-- CMD`, `--proxy-mode`, `--rm`, `--force`, `--colab-internal-mode` [hidden/internal]) |
| Automation | `install`, `drivemount`, `auth` (hidden) |
| Account & info | `usage`, `cost [--since --json]` (CU only), `url`, `log [-f]`, `pay`, `whoami` (hidden), `version`, `help`, `completion` |
| Maintenance | `update [--verify-only PATH]`, `doctor [--fix --apply]`, `compat report [--json]`, `trace -- <cmd>`, `logout`, `readme`, `skill`, `test-public-api` (hidden) |
| Go-only extras | `audit`, `budget`, `agentout` |

**Spend safety / VM inspection.** `cost` reports observed compute units (CU only, account-level, no prices) and `run --max-cu N --enforce` guards a run against the same account-level balance; `reap` unassigns orphaned server runtimes. `new --ttl D` records a local expiry checked on every invocation (no daemon). The inspection commands poll the VM server-side: `tail -f` is not byte-perfect across log rotation. See **[docs/USAGE.md](docs/USAGE.md)** for flags and caveats.

Full flags, examples and workflows are in **[docs/USAGE.md](docs/USAGE.md)**.

## Global flags and environment

These flags work on every command (Cobra also provides `-h`/`--help`).

| Flag | Default | Environment | Meaning |
|---|---|---|---|
| `-c`, `--client-oauth-config` | `~/.colab-cli-oauth-config.json` | — | Custom OAuth client JSON; a missing file falls back to the bundled client. |
| `--config` | `<config dir>/sessions.json` | `COLAB_SESSIONS_FILE` | Alternate session-state file. |
| `--auth` | `oauth2` | `COLAB_AUTH` | `oauth2` or `adc` (case-insensitive). |
| `--scopes` | `full` | `COLAB_SCOPES` | `full` (default) or `minimal` OAuth scope set; `minimal` cannot mount Drive, and changing mode forces re-consent. |
| `--public-api` / `--no-public-api` | tunnel API | `COLAB_CLI_PUBLIC_API` | Choose the control-plane path. |
| `--output` | `text` | `COLAB_OUTPUT` | `text`, `json` or `ndjson`; diagnostics stay on stderr. |
| `--explain` | off | — | Print a redacted dry-run plan for billable commands and exit. |
| `--yes` | off | `COLAB_ASSUME_YES` | Assume yes for non-destructive confirmations. Never bypasses a budget guard. |
| `--logtostderr` | off | `COLAB_DEBUG=1` | Verbose logs on stderr (debug logging is off by default). |

Additional environment overrides: `COLAB_CONFIG_DIR`, `COLAB_CLI_REQUEST_TIMEOUT`, `COLAB_CLI_ASSIGN_RETRIES`, `COLAB_CLI_RETRY_BACKOFF`, `GOLAB_BUDGET_FILE`, `GOLAB_SESSION` (default session), `GOLAB_TRACE` / `GOLAB_TRACE_UNSAFE_BODIES` (wire trace), `GOLAB_LIVE_PROBES`, `NO_COLOR`, `VISUAL`/`EDITOR`. The complete table is in [docs/USAGE.md](docs/USAGE.md#9-environment-variables).

## Configuration paths

`golab` uses the same `colab-cli` directory name as the Python CLI, so an existing installation is picked up automatically.

| Platform | Base config directory |
|---|---|
| Linux / macOS / Termux | `~/.config/colab-cli/` |
| Windows | `%APPDATA%\colab-cli\` |

Inside it: `sessions.json` (active runtimes and proxy tokens), `settings.json` (update throttle and API probes), `history/` (per-session JSONL event logs). Override the base with `COLAB_CONFIG_DIR`, or just the session file with `--config` / `COLAB_SESSIONS_FILE`. Temporary files prefer `$TMPDIR` (Termux: `$PREFIX/tmp`); caches live in `~/.cache/colab-cli` or `$XDG_CACHE_HOME/colab-cli`.

On Termux, install the static `linux/arm64` binary with [docs/INSTALL-TERMUX.md](docs/INSTALL-TERMUX.md) (checksum + cosign verification, keyring fallback and runtime notes).

## Compatibility with the Python CLI

- **Same state schema.** `sessions.json`, `settings.json` and the per-session history are read and written in the Python CLI's format, with a few added fields for the Public API. Existing sessions survive the switch; see [docs/MIGRATION.md](docs/MIGRATION.md).
- **Chatter moved to stderr.** All `[colab] …` diagnostics go to stderr so stdout is pipe-safe.
- **SSH embedded.** No external OpenSSH binary or key generation; `--proxy-mode` remains as an escape hatch for external clients.
- **Atomic state.** Locked temp-file + fsync + rename; a corrupt file aborts rather than being silently reset.
- **Signed self-update.** `golab update --install` replaces the unverified PyPI/pip path.
- **Happy-path parity plus fixes.** Upstream defects D-1…D-11 are fixed; upstream HEAD additions (`stop --all`, `--public-api`, `ssh --force`, `logout`, recursive transfers) are included. Full detail in [docs/COMPAT.md](docs/COMPAT.md).

## Documentation

- **Published documentation site:** <https://20youssef10.github.io/golab/> — wiki-style navigation over these docs (searchable sidebar, diagrams, theme toggle). Canonical prose stays in `docs/`; the site's excerpts are generated from it.
- [User guide — flags, workflows, exit codes](docs/USAGE.md)
- [Migrating from Python `colab`](docs/MIGRATION.md)
- [Frequently asked questions](docs/FAQ.md)
- [Compatibility declaration](docs/COMPAT.md)
- [Remediation & human-only verification status](docs/REMEDIATION.md)
- [Conformance status](docs/CONFORMANCE.md)
- [Changelog](CHANGELOG.md)
- Architecture & analysis: [docs/01](docs/01-python-cli-analysis.md) · [docs/02](docs/02-protocol-spec.md) · [docs/03](docs/03-go-architecture.md) · [docs/04](docs/04-platform-matrix.md) · [docs/05](docs/05-command-parity.md) · [docs/06](docs/06-roadmap.md) · [docs/07](docs/07-testing-and-conformance.md) · [docs/08](docs/08-risks-decisions-security.md) · [docs/09](docs/09-implementation-contracts.md)
- Audits: [index](docs/AUDIT-INDEX.md) · [00 live upstream](docs/AUDIT-00-live-upstream.md) · [01 parity](docs/AUDIT-01-parity.md) · [02 protocol/security](docs/AUDIT-02-protocol-security.md) · [03 platform/testing](docs/AUDIT-03-platform-testing.md) · [04 surpass](docs/AUDIT-04-surpass.md)
- [Dependencies](DEPENDENCIES.md) · [Contributing](CONTRIBUTING.md) · [Security policy](SECURITY.md)

## Contributing, license, trademark

Contributions are welcome; start with [CONTRIBUTING.md](CONTRIBUTING.md) and please do not report security issues in public — use [SECURITY.md](SECURITY.md). This project is licensed under the Apache License, Version 2.0 (the same license as upstream); portions are derived from `google-colab-cli`, © Google LLC, with modifications. `golab` is unofficial and is not endorsed by or affiliated with Google LLC.
