# 05 · Command Parity Matrix

> **Implementation note (2026-11-23):** all commands listed below are implemented
> in `internal/cli` and exercised against the fake control plane / kernel / sshd.
> Upstream HEAD additions (`stop --all`, `--public-api/--no-public-api`,
> `test-public-api`) are included, as is `ssh --force` (upstream `cda02bc`) and
> the community PR features `logout` and `upload/download -r`. New Go-only
> commands (`doctor`, `audit`, `budget`, `bundle`, `sync`, `agentout`) are
> additive. Live e2e against a real billable VM has **not** been run and requires
> a human; see `docs/REMEDIATION.md` for the v0.1.0 status.

> Flags and defaults below were extracted programmatically (AST) from `src/colab_cli/commands/*.py` at upstream `v0.8.0+2`. Exact message strings are **not** reproduced here: harvest them from `tests/` into golden files (doc 07 §Golden outputs) so they cannot drift.
> **Priority:** P0 = MVP (Phase 1–2) · P1 = parity-complete (Phase 3) · P2 = nice-to-have.
> **Δ** = deliberate deviation from Python (rationale in doc 08 DEC-xx).

## Global flags (all commands)
| Flag | Default | Go notes |
|---|---|---|
| `-c`, `--client-oauth-config PATH` | `~/.colab-cli-oauth-config.json` | missing file ⇒ bundled client |
| `--config PATH` | `<cfgdir>/sessions.json` | per-process state isolation |
| `--logtostderr` | off | Δ also `COLAB_DEBUG=1`; logging off by default |
| `--auth {oauth2,adc}` | `oauth2` | case-insensitive; Δ add env `COLAB_AUTH` |
| `-h`, `--help` | – | alphabetical command list |

Auto-update background check runs for every command **except** `update version log pay help url whoami readme README skill SKILL` and piped `ssh` (stdout not a TTY). Δ In Go it must **never block or write to stdout**; run it concurrent with the command, print the notice to stderr at exit, throttled to once per 24 h.

---

## Session management
| Command | Flags / args | Behaviour to match | Go notes | Pri |
|---|---|---|---|---|
| `new` | `-s NAME`, `--gpu {t4,l4,g4,a100,h100}`, `--tpu {v5e1,v6e1}`, `--high-mem` | validate accelerator **before** any network call; generate `nbh` UUID; assign (C1→C2) with retry/reconcile; persist session (token, url, endpoint, expiry, variant, accelerator, shape); log `session_created`; friendly 412/400 messages; **`--tpu` silently wins if both `--tpu` and `--gpu` are given** (no error upstream); default name = first 6 hex chars of a random UUID; `--high-mem` for L4/V5E1/V6E1 prints a notice to stderr and is ignored | accelerator tables are case-insensitive; Δ consider erroring on `--gpu`+`--tpu` (DEC-08) | P0 |
| `sessions` | – | reconcile local state with `GET /assignments`; prune stale; list name, hardware label, status; mark server-only assignments as orphans (`[?]`) | typed table writer; stable column order | P0 |
| `status` | `-s` | print state, hardware, shape, endpoint-derived info, `running`/`last_execution`, remaining token life | resolve session rule: explicit `-s`, else the unique session, else error listing names | P0 |
| `stop` | `-s` | shut down kernel (ignore 404) → unassign (GET+POST) → remove local → log `session_terminated` | tolerate "already gone"; always remove local state | P0 |
| `restart-kernel` | `-s` | `POST …/restart` | | P0 |

**Session resolution rule (shared):** `-s` wins; else if exactly one session exists use it and print `Using unique session '<n>'`; zero ⇒ error "No active sessions… `colab new`"; many ⇒ error listing names. Missing/expired-token sessions refresh via C3; gone ⇒ prune + `Session '<n>' not found.` exit 1.

