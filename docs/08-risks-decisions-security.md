# 08 · Risks, Decisions, Security & Verification Status

## 1. Risk register

Likelihood (L) / Impact (I): **H**igh · **M**edium · **L**ow.

| ID | Risk | L | I | Mitigation | Early-warning trigger |
|---|---|---|---|---|---|
| **R-01** | **Undocumented, unstable API.** `/tun/m/*` and the proxy can change without notice; a port breaks silently. | H | H | golden wire fixtures; nightly read-only live probe; upstream-sync policy (doc 06 §5); typed errors with clear "protocol changed?" message | nightly probe fails; upstream release touches `client.py`/`runtime.py` |
| **R-02** | **OAuth client reuse.** Upstream's own comment says the bundled client is Google's *cloud-SDK* client, and the required redirect is registered to it. A third-party binary using it may be blocked, rate-limited or flagged, and sends users' consent through someone else's client identity. | M | H | ship **ADC-first** docs and `-c` BYO-client as first-class paths; keep the bundled client behind a clear disclosure (DEC-02); isolate in one package so it can be swapped; ask Google/upstream whether a third-party reuse is acceptable | `redirect_uri_mismatch`, `invalid_client`, consent-screen warnings |
| **R-03** | **Client identity checks.** The server may allow-list `X-Colab-Client-Agent: colab-cli` or the OAuth client. A Go client sending its own agent could be rejected, while impersonating the official agent is dishonest. | M | H | spike S0/S1 tests both; use the single **honest** identity `github.com/20Youssef10/golab/<ver>` (`version.ClientAgent()`) on every plane; if rejected, discuss with upstream rather than spoof (DEC-03; S-3 closed) | 4xx on first call from Go but not Python |
| **R-04** | **Colab terms / abuse detection / quota.** Automated allocation and live CI tests could trip limits or risk the test account. | M | M | low volume; single concurrent runtime; throw-away account; sweeper; kill switch (doc 07 §9); read the current terms before enabling nightly jobs | 429/412 spikes, account warnings |
| **R-05** | **Upstream churn.** ~35 PRs/month (third-party snapshot); commands appear/disappear; docs drift (doc 01 §9). | H | M | compat declaration; monthly re-capture; triage rubric; keep Δ list short | parity diff in nightly L4 |
| **R-06** | **Jupyter protocol edge cases:** lost iopub on connect, reconnect, giant messages, interleaved executions. | M | M | optional `kernel_info` handshake; read-limit config; fuzz decoder; per-execution parent filtering; reconnect tests | flaky e2e, truncated outputs |
| **R-07** | **Embedded SSH diverges from OpenSSH** (agent forwarding, `~/.ssh/config`, ControlMaster) and may regress terminal behaviour. | M | M | keep `--proxy-mode`; document unsupported OpenSSH features; PTY test matrix | issues from VS Code Remote users |
| **R-08** | **Termux/Android quirks:** DNS, CA, seccomp, process killing, no `/tmp`. | H | M | netfix shim; fallback roots; Stat-based exec lookup; emulator nightly; device checklist; wake-lock hint | any x509/DNS error on device |
| **R-09** | **Windows complexity:** VT console, ACLs, Credential Manager size, SmartScreen, running-exe update. | M | M | S2/S4/S8; refresh-token-only storage; signed binaries; rename-replace updater | Windows-only failures |
| **R-10** | **Secret exposure via OS keyring helpers** (e.g. macOS `security` argv), logs, crash dumps. | M | H | S4; typed redaction; file-mode/ACL tests; no secrets on command lines; `GOTRACEBACK` default | secrets in `ps`, logs |
| **R-11** | **Licensing/attribution** of a derivative work (Apache-2.0; BSD-3 for jupyter-kernel-client logic). | M | M | LICENSE + NOTICE; per-file headers on translated code; state changes; legal check if commercialised | – |
| **R-12** | **Maintenance burden / bus factor** and agent-generated code quality. | M | M | strict test-first + CI gates; small packages; ADRs; contribution guide | review backlog |
| **R-13** | **Supply chain** (Go modules, release pipeline) in a tool that handles Google tokens. | L | H | minimal deps; `go mod verify`, `govulncheck`, pinned GH Actions by SHA, cosign + SBOM, 2FA + branch protection | advisory for a dependency |
| **R-14** | **Name collisions / typosquatting** (`colab-cli` on PyPI and crates.io; `colab` binary shadowing). | M | M | distinctive repo name; `colab version` shows implementation; install docs explain PATH order | user confusion reports |
| **R-15** | **Terminal-escape injection from remote output.** Code running on the VM (or a notebook it executes) can print OSC-52 clipboard writes, title changes, or query sequences that make the *local* terminal respond. | M | M | sanitiser in `render`: allow SGR colour, strip OSC/DCS/APC and terminal-query CSI; `--raw` opt-out | security review finding |

