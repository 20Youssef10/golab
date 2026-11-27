<!-- Generated from docs/INDEX.md §Project in one paragraph by docs-site/tools/sync-sources.py. Do not edit. -->

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

**Compatibility target:** `googlecolab/google-colab-cli` **0.8.x+HEAD**, re-checked
against upstream HEAD `0abf82d` (v0.8.0 + 5 commits) on 2026-11-23.
Module path: `github.com/20Youssef10/golab`; binary: `golab`; Go 1.27.4 with
`CGO_ENABLED=0`. See [COMPAT.md](https://github.com/20Youssef10/golab/blob/main/docs/COMPAT.md) for the exact drift status and
[CHANGELOG.md](https://github.com/20Youssef10/golab/blob/main/CHANGELOG.md) for what shipped in v0.3.0.

> ⚠️ **Proof status — read before trusting any "done" claim.** A billable CPU-VM
> live e2e *has* been run for v0.2.0 (v0.2.0 Verification in
> [CHANGELOG.md](https://github.com/20Youssef10/golab/blob/main/CHANGELOG.md)): `new`, `status`, `ls`, `exec`
> (text/JSON/secrets), `run` (script and notebook), `ssh`, piped `repl`,
> upload/download, `install`, `restart-kernel`, `log`, `stop --verify` and
> `resume` error paths, ending with an empty `golab sessions`. Release run
> `45489842618` published 13 archives with signatures and SBOMs, and a signed
> `v0.1.9` → `v0.2.0` self-update was proven. The full CI matrix is green,
> `golangci-lint` reports 0 issues and `govulncheck` is clean; across 664
> test/fuzz functions in 103 `_test.go` files (25 packages, 23 with tests),
> total statement coverage is 75.8% — above the 70% CI floor, short of the 80%
> target. This is one machine class, not blanket certification: OAuth
> re-consent, Drive browser approval, interactive-TTY `repl`/`ssh`, real
> Windows/Termux devices and induced WS-drop recovery remain human-only, and
> wire details still marked 🔴 in [02-protocol-spec.md](https://github.com/20Youssef10/golab/blob/main/docs/02-protocol-spec.md) §7
> and the checklist in [**AUDIT-INDEX.md §5**](https://github.com/20Youssef10/golab/blob/main/docs/AUDIT-INDEX.md) are *not* proved
> by fake-server tests. [CONFORMANCE.md](https://github.com/20Youssef10/golab/blob/main/docs/CONFORMANCE.md) is the per-layer ledger
> but is still dated 2026-11-23 and does not yet record this live pass.
