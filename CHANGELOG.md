# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Breaking changes are defined as changes to the CLI surface, flag semantics,
on-disk schema or persisted token format (per AGENTS.md).

## [0.4.0] - 2026-11-28

### Added

- **Named accounts with isolated sessions.** `golab account add|list|show|use|remove`
  manages versioned profiles in a 0600 atomic `accounts.json`; the existing
  single-identity token and `sessions.json` become `default` with no data
  movement. Named profiles use per-account keyring entries and derived
  `oauth_token_<name>.json` / `sessions_<name>.json` files. `GOLAB_ACCOUNT` and
  the global `--account` flag scope any invocation to one account.
- **Browser-created sessions.** `golab sessions --web` discovers Web UI runtimes
  from the read-only `List` on both the legacy tunnel and Public API planes;
  `attach --all` imports all unattached runtimes idempotently and
  `attach --refresh` renews expired proxy tokens. `attach` no longer requires
  the Public API, so no GCP setup is needed on the default path. No POST/assign
  is issued.
- **Self-bootstrapping installers.** `install.sh` falls back from curl→wget→
  Python urllib, sha256sum→shasum→openssl→hashlib, tar→Python tarfile and
  mktemp→private `$$` dir; it downloads and double-verifies a pinned cosign
  v2.6.5 when none is present. `install.ps1` uses PowerShell built-ins and the
  same pinned cosign bootstrap. The checksum + cosign trust gate, allow-listed
  extraction and both-binary smoke tests are unchanged.
- **MCP tools 23–25** (`cmd/golab-mcp`, 25 total): `colab_update` (read-only
  `check` / gated signed `install`), `colab_install_mcp` (auto or manual config
  for OpenCode, Claude Code, Cursor, Gemini CLI, Codex CLI and VS Code — all or
  named agents, dry-run by default) and `colab_install_skills` (unified, project,
  named-agent or all targets; only the two known portable skill trees, with
  traversal rejection). Implemented in a new injected-filesystem package
  `internal/agentinstall`; 13 tests plus stdio/HTTP/legacy handshake assertions.
- **Standalone `golab-mcp` download**: a second GoReleaser archive set
  (`golab-mcp_<version>_<os>_<arch>.tar.gz`, plus Windows `.zip`) with the same
  signed provenance as the CLI archives, wired into the release workflow's
  sign/upload/proof steps. Ships in the **next** release; v0.3.0 assets are
  immutable. `go install github.com/20Youssef10/golab/cmd/golab-mcp@v0.3.0`
  was verified live.

### Fixed

- `compat refresh` built `https://raw.githubusercontent.com/<ref>/<path>` with
  no owner/repo segment, so every production fetch returned HTTP 404. The URL
  now pins the official `googlecolab/google-colab-cli` repository; the `--ref`
  help text no longer implies the compatibility label `0.8.x+HEAD` is a
  resolvable git tag. Live proof: a snapshot of tag `v0.8.0` was fetched and
  consumed by `compat report`.
- `install.sh` / `install.ps1` now reject cosign older than v2.6.5 with an
  actionable version error. Releases use `--new-bundle-format` sigstore v0.3
  bundles; cosign v2.4.1 reports the misleading `bundle does not contain cert
  for verification` and cannot verify them (live VM proof: FAIL with v2.4.1,
  PASS with v2.6.5).
- Third-party manifests (Homebrew, Scoop, nfpm) pinned to the published signed
  v0.3.0 archives with real `checksums.txt` digests; winget keeps its three
  real v0.3.0 `.zip` hashes.
- Spend-safety audit (`docs/gap-report.md` §2–3) found seven defects, all fixed
  test-first: a failed session-record write now compensates with `Unassign`
  instead of orphaning a billable runtime; `stop --all` reports the real stop
  count and exits non-zero naming any session that failed to stop; `run` reads
  the script before allocating so an unreadable script makes zero requests;
  `ssh` auto-create now consults the budget guard; an enforced guard fails
  closed when usage cannot be read; `logout` exits non-zero when proxy tokens
  could not be cleared; and `max-balance` is corrected to a minimum reserve
  floor (config key and flag name retained for compatibility).

- Trust anchors from the same audit: the OAuth refresh token endpoint is now
  pinned to `https://oauth2.googleapis.com/token` and the embedded client ID
  is authoritative, so a planted token blob cannot redirect credentials or
  override the client pin; the self-update certificate identity now requires
  a `refs/tags/v` release-workflow SAN, rejecting branch/dispatch runs; and
  `golab.yaml` profiles now parse `enforce: true|false`, so profile-based budget
  enforcement actually works (named profiles override defaults).

- Resume and protocol-integrity fixes from the same audit: the journal now
  persists that `status:idle` was seen, so a socket drop between idle and
  `execute_reply` can be resumed to a terminal result instead of timing out;
  output accumulation and journal retention are bounded by count and bytes;
  a cancel during socket loss posts exactly one interrupt; colab_request hooks
  run on a bounded worker and socket writes hold the write lock; a timed-out
  kernel create reconciles with the server before retrying so a late-successful
  create is reused rather than duplicated; `requestId` is stable across retries;
  and `ALREADY_EXISTS` reattachment now verifies variant/accelerator/shape.