## 2. Decision log

Status note (2026-11-26, v0.2.0): DEC-01 is settled in code
(`github.com/20Youssef10/golab`, binary `golab`); DEC-03 is settled on one honest
identity on all planes; DEC-08 was **rejected** in favour of upstream behaviour.
The remaining rows are design decisions recorded for traceability.

| ID | Decision | Options | **Recommended default** | Decide by |
|---|---|---|---|---|
| DEC-01 | Project name, module path, licence | `colab-go` / other; Apache-2.0 | **Resolved:** `github.com/20Youssef10/golab`, binary `golab`, **Apache-2.0 + NOTICE** | kickoff |
| DEC-02 | OAuth client strategy | (a) embed upstream's client; (b) BYO only; (c) ADC-first + embedded fallback with disclosure | **(c)** | before M1 |
| DEC-03 | `X-Colab-Client-Agent` value | `colab-cli` vs own | **own:** `github.com/20Youssef10/golab/<ver>` on all planes; ask upstream if a live probe proves rejection | S1 |
| DEC-04 | Headless token fallback | plaintext 0600 vs passphrase vs refuse | **0600 file + notice; optional passphrase** | S4 |
| DEC-05 | SIGINT behaviour | match Python (just exit) vs interrupt remote kernel | **interrupt, then stop; 2nd Ctrl-C forces** | Phase 2 |
| DEC-06 | REPL library | readline fork / Bubble Tea / go-prompt fork | decide after **S7** | Phase 3 |
| DEC-07 | Chatter stream | keep stdout (parity) vs stderr | **stderr** (agents pipe stdout) | Phase 1 |
| DEC-08 | `--gpu` + `--tpu` together | silent `--tpu` precedence (upstream) vs error | **REJECTED** — match upstream: both accepted, `--tpu` silently wins (no error); see docs/USAGE.md | Phase 1 |
| DEC-09 | Public library | `pkg/colab` now vs later | **later** (after v1.0) | v1.0 |
| DEC-10 | Scopes | keep all 6 vs minimise (`cloud-platform` is very broad) | keep for parity; **test minimal set in S5** and document | S5 |
| DEC-11 | Single binary vs plugin for REPL/markdown | single | **single** (simplicity) | Phase 3 |

## 3. Security model

**Assets:** (1) Google OAuth refresh token (the scope set includes `cloud-platform`: broad); (2) runtime-proxy tokens (short-lived, VM control); (3) VM contents and any Drive credentials propagated into it; (4) the user's SSH private keys; (5) the integrity of the installed binary.

**Threats → controls**

| Threat | Control |
|---|---|
| Tokens in logs, argv, env dumps, crash output | typed `Secret`; redaction tests; no secrets on command lines (also for keyring helpers); `slog` off by default |
| Other local users reading state | `0600` files, `0700` dir; Windows owner-only ACL; test both |
| Malicious update / downgrade | pinned release source; checksum **and** cosign identity verification; no user-editable update URL (fixes D-9); refuse downgrade |
| Redirecting tokens to an attacker host | no env/flag to change Colab hosts in release builds (ADR-013); exact host matching (fixes D-7); no `InsecureSkipVerify` |
| Code injection into the VM kernel | never splice user strings into code; JSON/`repr`-quoted payloads (D-3) |
| Path traversal (local & remote) | session-name validator (D-2); clean/reject `..` in history paths; remote paths via `path.Clean`; download refuses to write outside the target |
| Data loss | atomic state writes; abort on corrupt state (D-1); `edit` aborts on non-404 (D-11) |
| Hanging forever | explicit timeouts everywhere (D-5) |
| Hostile output on the local terminal | escape-sequence sanitiser (R-15) |
| SSH MITM beyond TLS | transport already authenticated by TLS+bearer; host keys ephemeral ⇒ accept-any is documented (ADR-005); keys limited to ed25519/ECDSA |
| Supply chain | minimal deps, `govulncheck`, pinned Actions, cosign keyless verification (Fulcio chain + Rekor proof/SET + pinned identity) **and a required signed SBOM attestation**, protected branches. Fulcio SCT checking **is enforced** in production (`internal/update/verify.go:157-163`; only a test-CA opt-out for SCT-less bundles exists), and bundles must be sigstore v0.3 protobuf format — legacy bundles pass `cosign verify-*` but fail `golab update --install` (corrected 2026-11-24; previously recorded as an "SCT gap"). |
| Typosquatting | distinct names; documentation of canonical install commands; checksums |