## Execution
| Command | Flags / args | Behaviour to match | Go notes | Pri |
|---|---|---|---|---|
| `exec` | `-s`, `-f FILE`, `--output-image PATH`, `--timeout S` (30; 0=∞), `--env K=V` (repeat) | read code from `-f` or piped stdin (TTY with no input ⇒ error exit 1); empty code ⇒ exit 0; run `os.makedirs('/content'); os.chdir('/content')` first; `.ipynb` input ⇒ execute **code cells in order**, print `Executing cell i/N - <@title or cell id>…`, save outputs back into cells, write `<name>_output.ipynb`; `--env` becomes a Python prelude using `repr()`; stream outputs live; update `running`, `last_execution`; log `execution` events; on 404/401 prune session | `--env` key validation (`^[A-Za-z_][A-Za-z0-9_]*$`, else exit 2); `# @title` regex for cell labels; image handling = Kitty on TTY, else file | P0 |
| `repl` | `-s`, `--output-image` | interactive Python REPL with highlighting and multi-line; **piped stdin supported, exits on EOF** | behind `repl.Frontend`; P1 for the rich TTY mode, **P0 for piped mode** | P0/P1 |
| `run` | `SCRIPT [ARGS…]`, `-s`, `--gpu`, `--tpu`, `--high-mem`, `--keep`, `--timeout` (30), `--env` | `.ipynb` input parses code cells before allocation, executes them on one kernel with shared state and saves `<name>_output.ipynb` (0600); extra args warn instead of becoming `sys.argv`; **validate script exists before allocating**; create VM (or reuse `-s`); upload script (and forward args via `repr()`-quoted `sys.argv`); run with CPython-like exit semantics (`sys.exit(None/0)→0`, `int→N`, `str→1` + message to stderr, exception→1 + traceback); tear down unless `--keep`; teardown on SIGINT/SIGTERM/SIGHUP, idempotent; chatter to stderr; shebang use supported | interspersed flags disabled after SCRIPT; `--env` before/after SCRIPT both accepted | P0 |

