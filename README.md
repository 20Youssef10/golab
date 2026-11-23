# Colab CLI → Go: Port Planning Pack

Unofficial analysis and plan for rewriting Google's official **`google-colab-cli`** (Python, v0.8.0+2, Apache-2.0) in **Go**, as one static binary for **Linux · macOS · Windows · Termux** on **amd64 · arm64 · armv7 · 386 · riscv64** and more.

## Verdict
**Feasible, worthwhile, and bounded: ≈ 45–60 focused working days to v1.0.** The Python tool is small (≈ 5.7k LOC). The cost is not code volume; it is (1) an **undocumented, moving protocol** and (2) **platform edge cases** (Termux networking, Windows console, keyrings). Both are de-risked by a one-week spike phase with a hard go/no-go.

Why Go pays off: no Python runtime, **no system `ssh`/`ssh-keygen` needed** (embedded SSH client), real Windows and Termux support (upstream says it has no Windows machines), faster start-up, trivial distribution.

## Ten findings that shaped the plan
1. Two planes, two credentials: control plane (`/tun/m/*`, user bearer) and data plane (runtime-proxy token).
2. The "kernel client" is a Colab fork of `jupyter-kernel-client`; **Go has no equivalent**, so ~600 LOC of Jupyter-over-WebSocket must be written (spec: doc 02).
3. Colab extends Jupyter (`colab_request` / `input_reply` + `colab_reply`) for Drive mounting.
4. Control-plane quirks: XSSI prefix, GET-then-POST XSRF, ambiguous allocations that must be **reconciled by GET** (double-billing otherwise).
5. SSH is a WebSocket carrying raw SSH bytes; Go can embed the client and drop OpenSSH entirely.
6. Upstream `SKILL.md`/blogs are **stale** (auth default, A100 fallback, keep-alive daemon). Code was used as truth.
7. **11 defects found in the Python tool** that must *not* be ported: state silently wiped on parse error, path traversal via session name, code injection via `install`, `edit` can overwrite a remote file after a network blip, unbounded memory upload, no HTTP timeouts, always-on DEBUG log, and more (doc 01 §10).
8. Termux needs an in-binary **DNS + CA-bundle shim**; `GOOS=android` without cgo builds but is the wrong artefact (verified: PIE linked to `/system/bin/linker64`).
9. Upstream moves fast (a command removed and one added within weeks) ⇒ permanent **upstream-sync policy**.
10. The bundled OAuth client is **Google's cloud-SDK client**: a legal/operational risk for a third-party port (R-02).

## Implementation status (2026-11-23)

The Go port is now under active implementation. `golab` builds and runs:

```bash
make build          # produces ./golab (CGO_ENABLED=0)
./golab version
./golab --help
make check          # go vet + go test -race ./...
```

All protocol and foundation packages exist with test-first coverage and fake
servers: `colabapi`, `auth`, `runtime`, `contents`, `sshx`, `state`, `history`,
`nb`, `render`, `repl`, `update`, `platform`, `logx`, `version`, `app`, and the
`internal/cli` command layer. See `docs/09-implementation-contracts.md` for the
package boundaries and `docs/COMPAT.md` for upstream drift.

**Not yet proven:** live Colab behaviour (no Google account / billable VM in this
environment). Per `AGENTS.md`, no feature is called complete until a human has
run the live e2e — in particular `colab auth` equivalents, interactive `ssh`,
`drivemount`, and a real `run` allocation.

## Contents
| File | Purpose |
|---|---|
| `docs/01-python-cli-analysis.md` | what the tool is, module map, state schema, behaviour contracts, quirks, **defects** |
| `docs/02-protocol-spec.md` | **wire specification**: OAuth, `/tun/m/*`, Jupyter WS, outputs, `colab_request`, Contents, SSH; 🔴 items to capture |
| `docs/03-go-architecture.md` | goals, repo layout, interfaces, concurrency, **ADR-001…014**, dependency shortlist |
| `docs/04-platform-matrix.md` | OS/arch tiers, per-platform concerns, **Termux deep-dive**, distribution, CI matrix, definition of done |
| `docs/05-command-parity.md` | every command and flag, behaviour to match, Go notes, priorities, deliberate deviations |
| `docs/06-roadmap.md` | spikes S0–S8, work packages WP-01…22, phases, milestones, v1.0 criteria, upstream-sync |
| `docs/07-testing-and-conformance.md` | test layers, porting map of the 360 upstream tests, fakes, golden/differential tests, **live capture**, live-e2e safety |
| `docs/08-risks-decisions-security.md` | risk register, **decisions needing you**, threat model, licensing, verification status, sources |
| `AGENTS.md` | ready-to-drop guidelines for the Go repo (adapted from upstream's) |

## Decisions I need from you (defaults in doc 08 §2)
1. **DEC-02 OAuth client:** ADC-first + BYO client, with the bundled Google client only as a disclosed fallback?
2. **DEC-01 Name/licence:** `colab-go`, binary `colab`, Apache-2.0 + NOTICE?
3. **DEC-07 stdout/stderr:** move all `[colab]` chatter to stderr (deviates from Python)?
4. **DEC-05 Ctrl-C:** interrupt the remote kernel before exiting?
5. **Scope:** target parity with 0.8.x only, or also track upstream's future commands?

## First five actions
1. Create a throw-away Google account; run **S0** (traffic capture) with the official CLI and fill the 🔴 items of doc 02.
2. Run **S1** (Go: assign → exec → stop) and **S5** (OAuth/ADC in Go): these decide go/no-go.
3. Run **S3** on your Termux phone (resolver/CA shim, Android version matrix).
4. Bootstrap the repo (WP-01) with `AGENTS.md`, CI on six runners, lint gates.
5. Post a short note in upstream Discussions asking about third-party clients and the OAuth client (R-02/R-03).

## What was and was not verified
Verified: full upstream source read; effective wire presentation of tokens (both library versions); a 12-target Go cross-compile; Android no-cgo behaviour. **Not** verified: live Colab responses (no account in the sandbox), Termux behaviour on a real device, Go library versions, Windows/macOS keyring details. Doc 08 §6 lists everything precisely.

*Unofficial. "Google" and "Colab" are trademarks of Google LLC. This pack is analysis, not legal advice.*