**Review gate (WP-21):** threat-model walk-through, `gosec`, dependency audit, fuzz run ≥ 1 h per decoder, manual token-leak hunt (`strings`, `ps`, `strace -f -e trace=execve`, Process Monitor on Windows).

## 4. Licensing & attribution (not legal advice)
* Upstream is **Apache-2.0**. A translation is a derivative: keep `LICENSE`; add `NOTICE` crediting Google LLC; mark translated files (“Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified”); state changes.
* The kernel-client behaviour was learned from the Colab fork of Datalayer's `jupyter-kernel-client` (**BSD-3-Clause**); if code is translated rather than re-derived from the protocol, include its notice.
* "Google", "Colab" are Google's marks: describe the project as **unofficial** and avoid logos.
* The bundled OAuth client belongs to Google (R-02): redistributing the JSON file is permitted by the licence; *using* the client identity is governed by Google's terms, not the licence.
* Get a proper legal read before commercial distribution.

## 5. Open questions
1. Is a third-party Go client welcome/allowed to use the bundled OAuth client? (ask upstream via GitHub Discussions.)
2. Will Colab keep `/tun/m/*` stable? Any plan for a public API?
3. Does the proxy accept a reduced header set or reject unknown client agents? (S0/S1)
4. Is chunked upload honoured? (S6)
5. Can `cloud-platform` and `drive.file` be dropped from the OAuth scopes for non-Drive use? (S5)
6. Does upstream want Windows/arm64/Termux contributions? (Discussions #49/#50/#83, PR #70 suggest openness.)
7. Should the port target a Colab Enterprise/Vertex backend later?

## 6. Verification status of this analysis

| Claim class | Status |
|---|---|
| Architecture, flows, flags, defaults, defects D-1…D-11, state schema | **Read directly in source** (v0.8.0+2) |
| Effective kernel/WS header and token presentation | **Read in source** of both the pinned fork commit and PyPI 0.9.0; *not* confirmed on the wire |
| Response JSON shapes | **Inferred from pydantic models**; no live traffic captured |
| Cross-compilation of a stdlib-only program for 12 targets; `GOOS=android` no-cgo ⇒ PIE using `linker64` | **Verified by experiment** (Go 1.22.2). `GOAMD64=v3`, android/amd64, loong64 etc. **not run** |
| Termux DNS/CA/`faccessat2` problems | **Reported by independent projects** (search results; not all pages read in full); **not reproduced** on a device |
| Windows/macOS keyring behaviour, `x/term` Windows flags, `x509roots/fallback`, library maintenance status/versions | **Unverified** (no Go proxy access; no devices) |
| Upstream project metrics (stars, PR rate) | third-party snapshot, **approximate** |
| Live Colab behaviour (allocation, SSH, Drive) | **Not tested** (no Google account in the research sandbox) |

## 7. Sources
*Primary (read in full or in the relevant parts):* `github.com/googlecolab/google-colab-cli` (clone: source, docs, AGENTS.md, CHANGELOG, cloudbuild, uv.lock) · `github.com/googlecolab/jupyter-kernel-client` (clone) · PyPI `jupyter-kernel-client 0.9.0` (wheel).
*Secondary (search results/snippets; use as pointers, re-verify):* Google Developers Blog "Introducing the Google Colab CLI" · InfoQ (2026-06) · Help Net Security (2026-06-08) · upstream Discussions #49, #50, #77, #83 and PR #70 (Windows) · Rust crate `colab-cli` 0.1.4 on docs.rs (independent client; useful cross-reference for `/tun/m/*`) · `colabctl` on PyPI (wrapper) · `dave.engineer` "Cross-compiling Go for Android (Termux) with working DNS" · `therealfreddied/cliproxyapi-termux` · `termux/termux-packages` discussion #20602 · `pkg.go.dev/tailscale.com/feature/androiddns` · `coddy-project/coddy-agent` PR #396 and `curedbylethe/booxd` PR #12 (Android builds) · `cpdata/antigravity-cli-termux` (`faccessat2`).
Third-party blogs about the Colab CLI are **stale** on the keep-alive daemon; trust the source.
