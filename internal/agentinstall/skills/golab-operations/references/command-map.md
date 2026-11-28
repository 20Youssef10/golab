# golab command map (v0.2.0)

Source of truth: `golab --help` and `<command> --help` for the v0.2.0 binary (54 top-level commands), cross-checked against `docs/USAGE.md`. Re-run `golab version` and `<command> --help` before relying on a flag in a different build. Global flags are listed at the end; command-local flags below exclude `-h/--help`.

## Session lifecycle

| Command | Key flags / arguments | Notes |
|---|---|---|
| `new` | `-s/--session`, `--gpu {T4,L4,G4,H100,A100}`, `--tpu {V5E1,V6E1}`, `--high-mem`, `--ttl DURATION`, `--ttl-action warn\|stop`, `--wait-capacity DURATION` | Billable allocation. `--explain` is supported. TPU wins if both accelerator kinds are set. |
| `sessions` | — | Reconciles local records with the server; `[?]` rows are server-side orphans. |
| `status` | `-s`, `--watch`, `--interval DURATION` | Shows hardware/shape/variant and IDLE or BUSY. |
| `stop` | `-s`, `--all`, `--verify` | `--all` needs a TTY or `--yes`/`COLAB_ASSUME_YES=1`; `--verify` adds a GET. |
| `attach` | `--runtime NAME`, `-s` | Public API only; attaches an existing browser-created runtime, no allocation. |
| `reap` | `--dry-run`, `--older-than DURATION`, `--json` | Unassigns orphan runtimes (billable); preview first. |
| `resume` | `-s`, `--timeout SECONDS` | Recovers an interrupted execution without resubmitting code. |
| `restart-kernel` | `-s` | Restarts the kernel; does not change the assignment. |
| `use` | `NAME`, `--clear` | Sets the default session in settings.json. |
| `wait` | `-s`, `--for kernel\|idle\|ready`, `--timeout SECONDS` | Bounded polling; allocates nothing. |
| `config` | `get`, `set`, `unset`, `list`, `path` | Reads/writes local settings.json; printed values are redacted. |
| `init` | `--force` | Scaffolds `golab.yaml` and `.golabignore`. |
| `state` | `migrate [--apply] [--dry-run] [--json]` | Additive `schema_version` stamp; preserves session records. |

## Execution

| Command | Key flags / arguments | Notes |
|---|---|---|
| `exec` | `-s`, `-f/--file`, `--env KEY=VALUE`, `--env-file PATH`, `--secret NAME`, `--json`, `--timeout SECONDS`, `--output-image PATH` | Runs in an existing session; code from file or stdin. `--explain` is not listed for this command. |
| `run` | `SCRIPT`, `-s`, `--gpu`, `--tpu`, `--high-mem`, `--env`, `--env-file`, `--secret`, `-p/--param`, `--params-file`, `--timeout`, `--json`, `--output-image`, `--keep`, `--max-cu FLOAT`, `--enforce`, `--detach`, `--ttl DURATION`, `--lockfile PATH`, `--bundle`, `--out PATH` | Allocates, uploads, runs, then releases unless `--keep`. `--explain` is supported. Flags after `SCRIPT` belong to the script except `--env`. |
| `repl` | `-s`, `--output-image PATH` | Rich frontend only on a TTY; piped mode exits on EOF. |
| `ssh` | `-s`, `-i/--identity PATH`, `--gpu`, `--tpu`, `--high-mem`, `--force`, `--proxy-mode`, `--rm`, `--accept-host-key SHA256:…`, `-- CMD...` | Interactive or `-- CMD` (no allocation for `-- CMD`); auto-create consults the budget guard. `--accept-host-key` pins the runtime host key by fingerprint (prefix optional). `--explain` is supported. |
| `install` | `-s`, `PKGS...`, `-r/--requirement PATH` | Installs packages on the VM; package names are sent as a JSON list. |
| `auth` | `-s` *(hidden command)* | Runs Colab's interactive in-VM auth; human-only. |

## Files and VM inspection

| Command | Key flags / arguments | Notes |
|---|---|---|
| `upload` / `download` | `LOCAL REMOTE` / `REMOTE LOCAL`, `-r/--recursive`, `-s` | Remote paths are Linux paths; directories need `-r`. |
| `edit` | `REMOTE`, `-s` | `$VISUAL`/`$EDITOR` round-trip with a SHA-256 guard. |
| `ls` | `[PATH]`, `-s` | Defaults to `content`; directories get a trailing `/`. |
| `cat` | `REMOTE`, `-s` | Binary-safe. |
| `tail` | `REMOTE`, `-n/--lines`, `-f/--follow`, `--interval DURATION`, `--timeout SECONDS`, `-s` | Server-side polling; not log-rotation-safe. |
| `grep` | `PATTERN PATH`, `-i/--ignore-case`, `--glob`, `--max-matches`, `-s` | Searches remote files. |
| `du` | `PATH`, `--max-entries`, `-s` | Disk usage per remote entry. |
| `find` | `NAME`, `--max-results`, `-s` | Always searches remote `/content`. |
| `fetch` | `URL [DEST]`, `--sha256 HEX`, `--timeout SECONDS`, `-s` | Downloads into the VM (wget, curl fallback). |
| `ps` / `top` | `--json`, `--timeout SECONDS`, `-s` | Process/GPU/memory/disk snapshot. |
| `env` | `freeze [-o/--out FILE] [-s]` | Deterministic Python/pip/GPU lockfile. |
| `sync` | `LOCAL REMOTE`, `--direction up\|down\|both`, `--apply`, `--delete`, `--diff`, `--include-outputs`, `--watch`, `--interval DURATION`, `--iterations N`, `-s` | Dry-run by default; conflicts fail. |

