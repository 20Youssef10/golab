# golab documentation index

> **Implementation status of this pack: 2026-11-23 (v0.1.0).** The notes added at
> the top of docs 01–04 and 07 record what the code actually does at this release.
> Where a design doc and the code disagree, **the code wins** (AGENTS.md).

## Project in one paragraph

`golab` is an unofficial, single-binary Go implementation of Google Colab's
command-line client. It rents a Colab VM (CPU/GPU/TPU), attaches to its Jupyter
kernel through the runtime proxy, runs code, moves files, opens SSH and releases
the VM. It keeps the Python CLI's commands, flags, exit codes and on-disk state
layout so users can switch without re-logging-in, while fixing the upstream
defects (unsafe state writes, code injection, missing timeouts, unbounded logs,
unsigned self-update) and adding Go-only commands (`doctor`, `audit`, `budget`,
`bundle`, `sync`, `agentout`). There is no daemon: each command authenticates,
does one job and exits.

**Compatibility target:** `googlecolab/google-colab-cli` **0.8.x**, re-checked
against live upstream HEAD `0abf82d` (v0.8.0 + 5 commits) on 2026-11-23.
Module path: `github.com/20Youssef10/golab`; binary: `golab`; Go 1.27.4 with
`CGO_ENABLED=0`. See [COMPAT.md](COMPAT.md) for the exact drift status and
[CHANGELOG.md](../CHANGELOG.md) for what shipped in v0.1.0.

> ⚠️ **Known unverified — read before trusting any "done" claim.** No live Colab
> e2e has been run. Wire details still marked 🔴 in
> [02-protocol-spec.md](02-protocol-spec.md) §7 and the human-only checklist in
> [**AUDIT-INDEX.md §5**](AUDIT-INDEX.md) are *not* proved by fake-server tests.
> The honest layer-by-layer ledger is [CONFORMANCE.md](CONFORMANCE.md).
> "Fakes are necessary, not sufficient" (AGENTS.md #3).

## Track 1 — Users (install, use, switch)

Written by another agent; linked here even while they are being drafted.

1. [README](../README.md) — what golab is, install and quick start.
2. [USAGE](USAGE.md) — command-by-command guide and common recipes.
3. [MIGRATION](MIGRATION.md) — switching from the Python `colab` CLI.
4. [FAQ](FAQ.md) — troubleshooting, platform notes, safety and quotas.

Optional follow-ups: [COMPAT.md](COMPAT.md) (exact upstream drift) and
[CHANGELOG.md](../CHANGELOG.md).

## Track 2 — Contributors (implement and verify)

1. [AGENTS.md](../AGENTS.md) — non-negotiable mandates, security and workflow.
2. [09-implementation-contracts.md](09-implementation-contracts.md) — package
   ownership and the API contract between packages.
3. [03-go-architecture.md](03-go-architecture.md) — layout and the ADRs.
4. [07-testing-and-conformance.md](07-testing-and-conformance.md) — test layers
   and how to port an upstream test.
5. [AUDIT-INDEX.md](AUDIT-INDEX.md) — the findings the code was remediated against.
6. [REMEDIATION.md](REMEDIATION.md) — v0.1.0 finding-by-finding status and the
   remaining human-only checks.

Companions: [CONFORMANCE.md](CONFORMANCE.md) (current test/coverage ledger),
[05-command-parity.md](05-command-parity.md) (command surface),
[06-roadmap.md](06-roadmap.md) (sequencing).

## Track 3 — Researchers / Protocol (ground truth)

1. [01-python-cli-analysis.md](01-python-cli-analysis.md) — how upstream works,
   its defects (D-1…D-11) and the contracts downstream code relies on.
2. [02-protocol-spec.md](02-protocol-spec.md) — reconstructed control-plane and
   data-plane wire spec, with 🟢/🟡/🔴 confidence markers.
3. [04-platform-matrix.md](04-platform-matrix.md) — OS/architecture, Termux and
   Windows realities, distribution channels.
4. [08-risks-decisions-security.md](08-risks-decisions-security.md) — the
   decision log and security rationale behind deliberate deviations.
5. [AUDIT-00-live-upstream.md](AUDIT-00-live-upstream.md) and
   [AUDIT-02-protocol-security.md](AUDIT-02-protocol-security.md) — the live
   upstream snapshot and the unresolved 🔴 wire/security probes.

Reminder: docs/01–08 are design documents. For what actually shipped at
v0.1.0, start with the dated notes at the top of docs 01–04 and 07, then
[COMPAT.md](COMPAT.md) and [CONFORMANCE.md](CONFORMANCE.md).