- Silent-false-success fixes from the same audit: notebook saves no longer
  announce success before the write succeeds, and a failed pending-execution
  record aborts before the cell is submitted (non-critical record writes are
  debug-logged instead of discarded); `[colab] …` diagnostics in `logout` and
  `update` now go to stderr, keeping `--output json` clean; typed prompt
  replies are redacted before being written to history (the kernel still gets
  the real value); upload verification fails loudly on a failed/mismatched
  size check, including non-seekable streams; and `--budget-profile` /
  `GOLAB_BUDGET_PROFILE` select a named budget profile for the guard.

- Protocol and response-integrity fixes from the gap report: kernel IDs are
  URL-escaped in REST paths; oversized REST and control-plane responses now
  return typed errors instead of decoding truncated JSON; `clear_output` and
  `update_display_data` emit streaming events; frame offsets are checked before
  a `uint32` cast; the dead journal `terminal` field was removed; `doctor --fixtures`
  reports `skip` on an empty inventory; and a recoverable `usage` warning exits 0.

- File and release-chain hardening from the gap report: token and session/settings
  files are read with `Lstat` symlink/non-regular checks and mode self-healing,
  and token writes take a cross-process flock; installed binaries and state
  renames fsync their files and parent directories; update downloads reject
  off-host or https→http redirects; the release workflow now splits the
  unprivileged `build` job from the `id-token: write` `sign` job while keeping
  the pinned certificate identity unchanged.

- SSH/platform hardening from the gap report: embedded SSH connections now
  pin host keys with TOFU (stored in a separate private `known_hosts.json`, so
  no sessions schema migration); loose private-key permissions emit a warning;
  test-only env overrides (`COLAB_TEST_*`, `COLAB_TEST_NO_SIGNALS`,
  `COLAB_TEST_AGENT`) are inert in release binaries; the keyring probe is
  cancellable and caches unavailability; `stop` path containment is asserted in
  sync apply; the auto-update notice skips `--explain` and joins its async
  writer before exit.

- Final gap-report fixes: `stop --all` now refuses to act without `--yes` when
  stdin is not a TTY (matching the documented contract); `golab.yaml` is parsed
  with strict `yaml.v3` (document markers, quoted/block scalars, flow maps,
  duplicate and unknown keys all handled); the secret-redaction gate scans the
  whole tree for more credential families; `filepath.Dir` replaces the hand-rolled
  dirname; temporary rendered images (including REPL) are cleaned up; markdown
  export emits valid fences; notebook multiline lists are validated; the audit
  history-directory finding has its own ID; and two unused gosec exclusions were
  removed. Batch 7's new security tests were made portable to Windows runners.

- Documentation and gate reconciliation: README/INDEX/CHANGELOG/CONFORMANCE
  and the full docs pack now describe v0.2.0 truthfully (fresh counts: 664
  tests in 103 files across 25 packages, 75.8% coverage), all user-facing
  `colab` command hints point at `golab`, and the `colab`→`golab` exit-code,
  version-string, repo-tree, harness, DEC-08 and S-3 drift is resolved. The
  coverage gate now ratchets `platform`/`app` as well as the original five
  packages, and `goleak-check` is a hard CI gate instead of advisory.

- Live e2e on billable CPU VMs (2026-11-26) exercised all six tiers: allocation
  with `--ttl` and `--wait-capacity`, `wait`, `ps`/`cat`/`tail`/`grep`/`du`/
  `find`/`fetch`, `exec` with `--secret`/`--env-file`, `run --json` with notebook
  parameters, `run --detach` + `jobs`/`logs`, `reap` (which removed a real
  orphan), `cost`, the config/init/state/doctor/trace/compat/update commands,
  and the Public API quota-project failure path. It found and fixed two Tier 2
  defects: `du` failed to decode the array-shaped `lines` payload, and relative
  paths in `tail`/`grep`/`du`/`fetch` resolved against kernel cwd `/content`
  instead of VM root, so files uploaded to `/sample.txt` were invisible. All
  cases were re-verified live after the fix; cleanup left no assignments.

