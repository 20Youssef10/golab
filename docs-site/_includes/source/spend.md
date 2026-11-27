<!-- Generated from docs/USAGE.md §Spend guard for, golab reap, Upstream-compatible utilities by docs-site/tools/sync-sources.py. Do not edit. -->

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

### Spend guard for `run`

```bash
golab run train.py --max-cu 10 --enforce
```

`--max-cu FLOAT` is a **minimum reserve** sampled from `GET /tun/m/ccu-info`. That endpoint is **account-level**, so the sampled balance and any delta belong to the account, not to this one VM; no price is inferred. Without `--enforce`, a breach is reported; with it, the run is interrupted and the session stopped. `--max-cu 0` disables the monitor. The guard is **foreground-only**: `run --detach` returns after submitting the remote child and does not keep the in-run monitor alive, so supplying `--max-cu` or `--enforce` together with `--detach` is a usage error (exit 2) before any allocation. Wiring a remote CU sampler is deferred; see [ADR-001](https://github.com/20Youssef10/golab/blob/main/docs/adr-001-detached-runs.md).

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
| `golab version` | `golab 0.2.0 (compatible with google-colab-cli 0.8.x+HEAD)`. |
| `golab update [--install] [--verify-only PATH]` | Check GitHub Releases; with `--install`, verify checksums, cosign signature and signed SBOM before replacing the binary. `--verify-only PATH` verifies a **local** artifact (checksum + cosign + SBOM; `--checksums`, `--signature`, `--sbom` override the sibling defaults) without installing anything. |
| `golab logout` | Clear the local OAuth token, cached proxy tokens and capability probes so you can switch accounts. Under `--auth=adc`, print `gcloud auth application-default revoke` guidance instead. |
| `golab test-public-api` | *(hidden)* Probe Public and Tunnel API reachability and cache the result. |
