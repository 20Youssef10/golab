# Compatibility declaration

Target: current upstream `googlecolab/google-colab-cli` HEAD (analysed at commit
`0abf82d`, **v0.8.0+5**, 2026-11-23). The original design pack in docs/01–08 was
written against v0.8.0+2; this file records the drift found by re-reading
upstream source.

> **Reconciled with code — 2026-11-26 (v0.2.0).** Every bullet below was checked
> against the v0.2.0 tree; the "pending / in progress" statuses in the drift
> table have been corrected. The canonical module path is
> `github.com/20Youssef10/golab` (binary `golab`, Go 1.27.4), so the
> `github.com/<owner>/colab-go` placeholder in doc 03 ADR-001 is historical.
> Unverified wire details remain unverified — fake-server tests are not live
> proof (doc 02 §7; [AUDIT-INDEX.md](AUDIT-INDEX.md) §5).

## Upstream drift since v0.8.0+2

| Change | Effect on golab | Status (v0.2.0) |
|---|---|---|
| "Public API" path: `colaboratory.googleapis.com/v1beta/runtimes`, `--public-api/--no-public-api`, `COLAB_CLI_PUBLIC_API`, `test-public-api` command, settings fields `use_public_api`, `public_api_supported`, `tunnel_api_supported` | Implement both control-plane paths; persist the three new settings fields; expose the flag and command. | **Implemented**: both paths in `internal/colabapi`, all three settings fields, flag/env var and hidden `test-public-api`; full LRO lifecycle beyond upstream. |
| `colab stop --all` added | Add the flag and "Stopped N session(s)." message. | **Implemented** (`stop --all`, with the confirmation gate `--yes`). |
| `colab ssh -- CMD` handshake sends `colab-client-agent=ssh-mode-cmd` and is byte-exact when piped | Already in docs/02 §4; verify against HEAD tests. | **Implemented** in `internal/sshx` with byte-exact fake-server tests; `ssh --force` added too. |
| Debug log and state files now created owner-only | Matches our atomic-write design. | **Implemented** in `internal/state` (atomic write, owner-only). |
| `--auth` default is `oauth2`; ADC scopes must include `colaboratory` | Matches docs/02 §1. | **Implemented** in `internal/auth`. |
| Open upstream issues/PRs (#74/#200 notebooks in `run`, #237 `exec --json`, #258 Drive consent polling, #71 narrow scopes, #220/#261 recovery, #157 local secrets) | Port the useful subset and record deviations; Web secrets and unsigned notice feeds remain out of scope. | **Implemented in `[0.2.0]` / `[Unreleased]`** (test-first, fakes; a billable live e2e pass is recorded in `CHANGELOG.md`, GET-only wire capture still human-only). |

## Implementation notes — reconciled 2026-11-26 (v0.2.0)

* Module path is `github.com/20Youssef10/golab` (binary `golab`, Go 1.27.4,
  `CGO_ENABLED=0`); `go install github.com/20Youssef10/golab/cmd/golab@latest`
  is the canonical source channel (audit P0-8). The placeholder
  `github.com/<owner>/colab-go` in doc 03 ADR-001 is historical.
* Every command in docs/05 is registered in `internal/cli/register.go`,
  including the upstream additions (`stop --all`, `--public-api/--no-public-api`,
  `test-public-api`) and the Go-only `doctor`, `audit`, `budget`, `bundle`,
  `sync`, `agentout` and `logout` commands.
* `pay`, `url --open` and Drive consent URLs use one shared in-tree cross-platform
  launcher (`termux-open-url` / `wslview` / `xdg-open` / `open` / `rundll32`, with
  a print-URL fallback) in `internal/cli/utility.go`, instead of `pkg/browser`.
  The previous no-op `url --open` was fixed in v0.1.0 (audit P0-2).
* `update` uses signed GitHub Releases verification via `sigstore-go/pkg/verify`
  (Fulcio chain + Rekor proof/SET + certificate-time checks + pinned identity),
  not upstream's unverified PyPI/pip path. GoReleaser publishes
  `*.sbom.sigstore.json` or `colab update` refuses the release (`ErrMissingSBOM`).
  The updater's owner/repo default is `20Youssef10/golab`.
* `ssh --force` is implemented (upstream commit `cda02bc`): it kills stale sshd
  children through the kernel before connecting (audit P0-1).
* `logout` and recursive `upload/download -r` are implemented from upstream
  PRs #266/#267 (traversal and symlink guards on `-r`).
* The full Colab Public API lifecycle (LRO create/poll/delete, `runtime_name`
  persisted, operation-code mapping) is implemented ahead of upstream HEAD,
  which currently only lists runtimes via the Public API. See
  [AUDIT-00-live-upstream.md](AUDIT-00-live-upstream.md) §4 and
  `internal/colabapi/public.go`.

## Known deliberate deviations (Δ) from upstream

See docs/05 "Deliberate deviations" and docs/08 decision log. The short version
(all re-verified against the v0.2.0 code): all `[colab] …` chatter goes to
**stderr**; SSH is embedded (with `--proxy-mode` escape hatch); state writes are
atomic and corrupt state aborts; session names are validated; uploads stream
(with single-shot `chunk:1` for small files); logging is off by default; HTTP
timeouts are always set; self-update is signed; `edit` aborts on non-404 errors.

## Unverified wire details

docs/02 §7 lists the 🔴 items. At v0.2.0 several are settled in code and covered
by fake-server tests (see the dated note at the top of doc 02), but none are
live-proven. Until a human runs the live capture (S0), response shapes remain
inferred from Python pydantic models and the wire fakes implement that
inference. Never treat the fakes as proof of live behaviour; see
[AUDIT-INDEX.md](AUDIT-INDEX.md) §5.
