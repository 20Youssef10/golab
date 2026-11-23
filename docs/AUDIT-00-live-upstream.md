# Audit 00 · Live upstream snapshot (2026-11-23)

> Scope: the **live** `googlecolab/google-colab-cli` repository and PyPI at the
> time of this audit. All findings are dated; re-check before acting.
> This file is evidence for `AUDIT-01`…`AUDIT-04`, not a plan of its own.

## 1. Snapshot metadata

| Source | Value | How verified |
|---|---|---|
| GitHub HEAD | `0abf82dadc2b95c02b6ab2c08b1b4d664e616fbb` | `git ls-remote … HEAD` |
| Latest tag | `v0.8.0` → `6045e34cd9b50319ab92e38e200ceaf411d8b96c` | `git ls-remote … refs/tags/*` |
| PyPI latest | `google-colab-cli 0.8.0`, uploaded 2026-11-10 | `https://pypi.org/pypi/google-colab-cli/json` |
| Local source clone | `/tmp/opencode/google-colab-cli`, HEAD `0abf82d`, re-fetched from `main` | `git fetch origin main` |
| GitHub Releases API | empty array (no published GitHub Releases) | `api.github.com/repos/…/releases` |
| Upstream test suite (as of PR #272) | 600 passed, 19 skipped (their claim) | PR #272 description |
| Upstream license | Apache-2.0 | PyPI metadata |

**Conclusion:** our port targets the correct live code state. No new upstream
commit landed during this audit. The 5 commits after `v0.8.0` are already
accounted for in `docs/COMPAT.md` (public API flag, `stop --all`, SSH `--forced`,
drivemount propagation fix, partial scopes).

## 2. Upstream commits since v0.8.0 (`6045e34..0abf82d`)

| Commit | Change | Relevance to our port |
|---|---|---|
| `02d2321` | auth scopes documentation | Already covered (docs/02 §1). |
| `274b77f` | `colab stop --all` | Implemented. |
| `602462f` | `--public-api/--no-public-api`, `test-public-api`, public list path | Implemented (list only — see §4). |
| `cda02bc` | **`--forced` to kill stale SSH sessions** | Audit whether we ported it; our CLI agent reported it was not ported. |
| `33610a0` | annotate imports after `install` | Minor behavioural detail; check our `install`. |
| `aa01a39` | drivemount: report propagation failures instead of dumping HTML | Check our error path. |
| `0abf82d` | build cleanup | No functional impact. |

## 3. Live issues and pull requests that expose upstream gaps

These are *open* at audit time; they are both a parity target and an
opportunity to beat upstream.

| ID | Title / symptom | Implication for `golab` |
|---|---|---|
| [#269](https://github.com/googlecolab/google-colab-cli/issues/269) | `colab auth` hangs: `execute_interactive` discards the `stdin_hook` return value, so the kernel's `input()` never receives `input_reply`. Also prints a raw message dict instead of `content.prompt`. | **Upstream is broken** for `auth` and any `input()`-driven automation. Our `runtime.StdinHook` sends `input_reply` explicitly (per docs/02 §3.6) — verify with a fake-kernel test that we do not repeat the bug. This is a "surpass" opportunity if proven. |
| [#272](https://github.com/googlecolab/google-colab-cli/pull/272) | Chunked upload for files > ~75 MiB; 100 MiB request-body limit. Adds `refresh_session` per chunk and `IncompleteUploadError`. | We already stream chunks; verify our chunk size/refresh semantics and whether we surface a typed incomplete-upload error. |
| [#267](https://github.com/googlecolab/google-colab-cli/pull/267) | `upload/download -r` recursive directory trees, symlink/traversal guards, persistent HTTP session. | **Missing in our port** — high-value feature; also a security checklist (traversal, symlinks, depth limit). |
| [#266](https://github.com/googlecolab/google-colab-cli/pull/266) | `colab logout`: clear keyring token, invalidate cached proxy tokens, reset capability flags. | **Missing in our port**; important for multi-account users and for the R-02 OAuth-client risk. |
| [#270](https://github.com/googlecolab/google-colab-cli/issues/270) | Body-less POSTs get HTTP 411 behind forward proxies; chunked body works. | Our Go `net/http` sends `Content-Length: 0` for POST; check whether we hit the same failure in proxied CI/sandbox environments and design a safe workaround. |
| [#273](https://github.com/googlecolab/google-colab-cli/issues/273) | Paid accounts: G4/A100/L4 and `--high-mem` return 503 / ReadTimeout while T4/CPU work. | Upstream bug/quota behaviour; our retry+reconcile logic may already be more robust, but our friendly-error mapping should be checked against it. |
| [#257](https://github.com/googlecolab/google-colab-cli/discussions/257) | User request behind PR #267 (recursive transfer). | Confirms demand. |

## 4. The Public API migration is bigger than upstream's current implementation

The live file `docs/public_api_migration_plan.md` (fetched from `main`) describes
a **dual-support migration** that is only partially implemented in HEAD.

Currently implemented upstream (and in our port):
* `--public-api` flag, `COLAB_CLI_PUBLIC_API`, `Settings.use_public_api`,
  `public_api_supported`/`tunnel_api_supported`, hidden `test-public-api`.
* `GET /v1beta/runtimes` mapped into `ListedAssignment` only.

Still **planned, not implemented** upstream:
* Full create path via LRO: `POST /v1beta/runtimes?runtimeId=r-<uuid>&requestId=<uuid>`
  → poll `GET /v1/{op}:wait` until `done`, map `Operation.error` codes
  (9 → too many assignments, 6 → already exists, 3/7 → 400/403).
* Full delete path: `DELETE /v1beta/runtimes/{runtime_id}?allowMissing=true`.
  Critical identity distinction: the Public API uses **`runtime.name`**
  (`runtimes/r-…`, i.e. `notebook_id_hash`), **not** the tunnel `endpoint`
  (`m-s-…`). Upstream plans a new persisted field `runtime_name`.
* Token refresh matched by `runtime_name` or endpoint.
* Capability-aware auth hints on 401/403 using the cached probe flags.

This is the single largest "surpass upstream" opportunity: our `colabapi`
already has a dual-path abstraction, so implementing the complete Public API
lifecycle would put us ahead of the official CLI. It is also a moving target —
the plan explicitly notes generated OpenAPI stubs in an untracked `src/openapi/`
directory, so the final shape may change.

## 5. Documentation drift observed live

* The PyPI/README says `--auth` defaults to `adc`; the code default is `oauth2`
  (docs/01 §9 already recorded this drift). Our port follows the code.
* README still says "Linux and macOS only; Windows is not supported" — our
  port deliberately supports Windows and Termux.
* README still describes `colab ssh` as needing a local `ssh` client and key;
  our embedded client removes that dependency.
* Upstream README links to a separate **Colab MCP Server**
  (`github.com/googlecolab/colab-mcp`) for notebook/agent workflows — relevant
  when evaluating an MCP/agent-facing feature for our port (do not duplicate
  blindly).
* Changelog shows repeated broken/yanked releases (0.7.0/0.7.3 metadata and
  pin issues) — reinforces the need for our signed, verified release pipeline.

## 6. What this snapshot means for the audit

1. Parity work must include the post-v0.8.0 commits, especially SSH `--forced`.
2. The upstream `auth` bug (#269) must be explicitly tested against our code —
   if we are correct, it is a headline advantage.
3. Recursive transfer, logout, and full Public API lifecycle are concrete,
   evidence-backed missing features (not speculative ideas).
4. Live verification is still required for: proxy 411 behaviour, high-VRAM
   allocation failures, chunked upload limits, full Public API create/delete,
   and all human-only auth/TTY flows.

## 7. Sources

* `https://github.com/googlecolab/google-colab-cli` (clone at `0abf82d`, re-fetched 2026-11-23)
* `https://pypi.org/pypi/google-colab-cli/json` (version 0.8.0 metadata)
* `https://api.github.com/repos/googlecolab/google-colab-cli/releases` (empty)
* `https://api.github.com/repos/googlecolab/google-colab-cli/issues?state=open&per_page=40`
* `https://raw.githubusercontent.com/googlecolab/google-colab-cli/main/docs/public_api_migration_plan.md`
* Upstream `CHANGELOG.md`, `README.md`, `AGENTS.md`, `docs/*` in the local clone.
