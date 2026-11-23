# Audit index — golab vs live google-colab-cli (2026-11-23)

Five structured reports produced by reviewing our Go source, our design docs and
the live upstream project (HEAD `0abf82d`, PyPI `0.8.0`). **No implementation
code was changed by this audit**; the only edit outside the reports is a
correction to `docs/COMPAT.md` (`url --open` was documented as working when it
is a no-op).

| Report | Covers |
|---|---|
| [AUDIT-00-live-upstream.md](AUDIT-00-live-upstream.md) | Live snapshot: HEAD/tag/PyPI, commits since v0.8.0, open issues/PRs (#269, #272, #267, #266, #270, #273), the real Public API migration plan, doc drift. |
| [AUDIT-01-parity.md](AUDIT-01-parity.md) | Command/flag/env/message/exit-code parity, exact mismatches, upstream tests we don't cover, P0/P1/P2 backlog. |
| [AUDIT-02-protocol-security.md](AUDIT-02-protocol-security.md) | Wire fidelity, the 8 🔴 unknowns, security findings, D-1…D-11 verification, human-required probes. |
| [AUDIT-03-platform-testing.md](AUDIT-03-platform-testing.md) | Platform matrix truth, test-layer coverage (L1–L7), release/CI gaps, P0/P1/P2 backlog. |
| [AUDIT-04-surpass.md](AUDIT-04-surpass.md) | Where we already lead, 12 ranked "surpass upstream" proposals with designs, tests and metrics, roadmap gates, "do NOT do" list. |

## 1. Headline verdict

**Functional parity is high (~89% of commands/flags), but the port is not
release-ready.** The biggest problems are not missing commands; they are
(a) unverified live behaviour, (b) a broken release/CI story, and (c) a handful
of concrete P0 defects that would be visible to users immediately.

Strengths confirmed by source review: all D-1…D-11 Python defects are fixed;
typed secrets, redaction, atomic state, explicit timeouts, embedded SSH and the
terminal sanitiser are real; lint is clean (0 issues), `go test -race ./...`
passes, 11 cross-builds pass, `govulncheck` is clean.

## 2. Consolidated P0 backlog (do these first)

| ID | Finding | Evidence | Fix |
|---|---|---|---|
| **P0-1** | `ssh --force` missing while our own 429 messages tell users to use it | upstream `cda02bc`; `internal/sshx/errors.go:100-111`; no `--force` flag in `internal/cli/ssh.go` | Add `--force`; kill stale sshd via kernel exactly like upstream, with a safe non-spliced snippet; port `test_ssh.py --force` cases. See AUDIT-01. |
| **P0-2** | `url --open` is a no-op | `internal/cli/utility.go:261-265` | Wire the existing in-tree launcher (used by `pay`) into `--open`; add a stdout-is-clean test. |
| **P0-3** | Chunked upload may truncate a remote file if Colab accepts but ignores `chunk:` | `internal/contents/contents.go:197-232`; docs/02 §3.8 🔴 S6 | **HUMAN REQUIRED:** live upload + SHA-256 check. Until proven, add a typed incomplete-upload error and/or single-shot-by-default for small files. See AUDIT-02 S-2. |
| **P0-4** | `make cross` writes to a session-local `/tmp/opencode` path; CI cross-build job fails on a clean runner | `Makefile:24-27`, `.github/workflows/ci.yml:33` | Write to `$GITHUB_WORKSPACE/dist` / mktemp; add a CI smoke test of the target. See AUDIT-03. |
| **P0-5** | `update.CleanupOld` never called → Windows `.old` files accumulate | `internal/update/install.go:113`; no callers outside tests | Call it at startup from `cmd/golab/main.go` or `app.New`. |
| **P0-6** | Release pipeline cannot produce the artefacts our updater requires | `internal/update/verify.go:127` pins `release.yml@…`; `check.go:139` requires a signed SBOM; no `.goreleaser.yaml`, no `release.yml` | Add GoReleaser + release workflow publishing checksums, cosign bundle and `*.sbom.sigstore.json`; test verification against a fake release. |
| **P0-7** | Drive-failure path can echo raw HTML from `StatusError.Body` | `internal/cli/automation.go` `reportDriveFailure`; upstream `aa01a39`, test `test_drivefs_hook_reports_400_without_raw_html` | Strip to status + reason; port the upstream test. |
| **P0-8** | Module path `golab` makes `go install …@latest` impossible; project identity still unresolved | `go.mod:1`; DEC-01 | Decide and document the canonical install identity; add a `go install`-compatible module path or drop that channel from docs/04. |

## 3. P1 themes

* **Conformance:** `testdata/wire/` and `testdata/golden/` are empty; no L3 golden
  outputs, L4 differential proxy, or L5 live e2e. ~55–65% of upstream's ~373
  tests ported; `docs/CONFORMANCE.md` missing. (AUDIT-03)
* **Protocol proof:** all 8 🔴 items of docs/02 §7 remain unconfirmed; the
  GET-only capture plan in AUDIT-02 is the safe path. Nothing is "done" until a
  human runs it.
* **Identity leakage:** two client-agent values (`golab/<ver>` vs
  `colab-go/<ver>` in SSH); allow-listing unverified (R-03). (AUDIT-02 S-3)
* **Credential storage:** our token file keeps access token + client secret;
  AGENTS.md mandates refresh-token-only. (AUDIT-02 S-4)
* **Missing user features:** recursive upload/download (#267), `logout` (#266),
  and the complete Public API LRO lifecycle from upstream's own migration plan.
  (AUDIT-00, AUDIT-04)
* **Supply chain:** floating GitHub Action refs, no lint/fuzz/goleak/coverage/
  reproducible-build/license gates in CI; both shell safety scripts are unwired.
  (AUDIT-03, AUDIT-02 S-9)

## 4. Where we already beat upstream (keep these advantages)

Static single binary with no Python/OpenSSH; embedded SSH; atomic state with
loud corruption failure; typed errors + explicit timeouts; typed secret
redaction with logging off by default; sigstore keyless update verification with
mandatory signed SBOM; terminal escape sanitiser; Windows and Termux support;
5 s keyring fallback; safe `edit` and chunked upload; clean stdout for piping.
Note: our `runtime.StdinHook` sends `input_reply` and extracts `content.prompt`,
so we **avoid** the upstream `colab auth` hang reported in issue #269 — verify
with a live human run before claiming it publicly.

Our Public API support is currently at **parity**, not ahead (list + probe only
in both); the opportunity is to implement the full lifecycle first.

## 5. Human-required verification checklist (blocking final "done")

1. Live `auth` / OAuth consent; interactive `repl` and `ssh`; `drivemount`.
2. Live chunked upload of >75 MiB with SHA-256 comparison (S6 / P0-3).
3. GET-only wire capture for the 8 🔴 items; replay against fakes; check
   `X-Colab-Client-Agent` allow-listing and reduced header sets.
4. One CPU `run` end-to-end, then `colab sessions` must show nothing (unassign).
5. Signed release + SBOM verification from a real tag; Windows `.old` cleanup.
6. Termux device (DNS/CA/faccessat2) and Windows console/TTY matrix.
7. Proxy environment (`HTTP_PROXY`) body-less POST behaviour (upstream #270).

## 6. Suggested sequencing

```
P0 fixes (above)  →  conformance harness + wire fixtures  →  human live e2e
  →  release pipeline + channels  →  missing user features (logout, -r,
     full Public API lifecycle)  →  AUDIT-04 innovations (JSON output,
     doctor, audit, budgets, bundles)  →  MCP/SDK only after v1.0.
```

Each item's test-first steps, affected files and S/M/L effort are in the
per-topic reports; use those as the execution backlog rather than this summary.