- Live e2e follow-up (2026-11-27): billable CPU probes closed several checks
  that had been deferred as human-only — a CPU `new` / `run` pass, the sweeper
  (which left no assignments), GET-only wire/trace capture and the goleak and
  coverage gates. The two Tier 2 defects found on 2026-11-26 (`du` decoding the
  array-shaped `lines` payload and relative paths in `tail`/`grep`/`du`/`fetch`
  resolving against kernel cwd `/content` instead of VM root) were re-verified
  live after the fix. **Not covered:** OAuth re-consent, Drive browser approval,
  interactive TTY, real device matrices, the GCP quota project, or uploads
  larger than 75 MiB. See
  [CONFORMANCE.md §6](docs/CONFORMANCE.md#6-human-only--external-steps-blocking-done).

- Cost-ledger appends are now serialized across processes. `cost.New(path)` is
  constructed afresh by every CCU sampler (budget guard, `usage`, `--max-cu`
  monitor), so an in-process mutex alone let distinct Ledger instances — and
  distinct `golab` processes — read the same pre-append bytes and let the last
  rename silently drop the other samples. The whole read-modify-rename cycle
  now holds a per-path OS advisory lock with a 10-second timeout.

- `doctor --fix` test fixture no longer depends on the host clock or timezone.
  The stale-lock fixture derived its mtime from `time.Now().Add(-48h)`, which a
  fixed UTC test clock could see as only ~22 h old in a `+0200` timezone, so the
  stale-lock remediation disappeared. It now uses a fixed UTC date matching the
  injected filesystem clock. This is a test-only fix; no CLI behaviour changed.

- Release workflow least privilege: the `build` job now holds `contents: read`
  only, and `sign` is the only job with `contents: write` and `id-token: write`.
  The pinned Fulcio certificate identity is unchanged. No live tagged release
  has exercised the new `dist/` artifact hand-off yet.

- `run --detach` now rejects an explicitly supplied `--max-cu` or `--enforce`
  with exit 2 before any allocation. The CU guard samples an account-level
  endpoint inside the foreground process and cannot follow a detached child;
  wiring a second remote watchdog is deferred rather than silently ignored.

- Post-release audit + live e2e found and fixed seven defects in the batch 2
  features: `exec --json` now matches text mode for `SystemExit(0)` (clean
  `status:"ok"`, no IPython banner or "To exit: use …" warning); all `--env-file` values are redacted even
  when the key name looks ordinary (previously plaintext in history and exported
  logs); `--secret` refuses reserved internal credential names such as
  `oauth_token`; saved notebooks keep cell-level `execution_count`; `--output-image`
  paths containing `]` are reported exactly; and `drivemount --wait-for-consent`
  sanitizes the server-supplied auth URI, retries transient probe failures and
  unblocks its prompt reader on cancellation.

### Verification

v0.2.0 release run `45489842618` and CI run `45487165817` are green; a real
v0.1.9 → v0.2.0 signed self-update installed an executable ELF. Live e2e on a
billable CPU VM covered `new`, `status`, `ls`, `exec` (text/JSON/secrets),
`run` (script and notebook), `ssh`, piped `repl`, upload/download, `stop --verify`,
`resume` error paths and cleanup — final `golab sessions` was empty. Fuzz smoke
passed both seeds. Coverage gate misses the 80% target for `state`/`sshx`
(the CI floor is 70%, as documented). Cross-build smoke was interrupted by a
server restart and is not claimed. Fake-kernel tests cover all six fixes, and a
billable live re-run on 2026-11-26/27 re-verified the fixed cases (see the e2e
entries above). The newer surfaces — `golab-mcp`, the Agent Skills,
`ssh --accept-host-key`, `compat refresh`, the packaging configs and the
cost/doctorfix changes — are static- or fake-tested only: `internal/mcpserver`
covers both transports and legacy `2025-06-18` negotiation, and both skills pass
`agentskills validate`. `golab-mcp` is **not** in `.goreleaser.yaml`, so no
signed release archive or self-update proof exists for it.

### Added

- Six feature tiers landed on `main` (2026-11-26), all written test-first with
  fakes; at landing **no live e2e proof was claimed for any of them**. A later
  billable CPU pass (2026-11-26/27, see `### Fixed` above) covered part of
  these surfaces; it did not prove all of them. **Tier 1 (spend safety):**
  `reap [--older-than --dry-run --yes --json]` unassigns server runtimes with no local record and prunes stale
  local records; `new --ttl D [--ttl-action warn|stop]` stores a local expiry
  checked on every invocation (no daemon or timer) and `--wait-capacity D`
  retries capacity refusals; `cost [--since --json]` reports observed compute
  units from `GET /tun/m/ccu-info` (account-level, CU only, no prices); and
  `run --max-cu N [--enforce]` guards a foreground run against that same
  account-level balance. **Tier 2 (VM inspection):** `ps` (alias `top`), `cat`,
  `tail [-f -n --interval]`, `grep [-i --glob --max-matches]`, `du`, `find` and
  `fetch [--sha256]` read the VM through server-side polling (`internal/vmstat`);
  `tail -f` is not byte-perfect under log rotation. **Tier 3 (background):**
  `run --detach --ttl`, `jobs`, `logs ID [-f]`, `wait --for kernel|idle|ready
  --timeout` and `sync --watch`; detached jobs are **remote**, there is no local
  daemon and the VM stays allocated and billable. `--ttl` was mandatory but
  advisory at landing; it is now **remotely enforced** — see `### Changed`
  below and the amended [ADR-001](docs/adr-001-detached-runs.md).
  **Tier 4 (reproducibility):**
  `env freeze -s NAME -o FILE`, `run --lockfile`, `run -p K=V --params-file`,
  `run --bundle --out` and `bundle verify --env`; metadata-only verify is
  RECORDED, while `--env` actually re-checks the environment in a fresh VM
  (`internal/envlock`, `internal/nbparams`). **Tier 5 (ergonomics):** `config
  get|set|unset|list|path`, `init [--force]`, `use NAME|--clear` with
  `GOLAB_SESSION` (resolution `-s` > env > persisted default > unique),
  `status --watch --interval`, `log -f`, and `run --json --secret --env-file
  --output-image` parity with `exec`. **Tier 6 (honesty):**
  `GOLAB_TRACE=file.ndjson` and `trace -- <cmd>` append redacted wire traces —
  query **keys** only, body shape/hash, never headers or tokens — with
  `--unsafe-bodies` as an explicit, labelled opt-in; `compat report [--json]`
  compares against an embedded **self-snapshot**, not a live upstream;
  `doctor --fix [--apply]` plans locally and applies only safe reversible fixes
  (dry-run default); `state migrate [--dry-run|--apply]` stamps an additive
  `schema_version` while preserving unknown fields; `update --verify-only PATH`
  verifies a local artifact; and new `install.sh` / `install.ps1` verify a
  SHA-256 checksum plus the cosign signature. New gate packages exist for cost,
  vmstat, detach, envlock, nbparams, compat, doctorfix and trace; the coverage
  gate still checks the original seven packages. See [USAGE.md](docs/USAGE.md),
  [MIGRATION.md](docs/MIGRATION.md) and [CONFORMANCE.md](docs/CONFORMANCE.md).

- Upstream `googlecolab/google-colab-cli` issue #289 (reviewed 2026-11-26, tracked
  in [CONFORMANCE.md](docs/CONFORMANCE.md)): the planned 1.0 release candidate
  switches the control plane to Colab's Public API and requires users to re-run
  auth and set a GCP quota project. In readiness, `golab doctor --section api`
  and hidden `golab test-public-api` now report quota-project/re-consent
  prerequisites from local signals only (no GCP mutation), migration docs cover
  the public-API switch, and a new `golab attach --runtime NAME [--session NAME]`
  records browser-created runtimes as local sessions (Public API required, zero
  allocation POSTs). golab already had the full Public API lifecycle, both
  control-plane flags, `logout`, WS reconnect/resume and bounded timeouts.
  Deliberately NOT done: no `init-gcp-project` helper (GCP provisioning stays
  human-controlled), no default flip, and no upstream 1.0 support claim —
  `version.Upstream` remains `0.8.x+HEAD` and the CLI default stays the tunnel
  API until live proof. The tunnel API may be turned down soon, removing the
  `--no-public-api` / 0.8.0 opt-out.

