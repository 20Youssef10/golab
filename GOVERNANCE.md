# Governance

golab is a community-maintained, unofficial Go port of Google's Colab CLI,
licensed under Apache-2.0. This document describes how decisions are made and
how releases are approved. It complements (and does not override)
[`AGENTS.md`](AGENTS.md), the contributor rules.

## Roles

### Maintainers

Maintainers have commit and release rights. They:

- review and merge pull requests, and enforce the test-first, docs-update and
  security rules in `AGENTS.md`;
- own the ADRs in `docs/03-go-architecture.md` and the decision (DEC) log in
  `docs/08-risks-decisions-security.md`;
- approve breaking changes and releases, and handle security reports privately
  (see [SECURITY.md](SECURITY.md));
- keep the upstream-sync loop (below) running.

Current maintainer: **[@20Youssef10](https://github.com/20Youssef10)** (see
[`CODEOWNERS`](.github/CODEOWNERS)). New maintainers are added by existing
maintainers based on sustained, trusted contribution.

### Contributors

Anyone who opens an issue, improves docs, writes a test or sends a PR is a
contributor. Contributors propose changes through PRs, follow the
[contributor guide](CONTRIBUTING.md), and are credited in the CHANGELOG. There
is no paid support or employment relationship; participation is voluntary. All
participants are covered by the [Code of Conduct](CODE_OF_CONDUCT.md).

## Decision-making

- **Architecture Decision Records (ADRs)** live in
  `docs/03-go-architecture.md` (ADR-001 … ADR-014). An ADR records context,
  decision and consequences; it is the canonical answer to "why is it this way?".
- **Owner-input decisions** (DEC-01 … DEC-11) live in the decision log in
  `docs/08-risks-decisions-security.md` §2. These are the open questions where
  maintainer/owner input is still needed.
- **Breaking changes require owner/maintainer input** before implementation:
  any change to the CLI surface, flag semantics, on-disk schema or persisted
  token format (the SemVer definition below) must be discussed in an issue or
  ADR and explicitly approved.
- Day-to-day decisions (bug fixes, tests, refactors that preserve behaviour) are
  made by PR review. For review, use the full cumulative diff
  `git diff main..<branch>`.

### Proposing an ADR

1. Open an issue describing the problem, constraints and options, or go straight
   to a PR if the discussion is already settled.
2. Add a new numbered section to `docs/03-go-architecture.md` with:
   **Status** (proposed | accepted | superseded-by ADR-XXX), **Context**,
   **Decision**, **Alternatives rejected**, **Consequences**, and **Revisit if**.
3. Link any related DEC entry in `docs/08` and update it when the decision is
   made. Add a dated note to the doc's changelog/frontmatter as required by
   `AGENTS.md`.
4. A maintainer accepts or rejects the ADR in review. Accepted ADRs are binding
   until superseded by a later ADR.

## Versioning (SemVer)

This project follows [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).
While pre-v1.0, the `0.x` rules apply (minor bumps may be breaking).

Per `AGENTS.md` and the CHANGELOG, a **breaking change** is any change to:

- the **CLI surface** (command names, removal of commands, positional arguments);
- **flag semantics** (a flag's meaning, defaults, or exit-code behaviour);
- the **on-disk schema** (`sessions.json`, `settings.json`, history files);
- the **persisted token format** (keyring entry or fallback token file).

Bug fixes and additive, backward-compatible features are patch/minor bumps
respectively. Breaking changes require a minor bump in `0.x` (major after v1.0),
an explicit note in the CHANGELOG, and prior maintainer approval.

## Release approval flow

Releases happen **only when the user/maintainer explicitly asks** — agents must
never cut a release on their own. The flow is:

1. **Changelog PR** — move `[Unreleased]` items into a dated version section in
   `CHANGELOG.md`; update `internal/version`; confirm the inferred version with
   the requester before tagging.
2. **Merge** the changelog PR into `main` (CI green, human-only e2e items in
   `docs/REMEDIATION.md` closed or explicitly waived by a maintainer).
3. **Annotated tag** `vX.Y.Z` on the merge commit: `git tag -a vX.Y.Z -m "..."`,
   then `git push origin vX.Y.Z`.
4. **GoReleaser** runs via the tag-triggered
   [`.github/workflows/release.yml`](.github/workflows/release.yml), producing
   archives, checksums, cosign signatures and signed SBOM attestations.

The full operational runbook, including required artifacts, identity pin and
rollback guidance, is in [`docs/RELEASING.md`](docs/RELEASING.md).

## Upstream sync

Upstream (`googlecolab/google-colab-cli`) changes weekly, so keeping parity is a
permanent process, not a one-time task. Per `docs/06-roadmap.md` §5:

1. **Watch** upstream Releases/Tags and `CHANGELOG.md` — a weekly cron opens an
   issue with the diff.
2. **Triage** each change as *protocol* (live-capture and update wire fixtures),
   *CLI surface* (update `docs/05` + tests), *docs-only*, or *irrelevant*.
3. **Compat declaration** — `golab version` prints the upstream target and
   `docs/COMPAT.md` lists known divergences.
4. **Contract tests** — golden wire fixtures are re-captured from the latest
   Python release **monthly**; CI fails if fakes and fixtures diverge.
5. **Breakage detection** — a nightly **read-only** live probe (`assignments`,
   `usage`) with a throw-away account opens a priority issue on failure.
6. **Contribute back** — non-Go-specific fixes (e.g. Windows/arm64) may be
   offered upstream.

Any upstream change that touches the CLI surface or on-disk schema is a
candidate breaking change and goes through the approval rules above.
