# Audit 01 · `golab` ↔ `google-colab-cli` parity

> **Date:** 2026-11-23. **Upstream truth:** `/tmp/opencode/google-colab-cli` at HEAD
> `0abf82dadc2b95c02b6ab2c08b1b4d664e616fbb` (= `v0.8.0` + 5 commits, range
> `6045e34..HEAD`). **Port truth:** `/content/drive/MyDrive/golab` (`internal/cli/**`,
> `internal/app/**`, `cmd/golab/**`, plus the protocol packages where a CLI claim
> needed checking). All `file:line` citations are from those two trees.
>
> **Method:** static read of every `commands/*.py`, `cli.py`, `tests/*.py`,
> `CHANGELOG.md`, `docs/*` and the five post-tag commit diffs; line-by-line read of
> every `internal/cli/*.go` file and the relevant `internal/app`, `internal/auth`,
> `internal/colabapi`, `internal/sshx` code. Docs were treated as possibly stale.
>
> **Verification limits (important):** `go` is **not installed** in this environment
> (`go: No such file or directory`) and the Python package is not installed
> (`ModuleNotFoundError: No module named 'colab_cli'`). I could not run either test
> suite, the real `--help`, or any live probe. Statements about runtime rendering
> (help layout, Typer's implicit completion flags) are marked **UNVERIFIED** and a
> verification command is given. Everything else is from source and is reproducible.
>
> **Orchestrator addendum (2026-11-23):** a Go toolchain *is* available at
> `/tmp/opencode/go/bin/go`; the agent did not find it on its `PATH`. I independently
> confirmed findings P0-1 (`--force` absent) and P0-2 (`url --open` no-op), and ran
> `/tmp/opencode/golab --help`: commands are listed alphabetically and `help`
> appears exactly once (cobra's built-in help was hidden during integration).
> The "implicit Typer completion options" rows remain UNVERIFIED because they are
> Python/Typer-specific. Upstream's Python package was not installed, so no live
> comparison of Typer help output was performed.

---

## 1. Executive summary

**Parity score estimate: ~89 %.** This is a judgement, not a measurement. Component
estimates: command surface **93 %** (24 FULL + 2 deliberate-Δ + 4 half-credited
PARTIAL out of 30), documented-flag surface **97 %** (52 FULL + 1 PARTIAL + 1
MISSING out of 54; drops to ~93 % if Typer's 2 implicit completion options are
counted), behavioural/message parity **~80 %** because of the systemic
stdout→stderr deviation, a no-op `url --open`, a Drive-failure HTML leak, and the
missing `ssh --force`.

**Counts**

| Item | Upstream | golab | FULL | PARTIAL | MISSING | DELIBERATE Δ |
|---|---|---|---|---|---|---|
| Commands (incl. hidden) | 30 | 30 (+1 new: `completion`) | 24 | 4 (`help`, `drivemount`, `url`, `ssh`) | 0 | 2 (`version`, `update`) |
| Documented flags | 54 | 53 registered (+1 split flag) | 52 | 1 (`--public-api/--no-public-api` pair) | 1 (`ssh --force`) | 0 |
| Implicit Typer completion options | 2 | 0 (replaced by a command) | 0 | 0 | 2 **UNVERIFIED** | 1 (new `completion` command) |

**The five findings that actually matter**

1. **P0 — `ssh --force` is missing.** Commit `cda02bc` added it to all four SSH paths;
   our CLI has no such flag, yet our own HTTP-429 messages *tell the user to use it*
   (`internal/sshx/errors.go:100-111`). The flag is named **`--force`**, not
   `--forced`: the word "forced" appears only in the commit/PR title; upstream code
   and tests use `--force` (`src/colab_cli/commands/ssh.py:807-817`,
   `tests/test_ssh.py:668-692`).
2. **P0 — `url --open` is a no-op.** `internal/cli/utility.go:47` calls
   `openBrowser`, whose body is `func openBrowser(target string) { _ = target }`
   (`utility.go:259-265`). Upstream calls `webbrowser.open(connect_url)`
   (`utility.py:123-126`, test `test_url_open_flag_launches_browser`).
   `docs/COMPAT.md:21-22` claims this path uses the launcher — that claim is false.
3. **P1 — Drive propagation failures can echo raw HTML.** `reportDriveFailure`
   (`automation.go:281-293`) puts `StatusError.Body` straight into
   `driveFailureMessage`; upstream refuses to echo anything except parsed JSON
   `error`/`message` fields (`automation.py:41-82`, test
   `test_drivefs_hook_reports_400_without_raw_html`). This is a behavioural parity
   bug *and* an untrusted-output-to-terminal issue.
4. **P1 — three upstream env vars are ignored.** `COLAB_CLI_REQUEST_TIMEOUT`,
   `COLAB_CLI_ASSIGN_RETRIES`, `COLAB_CLI_RETRY_BACKOFF` are honoured upstream
   (`client.py:300-304`, `client.py:442-465`) but never read by golab; only constants
   exist (`colabapi/client.go:28-32`). This is a **deliberate** deviation under
   AGENTS.md #5, but it must be decided/documented, not left implicit.
5. **P1 — message/stream parity is systematically different.** Upstream prints most
   `[colab] …` chatter to **stdout**; golab prints it to **stderr** by design (Δ1).
   A handful of exact strings differ beyond the prefix (see §4).

**What is already correct (do not "fix"):** `stop --all`, `--public-api` +
`COLAB_CLI_PUBLIC_API` + hidden `test-public-api`, partial OAuth scopes, the
auto-update suppression list, `--colab-internal-mode`, hidden `auth`/`whoami`/
`README`/`SKILL`, the 429 explanation strings (including `--force` guidance),
`--tpu`-wins-over-`--gpu`, exit codes for all validated-error paths, and the
pipeable `url` output format. Details and evidence below.

---

## 2. Command-by-command matrix

`FULL` = flag surface and core behaviour implemented; message/stream Δ1 is *not*
counted as a gap unless a string differs beyond the `[colab]` prefix/stream.
`DELIBERATE Δ` = intentionally different implementation with the same CLI contract.

| # | Command | Upstream flags / behaviour (evidence) | golab location | Status | Concrete gap | Evidence |
|---|---|---|---|---|---|---|
| 1 | `help [CMD]` | `cli.py:140-161`; unknown ⇒ stderr `No such command 'x'.` exit 2; Rich help, alphabetical | `internal/cli/utility.go:203-222` | PARTIAL | Extra `[colab] ` prefix on unknown-command; `UsageString()` instead of upstream `get_help()` formatting; binary name `golab` | `tests/test_cli.py:421-428`; `cli.py:157` vs `utility.go:216` |
| 2 | `new` | `-s --session --gpu --tpu --high-mem`; friendly 412/400; `--tpu` wins; 6-hex default name; high-mem notice | `internal/cli/session.go:146-181` | FULL | Accelerator error wording differs (`[colab] Invalid value for --gpu:` vs upstream `unsupported value 'X'. Supported: …`) | `session.py:56-67`; `session.go:59-72`; `tests/test_cli.py:115-139` |
| 3 | `sessions` | no flags; unified `[name] endpoint \| Hardware \| Shape \| Variant`; orphans `[?]` | `internal/cli/session.go:183-213` | FULL | — | `session.py:280-304`; `tests/test_cli.py:141-189` |
| 4 | `restart-kernel` | `-s`; restart then stop in `finally` | `internal/cli/session.go:352-380` | FULL | — | `session.py:246-278` |
| 5 | `status` | `-s`; `BUSY (…)`/`IDLE`, `Last Execution` line, unique-session resolution | `internal/cli/session.go:215-270`; `app/app.go:298-313` | FULL | "Create one with 'golab new'" vs upstream `'colab new'` | `common.py:177,197`; `app.go:298,306,313` |
| 6 | `stop` | `-s`, **`--all`** (added `274b77f`) | `internal/cli/session.go:293-350` | FULL | — | `session.py:364-391`; `tests/test_cli.py:331-365`; `session_test.go:314-359` |
| 7 | `exec` | `-s -f --output-image --timeout --env`; `.ipynb` cells; TTY-no-input exit 1; env prelude | `internal/cli/execution.go:98-199` | FULL | Error strings differ beyond Δ1 (e.g. `--env` message quoting `%q` vs Python `!r`) | `execution.py:45-77,145-291`; `tests/test_exec.py:108-207` |
| 8 | `repl` | `-s --output-image`; piped EOF exits 0; rich TTY frontend | `internal/cli/execution.go:271-319`; `internal/repl/**` | FULL | — | `execution.py:294-377`; `tests/test_repl.py:113-152` |
| 9 | `run` | `SCRIPT [ARGS…]`, `-s --gpu --tpu --high-mem --keep --timeout --env`; validate before alloc; CPython exit semantics | `internal/cli/run.go:126-257` | FULL | Docs stale: `docs/05:42` says "reuse `-s`" and "teardown on signals"; upstream always allocates a new VM and has **no** signal handler — golab's `signal.NotifyContext` (`run.go:173`) is a surplus, not a gap | `run.py:202-469,509-519` |
| 10 | `ls [PATH]` | `-s`; dirs sorted dirs-first then name; trailing `/` | `internal/cli/files.go:41-90` | FULL | — | `files.py:26-52`; `tests/test_cli.py:503-524` |
| 11 | `rm PATH` | `-s`; 404 ⇒ exit 1 | `internal/cli/files.go:92-117` | FULL | — | `files.py:55-73` |
| 12 | `upload LOCAL REMOTE` | `-s`; base64 `PUT chunk:1` | `internal/cli/files.go:119-156` | FULL | Δ: streamed chunks (better than upstream; PR #272 is not merged) | `files.py:76-102`; `contents/contents.go:197-201` |
| 13 | `download REMOTE LOCAL` | `-s`; `?content=1`, format decode | `internal/cli/files.go:158-210` | FULL | Δ: atomic temp+rename | `files.py:105-132` |
| 14 | `edit REMOTE` | `-s`; temp file, editor, SHA-256 compare; all download errors swallowed upstream | `internal/cli/files.go:243-315` | FULL | Δ12: golab aborts on non-404 download errors (intentional) | `files.py:135-181`; `tests/test_cli.py:571-625` |
| 15 | `install [PKGS…]` | `-s -r/--requirement`; uv→pip fallback | `internal/cli/automation.go:136-186` | FULL | Δ: JSON-decoded payload instead of string splice | `automation.py:319-364`; `tests/test_automation.py:74-97` |
| 16 | `auth` (hidden) | `-s`; `USE_AUTH_EPHEM=0`; 600 s idle timeout; stdin | `internal/cli/automation.go:190-215` | FULL | — | `automation.py:277-294`; `tests/test_automation.py:49-72,124` |
| 17 | `drivemount [PATH]` | `-s`; `dfs_ephemeral` interception; propagation failure reporting (added `aa01a39`) | `internal/cli/automation.go:295-378` | PARTIAL | Raw HTML body can be echoed; no JSON `error`/`message` extraction; non-success final POST reports no server detail | `automation.py:41-93,297-316`; `tests/test_automation.py:169-254`; `automation.go:252-293` |
| 18 | `usage` | no flags; `GET /tun/m/ccu-info`; 3-line format | `internal/cli/usage.go:9-30` | FULL | — | `usage.py:20-33`; `tests/test_cli.py:276-291`; `colabapi/types.go:154` |
| 19 | `url` | `-s --host --open`; pipeable URL; `--open` opens browser | `internal/cli/utility.go:21-56` | PARTIAL | `--open` is a **no-op** (`utility.go:259-265`) | `utility.py:50-60,123-126`; `tests/test_url.py:213-232` |
| 20 | `log` | `-s -n/--lines -t/--type -o/--output`; suffix picks exporter | `internal/cli/utility.go:58-147` | FULL | Header/no-history lines go to stderr (Δ1); `-n` default 0 vs upstream `None` (equivalent) | `utility.py:129-206`; `tests/test_cli_log.py:21-105` |
| 21 | `pay` | no flags; print URL + `webbrowser.open` | `internal/cli/utility_extra.go:57-69` | FULL | — | `utility.py:25-31`; `tests/test_pay.py:21` |
| 22 | `version` | no flags; `Version: <v>` | `internal/cli/utility.go:191-201`; `version/version.go:13-15` | DELIBERATE Δ | Prints `golab 0.0.1-dev (compatible with …)` | `utility.py:307-309`; `tests/test_version.py:24-55` |
| 23 | `update [--install]` | PyPI check + pip/uv self-install | `internal/cli/update.go:19-110` | DELIBERATE Δ | Signed GitHub-Releases verification; different message strings; no pip/uv hint | `utility.py:312-345`; `tests/test_update.py:110-569` |
| 24 | `whoami` (hidden) | no flags; tokeninfo identity/scopes/expiry | `internal/cli/utility_extra.go:193-208` | FULL | Error text omits tokeninfo body (security-motivated Δ) | `utility.py:209-304`; `tests/test_whoami.py:53-160` |
| 25 | `test-public-api` (hidden, added `602462f`) | no flags; prints two `OK`/`FAILED` lines | `internal/cli/utility_extra.go:220-248` | FULL | — | `utility.py:390-400`; `tests/test_cli.py:728` |
| 26 | `readme` | no flags; bundled `README.md` | `utility_extra.go:210-218,261` | FULL | Our own bundled content (intentional) | `utility.py:348-382`; `tests/test_readme.py:30-87` |
| 27 | `README` (hidden) | alias of `readme` | `utility_extra.go:262` | FULL | — | `utility.py:414` |
| 28 | `skill` | no flags; bundled `SKILL.md` | `utility_extra.go:263` | FULL | Our own bundled content (intentional) | `utility.py:385-387` |
| 29 | `SKILL` (hidden) | alias of `skill` | `utility_extra.go:264` | FULL | — | `utility.py:416` |
| 30 | `ssh` | `-s --proxy-mode -i/--identity --gpu --tpu --high-mem --rm **`--force`** hidden `--colab-internal-mode`, `-- CMD` | `internal/cli/ssh.go:144-220` | PARTIAL | **`--force` missing** ⇒ no stale-sshd kill in any mode; HTTP 429 text advertises a flag we reject | `ssh.py:736-866`; `tests/test_ssh.py:665-707`; `ssh.go:206-218`; `sshx/errors.go:100-111` |
| +1 | `completion` (new) | n/a | `utility.go:224-249` | DELIBERATE Δ | Extra command; upstream relies on Typer implicit `--show-completion`/`--install-completion` | `cli.py:50-55` (UNVERIFIED at runtime) |

**Not ported upstream command:** `console` was removed upstream before v0.8.0
(`tests/test_cli.py:487-499`); golab does not have it. Correct.

---

## 3. Flag-level and environment gaps

### 3.1 Global flags

| Upstream flag (`cli.py:58-105`) | golab | Status | Note / evidence |
|---|---|---|---|
| `-c`, `--client-oauth-config PATH` (default `~/.colab-cli-oauth-config.json`) | `root.go:109-111`, `defaultOAuthConfigPath()` `root.go:206-212` | FULL | Missing file ⇒ bundled config (`auth/oauth2.go:103-150`). |
| `--config PATH` | `root.go:112` | FULL | Help text omits the upstream `~/.config/colab-cli/sessions.json` hint (`cli.py:69-72`). Cosmetic. |
| `--logtostderr` | `root.go:113` | FULL | Golab also reads `COLAB_DEBUG` (Δ). |
| `--auth {oauth2,adc}` (case-insensitive) | `root.go:114-115`, `auth/auth.go:38-49` | FULL | Upstream default is `oauth2` despite README (`docs/01:80`); golab follows code. |
| `--public-api/--no-public-api` + `COLAB_CLI_PUBLIC_API` | `root.go:61,116-119,195-204` | PARTIAL | One upstream paired flag is modelled as two independent bools. `resolvePublicAPI` checks `--no-public-api` **first**, so specifying both always yields `true` regardless of order; Typer's single flag is last-wins. `parseBoolEnv` (`root.go:221-228`) lacks Click's `t`/`y` true spellings. Port `tests/test_cli.py:716-727`. |
| `-h`, `--help` | cobra automatic | FULL | Rendering differs (§4). |
| `--install-completion`, `--show-completion` (Typer default `add_completion=True`) | none | MISSING / **UNVERIFIED** | Python deps absent so could not confirm on the live tool. Verify with `uv run colab --help` in the upstream clone. Golab's substitute is the `completion` subcommand. |

### 3.2 Environment variables

| Variable | Upstream behaviour | golab behaviour | Status |
|---|---|---|---|
| `COLAB_CLI_PUBLIC_API` | `cli.py:88-95` `envvar=...`; explicit flag wins | `root.go:61` default + `resolvePublicAPI` `root.go:195-204` | FULL (minor truthy-set gap, §3.1) |
| `COLAB_CLI_REQUEST_TIMEOUT` | `client.py:300-304`; unset ⇒ no timeout; explicit `timeout=` kwarg wins | **Ignored.** Fixed 60 s client timeout (`colabapi/client.go:32`) + transport limits (`auth/auth.go:275-292`) | DELIBERATE Δ (Δ7). Port `test_client.py:50-80` if parity required. |
| `COLAB_CLI_ASSIGN_RETRIES` | `client.py:442`; default 3, `0` disables retries | Constant `DefaultRetries = 3` (`colabapi/client.go:29`); `app.New` never populates `colabapi.Config.Retries` (`app/app.go:155-180`) | PARTIAL (same default, no override). Port `test_client.py:84-200`. |
| `COLAB_CLI_RETRY_BACKOFF` | `client.py:443`; default 10 s, doubled per attempt, bounded | Constant `DefaultBackoff = 10s` (`colabapi/client.go:30`); no env read | PARTIAL (same default, no override) |
| Auth env vars | ADC uses Google's standard discovery chain (`auth.py:327` `google.auth.default(scopes=PUBLIC_SCOPES)`), i.e. `GOOGLE_APPLICATION_CREDENTIALS` etc. | `auth/adc.go` uses the equivalent Go ADC chain; no CLI-specific auth env var | FULL (verify exact chain with `internal/auth/adc_test.go:49-188`) |
| `COLAB_AUTH`, `COLAB_DEBUG`, `COLAB_CONFIG_DIR` | Do **not** exist upstream | `root.go:60,62`; `platform/platform.go:26` | DELIBERATE Δ (Δ11) |

### 3.3 Post-v0.8.0 commits — verified one by one

| Commit | Change | golab status | Evidence |
|---|---|---|---|
| `ad85911` partial OAuth scopes | Persist only granted scopes; reapply on refresh | **FULL** — `grantedScopes()` parses token response `scope` and persists it (`auth/oauth2.go:253-270,318-322,369-398`); tests `TestOAuth2PartialGrantPersistsOnlyGrantedScopes`, `TestOAuth2ExpiredTokenRefreshesWithGrantedScopes` | `tests/test_auth.py:664-729`; upstream `auth.py` |
| `02d2321` auth scopes docs | Docs only | N/A | — |
| `274b77f` `stop --all` | **FULL** | `session.go:305-324,348`; `TestStop/all` | `session.py:364-391` |
| `602462f` `--public-api`, `test-public-api`, public `/v1beta/runtimes` list, capability cache | **FULL** for what upstream actually implemented (list only; create/delete are *planned*, `docs/public_api_migration_plan.md`) | `root.go:61,116-119`; `utility_extra.go:220-248`; `colabapi/client.go:434-475` `List`→`listPublic`; `AUDIT-00 §4` | `tests/test_client.py:338,469`; `tests/test_cli.py:716-740` |
| `cda02bc` SSH `--force` | **MISSING** (see P0-1) | `ssh.go` has no `force` var and no kernel-kill helper; 429 strings in `sshx/errors.go:100-111` still recommend it | `ssh.py:807-866`; `tests/test_ssh.py:665-707` |
| `33610a0` "annotate imports after install" | **Not a CLI change** — modifies only `examples/finetune_run.py` | No action needed | `git show 33610a0 --stat` |
| `aa01a39` Drive propagation failures | **PARTIAL** (see P1-3) | `automation.go:249-293` has the actionable text, but not JSON-field extraction / HTML suppression | `automation.py:41-93`; `tests/test_automation.py:169-254` |
| `0abf82d` build cleanup | No functional impact | N/A | — |

### 3.4 Hidden / undocumented flags

| Flag | Upstream | golab |
|---|---|---|
| `--colab-internal-mode {interactive,proxy,cmd}` | `ssh.py:818-820`, hidden, default `"proxy"` | `ssh.go:215-216` hidden, default `""` normalised to proxy (`sshx.NormalizeMode`) — behaviourally FULL |
| hidden commands `auth`, `whoami`, `test-public-api`, `README`, `SKILL` | `automation.py:368`, `utility.py:411-416` | all hidden and registered (`automation.go:195`, `utility_extra.go:197,222,262,264`) — FULL |
| Typer implicit completion options | UNVERIFIED (see §3.1) | absent |

---

## 4. Output / exit-code / message parity

### 4.1 Stream matrix (Δ1)

Upstream uses `typer.echo(...)` (stdout) for almost all messages, including `[colab] …`
chatter; golab deliberately sends chatter to `d.Stderr`. Exceptions that are
**identical** in both: `sessions` / `status` lines, `exec` stream outputs, `ls`
entries, `usage`, `version`, `url` URL line, `log` event lines, `whoami` fields,
`test-public-api` results, `readme`/`skill` bodies, completion scripts.
Notable concrete deviations (all covered by Δ1, listed so a future golden-test
suite does not "fix" them by accident):

| Message | Upstream stream | golab stream |
|---|---|---|
| `[colab] Session READY.` (`new`) | stdout `session.py:243` | stderr `session.go:172` |
| `[colab] No active sessions found on server.` | stdout `session.py:286` | stderr `session.go:195` |
| `[colab] Session 'x' not found.` (`status`, `url`) | stdout `session.py:340` / `utility.py:95` | stderr `errors.go:74` |
| `[colab] Using unique session 'x'.` | stdout `common.py:188` | stderr `app.go:309` |
| `[colab] Error: …` (`ls`, `rm`) | stdout `files.py:51,72` | stderr `files.go:62,108` |
| `[colab] Uploaded/Downloaded/Deleted/Edited…` | stdout `files.py:70,99,129,179-181` | stderr `files.go:111,150,204,297,309` |
| `[colab] Stopped N session(s).` | stdout `session.py:382` | stderr `session.go:322` |
| `[colab] Drive authorization…` / `Press Enter…` | stdout `automation.py:111-116,194` | stderr `automation.go:273-277,229` |
| `[colab] Colab CLI is up to date…` (`update`) | stdout (`tests/test_update.py:261`) | stdout, but with `[colab] ` prefix (`update.go:43`) |

**Caveat:** upstream tests use CliRunner's *combined* output, so Δ1 is invisible to
them. If strict stream parity becomes a goal, these are the lines to change; today
it is a documented decision (Δ1 / DEC-07), not a bug.

### 4.2 Exact-string mismatches beyond Δ1

| Case | Upstream expected string (test) | golab string | Priority |
|---|---|---|---|
| Unknown accelerator | `unsupported value 'A10O'. Supported: T4, L4, G4, H100, A100.` (`session.py:56-67`; `tests/test_cli.py:121`) | `[colab] Invalid value for --gpu: unsupported value 'A10O'. Supported: …` (`session.go:70-71`) | P2 |
| Unknown help command | `No such command 'nope'.` (`cli.py:157`) | `[colab] No such command 'nope'.` (`utility.go:216`) | P2 |
| No sessions | `Create one with 'colab new'.` (`common.py:177,197`) | `Create one with 'golab new'.` (`app.go:298,306,313`) | P2 (or document) |
| `version` | `Version: 0.2.0` (`tests/test_version.py:29`) | `golab 0.0.1-dev (compatible with …)` | Δ — accepted |
| `update` up-to-date | `Colab CLI is up to date (version: 1.0.0)` (`tests/test_update.py:261`) | `[colab] Colab CLI is up to date (version: …).` (`update.go:43`) | Δ — accepted |
| `update` upgrade | `available: 1.1.0 (current: 1.0.0)` + `You can run 'colab update --install' …` + pip/uv hint (`tests/test_update.py:131-133`) | `Run 'golab update --install' …`, no pip/uv hint (`update.go:45-46`) | Δ — accepted |
| `whoami` tokeninfo error | `tokeninfo returned HTTP 400: {body}` (`utility.py:272-275`, `tests/test_whoami.py:94`) | `whoami: tokeninfo returned HTTP 400.` (`utility_extra.go:153`) | Δ — security-motivated |
| Drive 400 with HTML body | no `<html` in output (`tests/test_automation.py:217-219`) | may print `<html…` as `Server said:` (`automation.go:258-263,281-293`) | **P1 — real bug** |
| Help layout | Rich `Usage:/Options:/Commands:` box, alphabetically sorted (`tests/test_cli.py:421-478`) | cobra `Usage:` layout; sorted by cobra default (`EnableCommandSorting=true`) | PARTIAL / UNVERIFIED rendering |

### 4.3 Auto-update suppression list — **exact match**

Upstream `_AUTO_UPDATE_SUPPRESSED` (`cli.py:120-133`):
`update, version, log, pay, help, url, whoami, test-public-api, readme, README,
skill, SKILL` plus piped `ssh` (stdout not a TTY). golab's `suppressed()` + guard
(`root.go:159-190`) lists exactly the same twelve names and the same piped-`ssh`
condition. **No gap.** Golab additionally skips when `d.SettingsStore == nil`
(injected test deps) — desirable. Add a regression test porting
`tests/test_update.py::test_background_check_skipped_for_quiet_subcommands`.

### 4.4 Help ordering

Upstream `AlphabeticalGroup.list_commands()` sorts (`cli.py:38-47`) and two tests pin
it (`tests/test_cli.py:455-478`). golab relies on cobra's default
`EnableCommandSorting=true` and pins only `completion` < `download`
(`files_test.go:189-201`). This is a **test-coverage gap**, not an implementation
gap. **UNVERIFIED** exact rendering (no Go toolchain): verify with
`go run ./cmd/golab help` once a toolchain is available, comparing command order to
`uv run colab --help`.

### 4.5 Shell completion parity

Upstream does not have a `completion` *command*; Typer's `add_completion=True`
default adds hidden `--show-completion` and `--install-completion` callback options
(`cli.py:50-55`; no upstream test found). golab implements a visible `completion
bash|zsh|fish|powershell` command (`utility.go:224-249`, tested by
`TestCompletion`). This is a deliberate addition (Δ11), but the two implicit
upstream options are **MISSING / UNVERIFIED**. Verify with `uv run colab --help`
before deciding whether to add aliases.

### 4.6 Exit codes — spot check (all match)

`run` missing script / malformed `--env` ⇒ 2 (`run.py:304-306`; `run.go:134-145`);
`upload` local missing ⇒ 1; `install` no packages ⇒ 1; `ssh` no key / proxy-mode
with CMD ⇒ 2 (`ssh.py:89,148-153,839-841`; `ssh.go:155-157`); `exec` no input ⇒ 1;
`help` unknown ⇒ 2; `usage` fetch failure ⇒ 1; `new` 412/400 ⇒ 1. `mapError`
(`errors.go:64-101`) and `flagError` (`errors.go:103-109`) cover these consistently.

---

## 5. Upstream tests not covered by golab CLI tests

Grouped by area. "Covered elsewhere" means a lower-layer Go test exists but the
*CLI-level* assertion (message, exit code, ordering, stream) is absent.

**Session / status / stop**
`test_cli_new_gpu_variants` (covered at CLI: `TestNewGPUCaseInsensitive`),
`test_cli_sessions_prune`, `test_cli_session_resolution`, `test_cli_stop_all`,
`test_cli_stop_all_no_sessions` (covered: `TestStop`), `test_cli_new_412_without_accelerator_shows_friendly_error`,
`test_cli_new_non_400_error_propagates`.

**Help / globals**
`test_cli_help_commands_sorted_alphabetically`,
`test_cli_help_subcommand_commands_sorted_alphabetically`, `test_cli_no_args`
(partially covered: `TestHelp`), `test_cli_console_removed`,
`test_cli_public_api_flag_and_env`, `test_state_client_use_public_api_precedence`.

**Execution**
`test_cli_exec_stdin_forwards_shell_escape`, `test_cli_exec_not_found`,
`test_cli_exec_outputs` (partially covered by `TestExecFile`),
`test_ipynb_exec_*` (partially covered by `TestExecIPynb`),
`test_cli_repl_missing_session`, `test_repl_*` rich-TTY cases (many).

**`run`**
`test_run_high_mem_passes_shape_to_assign`, `test_run_teardown_uses_refreshed_token`,
`test_run_env_flag_after_script_sets_env_and_preserves_argv`,
`test_run_env_flags_accumulate_and_split_on_first_equals`,
`test_run_env_flag_escapes_tricky_literals`, `test_run_sets_dunder_main`,
`test_run_systemexit_zero_treated_as_success`,
`test_run_systemexit_string_message_exits_one`,
`test_run_prelude_suppresses_ipython_exit_warning`, `test_run_with_timeout_flag`,
`test_run_unassign_called_on_exception_during_execute`, `test_run_rejects_unknown_accelerator`,
`test_run_with_gpu_flag`, `test_run_with_tpu_flag`, `test_run_nonexistent_script_errors_before_assign`.

**Files**
`test_cli_edit_no_changes`, `test_cli_edit_with_changes` (covered: `TestEdit`),
`test_contents_404_error`, `test_contents_download_notebook_json_format`.

**Automation**
`test_drive_propagation_failure_message_is_actionable`,
`test_drive_propagation_failure_message_includes_server_detail`,
`test_parse_propagation_response_reads_json`,
`test_parse_propagation_response_never_raises`,
`test_drivefs_hook_reports_400_without_raw_html`,
`test_drivefs_hook_does_not_prompt_when_auth_is_not_the_cause`,
`test_drivefs_hook_reports_token_request_failure`,
`test_drivefs_hook_replies_after_successful_propagation`,
`test_show_drive_auth_url_opens_browser`, `test_show_drive_auth_url_headless_still_prints`,
`test_auth_adc_*` (several), `test_remote_flow_partial_grant_persists_only_granted_scopes`
(covered at lower layer: `TestOAuth2PartialGrantPersistsOnlyGrantedScopes`).

**SSH**
`test_force_kills_stale_sshd`, `test_kill_stale_sshd_body`,
`test_explain_handshake_failure_429_without_usable_limit`,
`test_explain_handshake_failure_session_limit_past_200_char_snippet`,
`test_explain_handshake_failure_session_limit_invalid_utf8_elsewhere`,
`test_explain_handshake_failure_decodes_str_body`,
`test_connect_websocket_network_failure_exits_1`,
`test_bridge_proxy_mode_pumps_ws_to_stdout` / `…stdin_to_ws` (covered in `sshx/proxy_test.go`),
`test_resolve_pubkey_*` (covered in `sshx/keys_test.go`),
`test_proxy_command_round_trips_through_the_shell`,
`test_proxy_command_keeps_global_options`,
`test_hidden_mode_option_stays_out_of_help_and_suggestions`,
`test_ssh_help_advertises_autocreate_flags`,
`test_ssh_wire_contract_*` (partially covered by `sshx/wire_test.go`),
`test_ssh_workdir_*`, `test_windows_ssh_*`, `test_proxy_select_routes_create_output_to_stderr`,
`test_proxy_bridge_routes_rm_output_to_stderr`,
`test_proxy_mode_autocreate_failure_skips_connect`,
`test_interactive_autocreate_failure_skips_ssh`,
`test_gpu_and_tpu_both_forwarded_to_new`,
`test_proxy_mode_rm_teardown_idempotent`, `test_proxy_mode_rm_stops_reused_session`.

**URL / update / misc**
`test_url_*` (most: trailing slash, special chars, both signals, no-open default,
fragment not encoded, output pipeable — only partially covered by `TestURL`),
`test_url_open_flag_launches_browser`,
`test_update_*` (almost all except throttle/install/verification-failure),
`test_readme_failure`, `test_cli_log_show`, `test_cli_log_show_filter`,
`test_cli_log_export` (partially covered by `TestLog`), `test_whoami_*` (partially),
`test_client_*` env-var tests, `test_streaming_*` (covered in `internal/runtime/execute_test.go`).

---

## 6. Implementation plan (work backlog)

Effort: **S** ≈ ≤1 session, **M** ≈ 2–4 sessions, **L** ≈ >4 sessions.
Every item is test-first; the named upstream test is the specification.

### P0 — close before claiming parity

**P0-1 · `ssh --force` (M)**
- Files: `internal/cli/ssh.go` (add `force bool`, flag registration at `:206-218`, invoke in all four paths: cmd, hidden cmd-mode, proxy, interactive); new helper in `internal/sshx` (or `internal/runtime`) that runs
  `import os; os.system('pkill -P $(pgrep -o -x sshd)')` with a 10 s idle timeout, passing `kernel_id`, and always closing the kernel; stderr lines exactly `[colab] --force: killing stale SSH sessions...` and `[colab] --force: failed to kill sshd: <err>` (`ssh.py:410-424`).
- Tests first: port `tests/test_ssh.py::test_force_kills_stale_sshd` (parametrised interactive/proxy/cmd/no-force) and `test_kill_stale_sshd_body` into `internal/cli/ssh_test.go` (with a fake-kernel script assertion). Update `internal/sshx/errors_test.go` to pin the existing 429 strings (they already match; this prevents regression).
- Do **not** add an alias `--forced`: upstream has none (`ssh.py:807-817`; `git show cda02bc`). Keep the misleading-free 429 text only after the flag exists.
- Note: `_proxy_command()` upstream never forwards `--force` (`ssh.py:473-495`); mirror that, don't "fix" it.

**P0-2 · make `url --open` actually open (S)**
- Files: `internal/cli/utility.go:46-48` → call `openURL(connect)` from `utility_extra.go:38-55`; delete or repurpose the no-op `openBrowser` at `utility.go:259-265`.
- Test first: port `tests/test_url.py::test_url_open_flag_launches_browser` and `test_url_no_open_by_default` by injecting a launcher func (current `openURL` calls `exec.LookPath`, so add an injectable var) and asserting launch/no-launch.
- Also fix `docs/COMPAT.md:21-22` in the finalize step of that change (doc currently lies).

**P0-3 · stop Drive failure HTML from reaching the terminal (M)**
- Files: `internal/cli/automation.go:249-293`; add a small `propagationDetail(body string) string` that tries `parseFlexibleJSON`-style parsing (already in `colabapi/client.go:566-584`) and returns only `error`/`message`. `reportDriveFailure` must pass that extracted string, never `StatusError.Body`, to `driveFailureMessage`. Also extract detail from the final POST's JSON when `Success == false` (`automation.go:360-365`).
- Tests first: port `test_drive_propagation_failure_message_is_actionable`, `test_drive_propagation_failure_message_includes_server_detail`, `test_drivefs_hook_reports_400_without_raw_html`, `test_drivefs_hook_does_not_prompt_when_auth_is_not_the_cause`, `test_drivefs_hook_reports_token_request_failure`, `test_parse_propagation_response_never_raises` into `internal/cli/automation_test.go`.

### P1 — parity-complete

**P1-1 · env-var overrides (M)**
- Files: `internal/cli/root.go:45-63` (read the three vars), `internal/app/app.go:28-45` (add `RequestTimeout`, `Retries`, `Backoff` to `Config`), `internal/app/app.go:155-180` (populate `colabapi.Config`), `internal/colabapi/client.go:75-105` (honour `cfg.Retries/Backoff` — already supported).
- Constraint: keep AGENTS.md #5 — clamp to sane bounds (e.g. timeout 1 s…24 h; retries 0…10; backoff 0…5 min) and never log values. `COLAB_CLI_REQUEST_TIMEOUT=0` should mean upstream's "no timeout" only if the context still bounds the call.
- Tests first: port `test_client.py::test_request_timeout_override_and_explicit_precedence`, `test_assign_retries_can_be_disabled`, `test_assign_recovers_before_another_post`, `test_assign_uses_bounded_backoff` into `internal/colabapi/client_test.go` and add a root-level env test.

**P1-2 · string/stream parity for messages that tests assert (S)**
- Files: `internal/cli/session.go:59-72` (drop the `[colab] Invalid value for …` wrapper; keep exit 2), `internal/cli/utility.go:216` (drop `[colab] ` for unknown command), `internal/app/app.go:298,306,313` (`colab new` wording) — or record explicit Δs if the team prefers the golab-branded wording.
- Tests first: port `tests/test_cli.py::test_cli_new_rejects_unknown_gpu/tpu`, `test_cli_help` unknown-command branch, `test_cli_status` no-sessions branch.

**P1-3 · `--public-api/--no-public-api` semantics (S)**
- Files: `internal/cli/root.go:116-119,195-204`; track flag order (pflag supports `Flags().Changed` but not order — either use a custom `pflag.Value` that records the last-set value, or disallow both). Extend `parseBoolEnv` with Click truthy/falsey sets (`1,true,t,yes,y,on` / `0,false,f,no,n,off`).
- Test first: port `test_cli.py::test_cli_public_api_flag_and_env` and `test_common.py::test_state_client_use_public_api_precedence`.

**P1-4 · completion parity decision (S)**
- Verify first: `cd /tmp/opencode/google-colab-cli && uv run colab --help` (outside this audit environment). If `--show-completion`/`--install-completion` are present, either add hidden root flags that emit the same scripts or document Δ11 explicitly and add a test pinning the decision.
- Files: `internal/cli/root.go`, `internal/cli/utility.go:224-249`.

**P1-5 · golden-test the auto-update suppression list (S)**
- Files: `internal/cli/root_test.go` (new) or `internal/cli/update_test.go`; pin `suppressed()` against the exact 12-name set and piped-ssh. Port `tests/test_update.py::test_background_check_skipped_for_quiet_subcommands`, `test_cached_banner_suppressed_when_update_check_disabled`.

### P2 — hygiene / docs

**P2-1 · help ordering test (S)** — port `test_cli_help_commands_sorted_alphabetically` and `test_cli_help_subcommand_commands_sorted_alphabetically` into `internal/cli/files_test.go:TestHelp`; verify with a real toolchain (`go test ./internal/cli/...`). No code change expected.

**P2-2 · `log` filter/export CLI tests (S)** — port `test_cli_log_show_filter`, `test_cli_log_export` into `internal/cli/session_test.go:TestLog`; current coverage only checks a raw EXEC line and one `.md` export.

**P2-3 · `run` semantics tests (S–M)** — port the SystemExit family (`test_run_systemexit_zero_treated_as_success`, `…string_message_exits_one`), `test_run_prelude_suppresses_ipython_exit_warning`, `test_run_env_flag_after_script_sets_env_and_preserves_argv`, `test_run_teardown_uses_refreshed_token` into `internal/cli/execution_test.go:TestRunFlow`. Code is likely already correct; this is coverage.

**P2-4 · docs corrections (S)** — in the finalize step of the relevant changes: `docs/COMPAT.md:21-22` (`url --open` launcher claim), `docs/05:6,27,63` (`ssh --force` scope decision), `docs/05:42` (run `-s` reuse / signal teardown claim), `docs/COMPAT.md:11-12` ("CLI wiring pending" is now false), and add dated changelog notes per AGENTS.md.

### Explicitly no action

* `--tpu` wins over `--gpu` — implemented (`session.go:29-49`), test `TestNewTPUSilentlyWins`.
* partial scopes — implemented (`auth/oauth2.go:369-374`), test `TestOAuth2PartialGrantPersistsOnlyGrantedScopes`.
* `stop --all`, `--public-api`, `test-public-api` — implemented (§3.3).
* 429 message wording — byte-identical to `_LIMIT_*_TEXT` / `_FALLBACK_429` (`tests/test_ssh.py:226-249`).
* `completion` command, stderr chatter (Δ1), atomic state/session-name validation/chunked upload/signed update — deliberate deviations; do not "fix" without a decision.
* Public API create/delete LRO — **planned upstream, not implemented** (`docs/public_api_migration_plan.md`, `AUDIT-00 §4`); not a parity gap.

### Verification checklist when a toolchain is available

```sh
cd /content/drive/MyDrive/golab
go vet ./... && golangci-lint run && go test -race -count=1 ./...
GOOS=linux GOARCH=amd64 go build ./...
go run ./cmd/golab --help            # compare command order with upstream
go run ./cmd/golab ssh --help        # confirm --force absent before P0-1
cd /tmp/opencode/google-colab-cli && uv run colab --help   # confirm completion flags
```