## Files (Contents API; remote paths relative to VM root)
| Command | Args | Behaviour | Go notes | Pri |
|---|---|---|---|---|
| `ls` | `[PATH]` default `content` | directory listing (name, type); file ⇒ its name | `path` pkg only | P0 |
| `rm` | `PATH` | delete; 404 ⇒ error | | P0 |
| `upload` | `LOCAL REMOTE` | base64 `PUT` (`chunk:1`); log `file_operation` | Δ **stream in chunks** (D-4); progress bar on TTY | P0 |
| `download` | `REMOTE LOCAL` | `GET ?content=1`, decode by `format` (`base64`/`json`/text); write file | Δ stream to disk, atomic temp+rename | P0 |
| `edit` | `REMOTE` | download to a temp file (keeps the remote file's extension) → open the editor (`click.edit`: `$VISUAL`/`$EDITOR`, else platform default) → compare SHA-256 before/after → upload only if changed; prints `Edited and uploaded` / `No changes made` | temp file in a private dir; Windows `notepad`, Termux `nano`/`vim` fallbacks; Δ **only a 404 means "new file"**; any other download error aborts (upstream swallows *every* error and would overwrite the remote file with the blank buffer, D-11) | P1 |

## Automation (executed *inside* the VM kernel)
| Command | Args | Behaviour | Go notes | Pri |
|---|---|---|---|---|
| `install` | `PKGS…` or `-r REQUIREMENTS` | build and run a `pip install` snippet in the kernel; stream output | Δ **never string-splice** (D-3): embed packages as a JSON list literal decoded remotely | P0 |
| `auth` *(hidden)* | `-s` | run `google.colab.auth.authenticate_user()` with `USE_AUTH_EPHEM=0`; interactive stdin; 10-minute idle timeout | needs stdin+streaming together (ADR-009.5) | P1 |
| `drivemount` | `[PATH]` default `/content/drive`, `-s` | run `drive.mount(PATH)`; intercept `colab_request dfs_ephemeral` (doc 02 §3.7); print consent URL, open browser, wait for Enter | `/dev/tty`/`CONIN$` fallback to stdin; 10-minute idle timeout | P1 |

## Remote access
| Command | Flags / args | Behaviour | Go notes | Pri |
|---|---|---|---|---|
| `ssh` | `-s`, `--proxy-mode`, `-i/--identity PATH`, `--gpu`, `--tpu`, `--high-mem`, `--rm`, `[-- CMD…]`, hidden `--colab-internal-mode {interactive,proxy,cmd}` | key discovery (`--identity` → default `~/.ssh/id_ed25519`/`ecdsa`; **RSA rejected locally with guidance**); auto-create a session if none (with `--gpu/--tpu/--high-mem`); open WS (doc 02 §4); interactive shell lands in `/content`; `-- CMD` runs non-interactively and returns remote status; `--rm` stops the runtime on exit **including on SIGHUP**; exact 400/401/403/404/429/502 messages; piped stdout ⇒ no update check | Δ embedded client by default (ADR-005); `--proxy-mode` = stdio bridge for external clients; cmd quoting: **1 arg = shell line, N args = POSIX-quoted argv** | P1 |

## Utility
| Command | Flags | Behaviour | Go notes | Pri |
|---|---|---|---|---|
| `usage` | – | `GET /ccu-info` → balance, hourly rate, active assignments | format identical; no auto-update gate | P0 |
| `url` | `-s`, `--host`, `--open` | doc 02 §5 URL; print; optionally open browser | | P0 |
| `log` | `-s`, `-n/--lines N`, `-t/--type TYPE`, `-o/--output FILE` | view session history; `-o` suffix selects `.ipynb`, `.md`, `.txt`, `.jsonl` exporter | exporters in `history`; notebook export uses `nb` | P1 |
| `pay` | – | open the Colab sign-up page (compute units) | | P2 |
| `version` | – | print version | Δ `colab-go X (compatible with google-colab-cli 0.8.x)` | P0 |
| `update` | `--install` | check latest; with `--install` upgrade | Δ GitHub Releases + signature verification; works on all OSes incl. Windows running-exe replace | P1 |
| `whoami` *(hidden)* | – | print credentials identity, scopes, expiry (debug) | never prints tokens | P1 |
| `readme` / `README` / `skill` / `SKILL` *(hidden)* | – | print bundled markdown | `go:embed`; **write our own SKILL.md** (do not copy upstream's drifted one) | P2 |
| `help` | `[COMMAND]` | help; unknown ⇒ stderr + exit 2 | cobra's help is acceptable if wording tests are golden-pinned | P0 |
| `completion` *(new)* | `bash\|zsh\|fish\|powershell` | shell completions | Δ new | P1 |

**Not ported:** `console` (removed upstream).

---

## Deliberate deviations (Δ) summary
| ID | Deviation | Why |
|---|---|---|
| Δ1 | `[colab] …` chatter on **stderr** for every command | clean stdout for piping (DEC-07) |
| Δ2 | Embedded SSH client; `--proxy-mode` retained | no OpenSSH dependency (ADR-005) |
| Δ3 | Atomic state writes; abort on corrupt state | fixes D-1 |
| Δ4 | Session-name validation | fixes D-2 |
| Δ5 | Chunked upload / streamed download | fixes D-4 |
| Δ6 | Logging off by default; rotating file | fixes D-6 |
| Δ7 | Always-set HTTP timeouts | fixes D-5 |
| Δ8 | SIGINT interrupts the remote kernel first | `run` should behave like `python script.py` (DEC-05) |
| Δ9 | Token store tiers + one-time import of Python's keyring entry | ADR-006 |
| Δ10 | Signed self-update | fixes D-9 |
| Δ12 | `edit` aborts on non-404 download errors | fixes D-11 |
| Δ11 | `COLAB_AUTH`, `COLAB_CONFIG_DIR`, `COLAB_DEBUG` env vars; `completion` command | ergonomics |
