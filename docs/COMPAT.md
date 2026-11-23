# Compatibility declaration

Target: current upstream `googlecolab/google-colab-cli` HEAD (analysed at commit
`0abf82d`, 2026-11-23). The original design pack in docs/01–08 was written
against v0.8.0+2; this file records the drift found by re-reading upstream source.

## Upstream drift since v0.8.0+2

| Change | Effect on golab | Status |
|---|---|---|
| "Public API" path: `colaboratory.googleapis.com/v1beta/runtimes`, `--public-api/--no-public-api`, `COLAB_CLI_PUBLIC_API`, `test-public-api` command, settings fields `use_public_api`, `public_api_supported`, `tunnel_api_supported` | Implement both control-plane paths; persist the three new settings fields; expose the flag and command. | colabapi implements both; CLI wiring pending |
| `colab stop --all` added | Add the flag and "Stopped N session(s)." message. | CLI wiring pending |
| `colab ssh -- CMD` handshake sends `colab-client-agent=ssh-mode-cmd` and is byte-exact when piped | Already in docs/02 §4; verify against HEAD tests. | sshx in progress |
| Debug log and state files now created owner-only | Matches our atomic-write design. | state done |
| `--auth` default is `oauth2`; ADC scopes must include `colaboratory` | Matches docs/02 §1. | auth in progress |

## Implementation notes (2026-11-23)

* Every command in docs/05 is implemented, including the upstream additions
  (`stop --all`, `--public-api/--no-public-api`, `test-public-api`).
* `pay` uses a small in-tree cross-platform URL launcher (`xdg-open` / `open` /
  `rundll32`) instead of `pkg/browser`. `colab url --open` currently only prints
  the URL: `internal/cli/utility.go:261` is a no-op. This is a **known P0 gap**
  (see `docs/AUDIT-01-parity.md`), and the previous claim that it used a launcher
  was incorrect (2026-11-23 audit correction).
* `update` uses signed GitHub Releases verification (Fulcio chain + Rekor
  proof/SET + mandatory signed SBOM), not upstream's unverified PyPI/pip path.
  GoReleaser must publish `*.sbom.sigstore.json` or `colab update` refuses the
  release (`ErrMissingSBOM`).
* Upstream `--force` for `ssh` was not ported (not part of the agreed scope).

## Known deliberate deviations (Δ) from upstream

See docs/05 "Deliberate deviations" and docs/08 decision log. The short version:
all `[colab] …` chatter goes to **stderr**; SSH is embedded (with `--proxy-mode`
escape hatch); state writes are atomic and corrupt state aborts; session names
are validated; uploads stream; logging is off by default; HTTP timeouts are
always set; self-update is signed; `edit` aborts on non-404 errors.

## Unverified wire details

docs/02 §7 lists the 🔴 items. Until a human runs the live capture (S0),
response shapes remain inferred from Python pydantic models and the wire
fakes implement that inference. Never treat the fakes as proof of live behaviour.