- **Colab MCP server** (`cmd/golab-mcp`): a dedicated static binary built on the
  official `modelcontextprotocol/go-sdk` (v1.7.0). It exposes 22 `colab_*` tools
  from an allow-list (no shell, arbitrary-code or raw-request tool), with an
  explicit `confirm: true` required for every billable/destructive tool, and two
  resources: `golab://docs/skill` and `golab://docs/usage`. Transports are
  `--transport stdio` (default) and `--transport http --addr :8080`; HTTP is
  streamable MCP at `/mcp` (also `/`), with no `/sse` endpoint. Tests cover
  legacy clients negotiating protocol version `2025-06-18`. Install with
  `go install github.com/20Youssef10/golab/cmd/golab-mcp@latest`.
  **Follow-up:** `golab-mcp` is not in `.goreleaser.yaml`, so release archives
  and the signed-update path do not publish it yet. See
  [USAGE.md](docs/USAGE.md#12-mcp-server-golab-mcp).

- **Portable Agent Skills** at `.agents/skills/golab-operations` and
  `.agents/skills/colab-mcp` (each a `SKILL.md` plus `references/`), validated
  with the official `agentskills` CLI. They document safe operation and client
  configuration; they are not live proof of any underlying CLI behaviour.

- `ssh --accept-host-key SHA256:<fingerprint>` (the `SHA256:` prefix is optional)
  pins a runtime host key explicitly. The pin is stored only after the live
  handshake matches it; a stored pin must also match the explicit value. An
  invalid fingerprint or a mismatch is a usage error (exit 2) before allocation.

- `golab compat refresh [--out FILE] [--ref REF]` fetches the official upstream
  Typer source from `raw.githubusercontent.com` at a pinned ref and writes a
  provenance-tagged, explicitly **partial** command-surface snapshot. It is
  dry-run by default (`--out -` prints JSON without writing) and never
  synthesises flags from parameter names or comments. Feed the file to
  `golab compat report --snapshot FILE`. This is a bounded source parse, not a
  live upstream CLI comparison and not a claim of complete upstream coverage.

- Packaging configs under `packaging/`: Homebrew formula, Scoop manifest, winget
  version/locale/installer manifests and an nfpm config. None is wired into
  release CI or live-proven on this host; winget is blocked pending a `.zip`
  repack. See the [platform matrix](docs/04-platform-matrix.md#7-distribution-channels).

### Changed

- **Detached-run `--ttl` semantics (breaking).** `run --detach --ttl D` previously
  stored an advisory deadline that did not stop anything; it is now enforced by
  a remote watchdog on the VM that polls every 5 seconds, kills the whole
  process group at the deadline and records `exit=124` /
  `ttl-reason=ttl_exceeded`. The watchdog stops the *job*, not the runtime — the
  VM remains allocated and billable until `golab stop`, and a watchdog cannot
  survive VM reclamation. [ADR-001](docs/adr-001-detached-runs.md) is amended;
  see [MIGRATION.md](docs/MIGRATION.md#new-ergonomics-and-spend-semantics).


## [0.3.0] - 2026-11-27

Release summary for the `v0.3.0` tag. The last published release is `v0.2.0`;
the individual fix entries that landed on `main` before the tag remain in
`[Unreleased]` below.

### Added

- **MCP server** (`cmd/golab-mcp`): a second static binary built on the official
  `modelcontextprotocol/go-sdk`, exposing 22 allow-listed `colab_*` tools (15
  read-only, 7 requiring `confirm: true`), stdio and streamable HTTP at `/mcp`
  (also `/`, no `/sse`), `golab://docs/skill` and `golab://docs/usage` resources,
  CLI redaction, and legacy `2025-06-18` negotiation tests.
- **Portable Agent Skills** at `.agents/skills/golab-operations` and
  `.agents/skills/colab-mcp`, both validated with the `agentskills` CLI.
- **Spend safety (Tier 1):** `reap`, `new --ttl` / `--ttl-action`,
  `--wait-capacity`, `cost`, `run --max-cu [--enforce]` and named budget profiles.
  CU reporting is account-level and carries no prices.
- **VM inspection (Tier 2):** `ps` (`top`), `cat`, `tail`, `grep`, `du`, `find`
  and `fetch [--sha256]` via server-side polling; `tail -f` is not byte-perfect
  across log rotation.
- **Detached runs (Tier 3, ADR-001):** `run --detach --ttl`, `jobs`,
  `logs ID [-f]`, `wait` and `sync --watch`; `--ttl` is enforced by a remote
  5-second watchdog (`exit=124` / `ttl-reason=ttl_exceeded`), and `--detach`
  with an explicit `--max-cu` / `--enforce` is rejected before allocation.
- **SSH host-key pinning** with `ssh --accept-host-key SHA256:…` (TOFU; a
  mismatch exits before allocation) and **`compat refresh [--out] [--ref]`**,
  which writes a provenance-tagged, explicitly partial snapshot.
- **Distribution:** `.goreleaser.yaml` builds both binaries and ships both plus
  `.agents/skills/` in every archive — 13 `.tar.gz` plus 3 Windows `.zip`
  repacks, all with checksums, cosign signatures and SBOM attestations.
  `install.sh` / `install.ps1` install both binaries and the skills
  (`--no-skills` / `--skills-dir`, `-NoSkills` / `-SkillsDir`); `Dockerfile`
  and `.github/workflows/docker.yml` publish a distroless nonroot image to
  `ghcr.io/20youssef10/golab` on `main` and `v*` tags with provenance, SBOM and
  digest smoke tests. Winget manifests point at the v0.3.0 `.zip` assets, but
  their `InstallerSha256` values remain `REPLACE_AT_RELEASE` until the tag
  exists.

### Fixed

- Follow-up fixes from the spend-safety, protocol and response-integrity audits:
  compensating unassign after a failed session write, honest `stop --all`
  counts, pre-allocation script reads, a guard on `ssh` auto-create, a
  fail-closed CU guard, `logout` exit status, a cross-process cost-ledger flock,
  pinned OAuth token endpoint/client ID, strict `golab.yaml` parsing, URL-escaped
  kernel IDs, bounded responses, resume journal continuity, one interrupt on
  socket loss, and redacted typed prompt replies.
- Silent-false-success fixes (notebook saves, execution records, upload size
  checks), stderr diagnostics in `logout` / `update`, valid markdown fences,
  and portable security tests.

### Verification

- Billable CPU e2e on 2026-11-26/27 covered all six tiers, including VM
  inspection, detached submission, `reap`, `cost`, config/doctor/trace/compat
  and the sweeper; two Tier 2 path/decoding defects were fixed and re-verified.
  OAuth re-consent, Drive approval, interactive TTY, real devices, the GCP
  quota project and >75 MiB uploads remain human-only.
- `go vet ./...`, `go test -race -count=1 ./...`, `golangci-lint` (0 issues),
  `govulncheck`, `go mod verify`, redaction/path gates and goleak/coverage gates
  are green on `main`; `docs/CONFORMANCE.md` is the layered ledger.

## [Unreleased]

## [0.2.0] - 2026-11-24

### Added

- Second upstream batch: `colab exec --json` emits NDJSON per-cell results with
  the same CPython exit codes (PR #237); `--secret NAME` and `--env-file PATH`
  provide a local-only secrets bridge with mandatory redaction (issue #157;
  Colab Web/browser secrets are deliberately not read); `--scopes full|minimal`
  (or `COLAB_SCOPES`) narrows requested OAuth scopes and forces re-consent on a
  mode change (issue #71); `drivemount --wait-for-consent` polls for browser
  approval instead of requiring Enter (issue #258; the approval itself remains
  human-only).
- `colab resume -s NAME [--timeout S]` reattaches to a transport-interrupted
  execution and waits for late output/result without resubmitting. A bounded
  msg-id journal deduplicates replayed frames; messages emitted strictly while
  the socket was down cannot be fabricated and surface as a typed
  `ErrOutputGap` with a resume hint. `run` no longer tears down a session on a
  transport error (issue #220 / PR #221, scoped implementation).

- `colab run <notebook>.ipynb` executes code cells in order with shared kernel
  state, skips markdown, writes `<base>_output.ipynb` with mode 0600, parses
  before any VM allocation and warns (without injecting) on extra script args
  (upstream issue #74 / PR #200).
- `colab stop --verify` (also with `--all`) re-checks `/tun/m/assignments` after
  unassigning and exits non-zero if the runtime is still assigned (upstream #47).
- Bounded kernel WebSocket reconnect (~30 s) with a typed
  `ConnectionLostError`; transient drops resume output on the same session,
  permanent drops fail explicitly (upstream PR #261 / issue #220).
- `docs/INSTALL-TERMUX.md`: Go-specific Termux install, checksum/cosign
  verification, keyring fallback and runtime notes (upstream issue #131; the
  Python-specific upstream guide does not apply to a static Go binary).

### Fixed

- `colab exec` now propagates CPython `SystemExit`/error exit codes and stops a
  notebook at the first failing cell while still writing the 0600 output file
  (upstream PRs #278/#280).
- Body-less control-plane POSTs use chunked transfer instead of
  `Content-Length: 0`, avoiding HTTP 411 behind forward proxies (upstream #270).
- A final assign timeout now performs one GET reconciliation before returning,
  so an allocated-but-untracked billable runtime is reattached rather than
  orphaned (upstream #263).
- Assign `503` now maps to a typed `capacity_unavailable` error with actionable
  text and a stable reason code instead of generic retry noise (upstream
  #282/#273/#236).
- `colab status` shows the server-assigned machine shape instead of the stale
  locally requested shape (upstream #47).
- Routine OS-keyring fallback no longer prints on stderr for ordinary commands.
  `tieredStore.notice` still records a redacted `oauth2: keyring tier
  unavailable` debug log entry, but the `[colab] OS keyring unavailable; using
  owner-only token file …` hint is now shown only when debug logging is enabled
  (`COLAB_DEBUG=1`). `golab audit` remains the authoritative report of the
  token-store tier and file mode. Genuine auth failures (corrupt cached token,
  refresh failure, save failure) are unchanged and still use
  `oauth2Provider.notice` in the default configuration. No release, tag or live
  test is claimed for this fix.

## [0.1.9] - 2026-11-24

### Fixed

- Release workflow: the post-release CycloneDX predicate guard now reads the
  DSSE envelope from either layout —
  `(.dsseEnvelope.payload // .content.dsseEnvelope.payload) | @base64d |
  fromjson | .predicateType == $pt` — instead of only
  `.content.dsseEnvelope.payload`. cosign v2.6.5's `--new-bundle-format`
  `attest-blob` writes `dsseEnvelope` at the **root** of a v0.3 bundle, so the
  v0.1.8 release run `45428451505` failed at this guard with
  `jq: … Malformed BOM` even though the published attestation was correct
  (confirmed by decoding the downloaded
  `golab_0.1.8_linux_amd64.tar.gz.sbom.sigstore.json`). `--arg pt`, the
  assertion and the loud FAIL message are unchanged; no check was weakened.
  v0.1.8 is **not** a green release: published assets are immutable, so a new
  tag is required. A working self-update is not claimed — a separate critical
  install-path defect is being fixed by another agent (see
  [CONFORMANCE.md §4](docs/CONFORMANCE.md#4-release-pipeline-and-updater-identity)).

### Verification

- Real downloaded v0.1.8 SBOM bundle: the exact guard expression exits 0 and
  prints `true`; a synthetic nested-layout bundle prints `true`; synthetic
  root- and nested-layout bundles with the wrong predicate both exit 1 and
  print `false`; the old guard reproduces `Malformed BOM` on the real bundle.
  PyYAML parse and `bash -n` over all workflow `run:` blocks,
  `bash scripts/check-redaction.sh` and `bash scripts/check-paths.sh` pass. No
  live signed release or `golab update --install` proof.

## [0.1.8] - 2026-11-24

### Fixed

- Release workflow: `cosign attest-blob` now uses the exact predicate URI
  `--type https://cyclonedx.org/schema/bom-1.5` instead of the `cyclonedx`
  alias, and the post-release proof verifies with that same URI plus a `jq`
  guard on the DSSE payload's `.predicateType`. v0.1.7 was signed (release run
  `45414858634` green, correct v0.3 bundles and passing cosign signatures), but
  `colab update --install` from a v0.1.6-labelled binary failed with `SBOM
  attestation has an unsupported predicate type`: the alias emits
  `https://cyclonedx.org/bom`, which the updater rejects. cosign copies a URI
  verbatim, and this URI is already accepted by the unmodified verifier in
  shipped v0.1.6/v0.1.7 binaries, so either can install the next release; no
  verifier change was needed. Published assets are immutable, so a new tag was
  required; v0.1.9 supplied the live proof recorded below.
- Docs: record the v0.1.7 SBOM predicate failure and the exact predicate-URI
  requirement in `docs/CONFORMANCE.md` §4/§6, `docs/FAQ.md` and
  `docs/RELEASING.md`.

### Verification

PyYAML parse and `bash -n` of the release workflow; local synthetic DSSE-bundle
check of the jq predicate guard (positive and wrong-predicate negative);
`bash scripts/check-redaction.sh` and `bash scripts/check-paths.sh`.

**Live proof (2026-11-24):** v0.1.9 release run `45446096192` is green with all
guards (13 archives, 13 signatures, 13 SBOM attestations, checksums). A
v0.1.8-labelled binary ran `golab update --install --yes` against the real
release: it discovered v0.1.9, verified signature + SBOM, extracted the binary
from the signed archive and installed an executable ELF reporting
`golab 0.1.9`; `golab doctor` also runs. The global `/usr/local/bin/golab` was
rebuilt from this commit and reports 0.1.9. A temporary `/etc/hosts` pin for
`github.com` was needed on this host because its Termux resolver selected an
unreachable address; the file was restored immediately afterward.

## [0.1.7] - 2026-11-24

### Fixed

- `whoami`: stop discarding the OAuth transport when extracting the access
  token. The previous code replaced `client.Transport` with a capture
  RoundTripper, so no bearer header was attached and `whoami` always failed with
  `credentials have no access token after refresh` even with valid credentials.
  Verified by `TestAccessTokenFromAuthCapturesBearer` (red before, green after);
  no token is logged or included in errors.
- Release workflow: add `--new-bundle-format` to both `cosign sign-blob` and
  `cosign attest-blob` so published bundles are sigstore v0.3 protobuf bundles,
  and add a `jq` post-release guard asserting `.mediaType` contains `v0.3` and
  a tlog `inclusionProof` exists on every bundle. v0.1.6's legacy bundles passed
  the cosign proof but made `golab update --install` fail with
  `unknown field "base64Signature"`; cosign's verifiers accept both formats, so
  the format guard is what prevents a silent regression. The verifier is
  unchanged. Published assets are immutable, so a **new tag** is required to
  prove the updater end-to-end; v0.1.6 cannot be repaired in place. No release
  was cut or tagged in this change.
- Docs: correct the verification record — v0.1.6 was signed and cosign-verified
  but its updater was broken (legacy bundle format), and Fulcio SCT checking is
  **not** an open gap (`internal/update/verify.go:157-163` enforces it).
  Updated `docs/CONFORMANCE.md`, `docs/FAQ.md`, `docs/RELEASING.md`,
  `docs/03-go-architecture.md`, `docs/AUDIT-02-protocol-security.md` S-8 and
  `docs/08-risks-decisions-security.md`.

## [0.1.6] - 2026-11-24

### Fixed

- Self-update: `UpdateConfig` no longer embeds the running version in the
  release asset pattern, so a newer GitHub release can actually be offered.
  Version gating stays with `IsNewer`; checksum, signature and SBOM checks are
  unchanged. Verified by `TestUpdateConfigOffersNewerRelease` (red before, green
  after) plus `go vet ./...` and `go test -count=1 ./...`; a live signed-tag
  `colab update` is still human-pending.
- Release workflow: pin syft to v1.57.2 and drop the unsupported `scan`
  subcommand so the signed CycloneDX SBOM contains the Go dependencies
  (v0.42.2 could not parse `go 1.27.4` and produced an empty SBOM).
- CI: force `bash` for the matrix `go test` step so `TEST_FLAGS` expands on
  Windows runners (PowerShell silently dropped `-race`/`-count=1`).
- Docs: correct stale identity and per-platform bundle claims; the updater pin
  matches this repository and bundle selection is implemented.
- Release workflow: the post-release proof now verifies every archive signature
  and every CycloneDX SBOM attestation against its own archive, and checks every
  `checksums.txt` entry (it previously sampled one signature bundle and checked
  neither SBOMs nor checksums).
- Release: v0.1.6 signed release verified (workflow run `45399682610` green) —
  13 archives, 13 signature bundles, 13 CycloneDX attestation bundles and
  `checksums.txt`; a sampled attestation contains 1,627 components including the
  Go modules. The read-only `golab update` check succeeds.
- Release workflow: pin GoReleaser to v2.20.3 instead of floating `~> v2`, assert
  `GITHUB_REPOSITORY` matches the updater's `20Youssef10/golab` pin, and gate the
  release job behind the `release` environment (admins must add required
  reviewers for that gate to take effect).
- Release workflow: pass the syft command through an env var instead of
  interpolating it into `run:`, and drop `--clobber` from `gh release upload` so
  re-runs cannot overwrite published signatures.
- CI: extend `check-redaction.sh` to scan YAML/shell/JSON as well as Go for
  obvious credential literals.

## [0.1.5] - 2026-11-23

### Fixed

- Release workflow: generate the CycloneDX SBOM with the syft CLI directly
  instead of sbom-action (whose pinned version does not support `output-file`
  and whose artifact upload was rejected).

### Verification

YAML parse; CI green on v0.1.4.

## [0.1.4] - 2026-11-23

### Fixed

- Release workflow: give sbom-action an explicit artifact name; the default
  contains a dot and is rejected by GitHub's artifact service.

### Verification

YAML parse, local `go test`; CI green on v0.1.3.

## [0.1.3] - 2026-11-23

### Fixed

- Last Windows-only POSIX mode assertion in the token-store test.
- Release workflow: disable sbom-action's artifact upload (its default name
  contains a dot and is rejected); the SBOM file is still produced for cosign.

### Verification

Local `go test`, YAML parse, `goreleaser check`.

## [0.1.2] - 2026-11-23

### Fixed

- Windows CI: skip POSIX directory-mode assertions, avoid blocking on `CONIN$`
  when stdin is not a TTY (Drive consent fixture), and disable `-race` on
  windows/arm64 where the Go race detector is unavailable.
- Release workflow: pass `GITHUB_TOKEN` to GoReleaser so publishing can proceed.

### Verification

Local `go test`, Windows test-binary compile, YAML parse, `goreleaser check`.

## [0.1.1] - 2026-11-23

### Fixed

- Release workflow's annotated-tag check: explicitly fetch tag refs before
  inspecting them (checkout leaves a detached HEAD).
- CI on Windows: tests no longer assert POSIX `0600` file modes where Windows
  reports `0666` (ACLs are the real control), skip SIGHUP and POSIX-shell
  editor fixtures, and avoid `.bak` hard-link assertions.
- Two stdin-feedback test fixtures that could drop an `input_reply` when the
  CLI closed the kernel under coverage instrumentation; bounded the runtime
  stdin prompt wait with a timeout.

### Verification

Local `go test -race ./...`, `-covermode=atomic ./...`, Windows test-binary
cross-compilation and `golangci-lint` all pass.

## [0.1.0] - 2026-11-23

First feature-complete release of the Go port against live upstream HEAD
`0abf82d` (v0.8.0 + 5 commits). Closes the code-fixable findings of
`docs/AUDIT-00` … `docs/AUDIT-04`; see `docs/REMEDIATION.md` for the per-finding
statuses and the remaining human-only checks.

### Added

- Full Colab **Public API lifecycle** (LRO create/poll/delete, `runtime_name`
  persisted in session state, operation-code mapping) ahead of upstream, which
  currently only lists runtimes via the Public API.
- `ssh --force` (upstream `cda02bc`) to kill stale sshd children before dialling.
- `logout` (clears keyring/legacy token, invalidates cached proxy tokens and
  capability flags) and recursive `upload/download -r` with traversal/symlink
  guards (upstream PRs #266/#267).
- New Go-only commands: `doctor` (GET-only redacting probe), `audit` (credential
  and update-verification status), `budget` (advisory compute-unit guard),
  `bundle` (reproducible provenance), `sync`, and `agentout`.
- Structured output `--output text|json|ndjson` with the stable `golab/1`
  schema, `--explain` dry-run plans, and `--yes` confirmation for `stop --all`.
- GoReleaser config, cosign + signed-SBOM release workflow, live-e2e skeleton
  with kill switch, QEMU/Termux skeletons, conformance scripts and
  `docs/CONFORMANCE.md`.
- Env-var support: `COLAB_CLI_REQUEST_TIMEOUT`, `COLAB_CLI_ASSIGN_RETRIES`,
  `COLAB_CLI_RETRY_BACKOFF`, `COLAB_OUTPUT`, `COLAB_ASSUME_YES`,
  `GOLAB_BUDGET_FILE`.

### Changed

- Module path is the canonical `github.com/20Youssef10/golab`, enabling
  `go install …@latest` (audit P0-8). CLI and on-disk schema are unchanged.
- One honest client identity `golab/<version>` across control, data and SSH
  planes (audit S-3).
- Token storage keeps only the refresh token + metadata; access tokens are
  minted on demand (audit S-4).
- Small uploads use one atomic `chunk:1` PUT; large uploads report
  `ErrIncompleteUpload` and verify final size (audit S-2).
- `url --open` and `pay`/Drive consent URLs share one cross-platform launcher
  with Termux/WSL support.
- GitHub Actions pinned to commit SHAs; CI adds lint, fuzz, coverage,
  cross-build smoke and safety-script gates.

### Fixed

- `url --open` no-op (audit P0-2); Drive propagation failures echoing raw HTML
  (P0-7); `make cross` writing to a session-local path (P0-4);
  `update.CleanupOld` never called (P0-5); updater identity/asset patterns that
  would reject our own releases (P0-6).
- Redaction covers `id_token`, passwords, secrets and code verifiers; REPL
  sanitiser keeps SGR only; cosign verification requires signed certificate
  timestamps; updater matches signature/SBOM bundles per platform archive.
- All Python defects D-1…D-11 remain fixed; no regressions found.
- Test-fixture race in `TestAuthFlow` made deterministic.

### Verification

`go vet ./...`, `golangci-lint run` (0 issues), `go test -race -count=1 ./...`,
11 `CGO_ENABLED=0` cross-builds, `govulncheck`, `go mod verify`, redaction and
remote-path scans. **Live Colab e2e is still human-gated** per AGENTS.md.

## [0.1.0-alpha.1] - 2026-11-23

- Audit baseline: initial Go port, audit reports AUDIT-00…04, canonical module path.

[Unreleased]: https://github.com/20Youssef10/golab/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/20Youssef10/golab/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/20Youssef10/golab/compare/v0.1.9...v0.2.0
[0.1.9]: https://github.com/20Youssef10/golab/compare/v0.1.8...v0.1.9
[0.1.8]: https://github.com/20Youssef10/golab/compare/v0.1.7...v0.1.8
[0.1.7]: https://github.com/20Youssef10/golab/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/20Youssef10/golab/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/20Youssef10/golab/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/20Youssef10/golab/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/20Youssef10/golab/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/20Youssef10/golab/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/20Youssef10/golab/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/20Youssef10/golab/compare/v0.1.0-alpha.1...v0.1.0
[0.1.0-alpha.1]: https://github.com/20Youssef10/golab/releases/tag/v0.1.0-alpha.1