## Detached jobs and provenance

| Command | Key flags / arguments | Notes |
|---|---|---|
| `run --detach` | `--ttl DURATION` **required**, plus normal `run` flags | Remote child process; no local daemon. `--max-cu/--enforce` monitors foreground runs only. |
| `jobs` | — | Lists detached runs with status, exit code, duration. |
| `logs` | `ID`, `-f/--follow`, `--interval DURATION`, `--max N` | Bounded follow of a detached run's remote log. |
| `bundle` | `record [--out] [--file] [--env KEY=VALUE] [--root] [--shape] [--variant] [--accelerator]`, `verify MANIFEST [--files-root] [--env]` | `record` and metadata-only `verify` allocate nothing; `verify --env` allocates a fresh VM. |

## Spend, diagnostics and utilities

| Command | Key flags / arguments | Notes |
|---|---|---|
| `budget` | `--profile PATH`, `--profile-name NAME`, `--max-balance FLOAT`, `--max-hourly FLOAT`, `--enforce`, `--json` | Advisory CU decision; never allocates. |
| `cost` | `--since DURATION` (default `24h`), `--json` | Observed account-level CU samples, no prices. |
| `usage` | — | CU balance, rate, assignments. |
| `pay` | — | Opens the Colab sign-up page. |
| `doctor` | `--section config\|auth\|api\|update\|wire`, `--json`, `--timeout DURATION`, `--fixtures DIR`, `--live`, `--fix`, `--apply` | GET-only by default; `--live` needs `GOLAB_LIVE_PROBES=1`; `--fix` is dry-run. |
| `audit` | `--json` | Local credential-store/file-mode report; no network. |
| `trace` | `-- <command> [args]`, `-o/--output PATH`, `--unsafe-bodies` | Redacted wire NDJSON; unsafe bodies must be reviewed before sharing. |
| `compat` | `report [--json]`, `refresh [--out FILE] [--ref TAG]` | `report` compares the live tree to a snapshot. `refresh` fetches the official upstream surface into a provenance-tagged snapshot: dry-run by default, `--out -` prints JSON to stdout. No VM allocation. |
| `log` | `-s`, `-n/--lines`, `-t/--type`, `-o/--output FILE`, `-f/--follow` | Session history; `-o` extension chooses exporter. |
| `url` | `-s`, `--host URL`, `--open` | Prints/opens a browser URL for a session. |
| `drivemount` | `[PATH]`, `-s`, `--wait-for-consent` | Drive approval in the browser stays human-only. |
| `update` | `--install`, `--verify-only PATH`, `--checksums PATH`, `--signature PATH`, `--sbom PATH` | Signed release check/install. |
| `logout` | — | Clears local credentials and cached proxy tokens. |
| `agentout` | `[text ...]`, `--format text\|json\|ndjson`, `--type info\|output\|error` | Emits a typed `golab/1` event; no VM. |
| `completion` | `bash\|zsh\|fish\|powershell` | Shell completion script. |
| `readme` / `skill` | — | Print bundled documentation. |
| `version` | — | Prints build version. |
| `help` | `[COMMAND]` | Unknown names exit 2. |
| `whoami` *(hidden)* | — | Prints provider, email, scopes, expiry — never the token. |
| `test-public-api` *(hidden)* | — | Probes and caches plane reachability. |

## Global flags (all commands)

`--auth oauth2|adc`, `--budget-profile NAME`, `-c/--client-oauth-config PATH`, `--config PATH`, `--explain`, `--logtostderr`, `--no-public-api`, `--output text|json|ndjson`, `--public-api`, `--scopes full|minimal`, `--yes`. Boolean flags accept optional `=true|false`. There is no `--debug` flag; use `COLAB_DEBUG=1`.

## Companion surfaces

- MCP server binary: `go install github.com/20Youssef10/golab/cmd/golab-mcp@latest`; flags `--transport stdio|http`, `--addr :8080`, `--golab-bin PATH`. See the sibling `colab-mcp` skill.
- Distribution configs in `packaging/`: `golab.rb` (Homebrew), `golab.json` (Scoop), `20Youssef10.golab*.yaml` (winget), `nfpm.yaml` (`.deb`/`.rpm`/`.apk`). winget manifests are blocked pending a `.zip` repack of the signed release (only `.tar.gz` assets exist). Channel configs are not install proof.
