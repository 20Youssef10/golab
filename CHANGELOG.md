# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Breaking changes are defined as changes to the CLI surface, flag semantics,
on-disk schema or persisted token format (per AGENTS.md).

## [Unreleased]

## [0.1.0-alpha.1] - 2026-11-23

### Added

- Initial Go port of `google-colab-cli` at upstream HEAD `0abf82d` (v0.8.0 + 5
  commits): all commands, dual tunnel/Public API control plane, Jupyter kernel
  client, embedded SSH, Contents API, auth providers, history/notebooks,
  rendering, REPL and signed self-update.
- Audit reports `docs/AUDIT-00` … `docs/AUDIT-04` and `docs/AUDIT-INDEX.md`,
  describing parity, security, platform, testing and "surpass upstream" findings.

### Changed

- Module path is now the canonical `github.com/20Youssef10/golab`, enabling the
  documented `go install …@latest` channel (fixes audit P0-8). This does not
  change the CLI surface or on-disk schema.

### Fixed

- Corrected `docs/COMPAT.md`: `url --open` was documented as working when it is
  currently a no-op (tracked as audit P0-2).

[Unreleased]: https://github.com/20Youssef10/golab/compare/v0.1.0-alpha.1...HEAD
[0.1.0-alpha.1]: https://github.com/20Youssef10/golab/releases/tag/v0.1.0-alpha.1
