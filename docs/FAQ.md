# Frequently asked questions

Short answers to the questions that come up most often. For full details, see the [user guide](USAGE.md), the [migration guide](MIGRATION.md) and the [compatibility declaration](COMPAT.md).

## Is `golab` official?

No. It is an unofficial, community-written Go implementation of Google's Colab CLI. It is not endorsed by or affiliated with Google LLC. “Google” and “Colab” are trademarks of Google LLC. The project is Apache-2.0 licensed and includes portions derived from `google-colab-cli`, © Google LLC.

## Which version of the upstream CLI does it target?

Upstream HEAD `0abf82d` — the `v0.8.0` tag plus five commits — analysed on 2026-11-23. That includes `stop --all`, `--public-api` / `--no-public-api`, `test-public-api`, the `ssh --force` equivalent and the Drive propagation fix. `golab version` prints `golab 0.1.0 (compatible with google-colab-cli 0.8.x+HEAD)`.

## What OAuth scopes does `golab` request?

The default OAuth2 provider requests the same six scopes as upstream: `openid`, `userinfo.profile`, `userinfo.email`, `cloud-platform`, **`colaboratory`** and `drive.file`. The `colaboratory` scope is mandatory — without it `/tun/m/*` returns 401/403.

With `--auth adc` (Application Default Credentials), `gcloud auth application-default login` may ignore requested scopes, so supply them at login time:

```bash
gcloud auth application-default login --scopes=openid,https://www.googleapis.com/auth/cloud-platform,https://www.googleapis.com/auth/userinfo.email,https://www.googleapis.com/auth/colaboratory
```

Check what you actually have with `golab whoami` (hidden command); it prints scopes and expiry, never the token.

## Why do I get “No active sessions”?

`golab` uses a shared session-resolution rule: an explicit `-s NAME` wins; otherwise it uses the unique local session; with zero sessions it prints `No active sessions found. Create one with 'golab new'.`, and with several it lists the names and asks you to choose. Run `golab sessions` to see what exists. A session can disappear because it was stopped, expired, or pruned after a terminal 404/401. If the server shows assignments that you do not recognise locally, they appear as `[?]` orphans — clean them up explicitly so they do not keep billing you.

## SSH says HTTP 429 — what should I do?

Colab limits concurrent SSH sessions per runtime. First try:

```bash
golab ssh --force -s NAME
```

`--force` kills stale sshd children through the kernel before dialling (bounded to 10 seconds; diagnostics on stderr). If that does not help, stop stale sessions with `golab stop -s NAME` or `golab stop --all`, then reconnect. See [USAGE.md §4](USAGE.md#4-remote-access-ssh).

## What do 412, quota errors and 503 mean?

- **412 / “Allocation refused (precondition failed)”** usually means too many active sessions or a temporary capacity limit. Stop a session, wait, or try a different accelerator.
- **Backend rejected accelerator / quota error** means your account is not entitled to that GPU/TPU. Try `--gpu T4` or drop `--gpu`/`--tpu` for CPU.
- **503 / timeouts on high-VRAM shapes** have been reported upstream (issue #273) for G4/A100/L4 and `--high-mem`. `golab` validates hardware before allocating, retries and reconciles assignments, and turns these into actionable messages rather than raw failures. Live behaviour on paid accounts is not proven in this release.

## Why is `--high-mem` ignored?

For L4, V5E1 and V6E1 there is only one machine shape, so `golab` prints `[colab] --high-mem ignored: this accelerator only offers one machine shape.` and continues. For other accelerators it requests the high-RAM shape. The same notice appears for `new`, `run` and `ssh` when they create a runtime.

## Does the proxy 411 problem (upstream #270) affect `golab`?

Upstream's body-less POSTs get HTTP 411 behind some forward proxies. Go's `net/http` emits `Content-Length: 0` for POSTs, which may behave differently, but this has **not** been verified in a real proxied environment and is on the human-required checklist in [REMEDIATION.md](REMEDIATION.md). If you hit it, record the proxy configuration and report it. Do not work around it by disabling TLS verification.

## How do I set up Termux?

Install the `linux_arm64` (or matching `linux_arm`) release archive, or install Go inside Termux and run `go install github.com/20Youssef10/golab/cmd/golab@latest`. There is no `GOOS=android` artefact — the correct build is the Linux binary running under Termux. `golab` installs a DNS resolver and CA-bundle shim at startup (using `$PREFIX/etc/resolv.conf` and `$PREFIX/etc/tls/cert.pem`), prefers `$PREFIX/tmp` for temporary files, and uses `termux-open-url` for browser links. Real-device checks are still human-gated, so treat this as best-effort support.

## What about Windows consoles and terminals?

`golab` enables and restores Windows VT console modes, uses `rundll32` for browser URLs, `notepad` as the default editor, and `CONIN$` for interactive Drive confirmation. Self-update replaces the running executable safely and removes leftover `.old` files on startup. Configuration lives in `%APPDATA%\colab-cli\`. The Windows device/TTY matrix is part of the remaining human verification; see [REMEDIATION.md](REMEDIATION.md).

## Where are my credentials stored?

- **OAuth2:** in the OS keyring (service `google-colab-cli`, user `oauth_token`), probed with a 5-second timeout. If no keyring is available, a `0600` file in the config directory is used. A Python `token.json` is imported once.
- **ADC:** credentials are managed by `gcloud`, not by `golab`.
- Only the **refresh token** is persisted; access tokens are minted on demand. `golab whoami` never prints a token, and tokens are redacted from logs and errors.

Config directory: `~/.config/colab-cli/` on Linux/macOS/Termux, `%APPDATA%\colab-cli\` on Windows; override with `COLAB_CONFIG_DIR`. Run `golab audit` for a local report on file modes and token-store tier.

## How do I rotate credentials or switch accounts?

```bash
golab logout
golab whoami        # confirm the active identity
```

`logout` clears the local OAuth token, blanks cached proxy tokens in `sessions.json` and resets the API capability cache. It deliberately does **not** revoke the token at Google and does **not** stop VMs. Under `--auth=adc` it prints `gcloud auth application-default revoke` guidance. If sessions were left running under the old account, stop them with `golab stop --all`. See [MIGRATION.md](MIGRATION.md#keeping-existing-sessions).

## Can agents and CI consume `golab` output?

Yes. Set `COLAB_OUTPUT=json` or `--output ndjson` to wrap stdout in the stable `golab/1` schema:

```json
{"schema":"golab/1","type":"info","text":"..."}
{"schema":"golab/1","type":"output","text":"..."}
{"schema":"golab/1","type":"error","text":"..."}
```

Diagnostics stay on stderr. `--explain` prints a redacted plan for billable commands before any network call, and `--yes` (or `COLAB_ASSUME_YES=1`) handles non-destructive confirmations without bypassing budget enforcement. `golab agentout` lets you test your consumer without a VM. See [USAGE.md §7](USAGE.md#7-structured-output-plans-and-confirmations).

## Why is the chatter on stderr?

Because stdout must be usable as data. The Python CLI prints `[colab] …` progress lines on stdout, which breaks `echo 'code' | colab exec | jq` and makes `ssh -- CMD` byte streams unusable. `golab` sends all of its own diagnostics to stderr and keeps stdout for program output. This is a deliberate, documented deviation (DEC-07).

## How do I verify a release?

The supported path is `golab update --install`. It checks the published checksums, verifies a cosign keyless signature tied to the project's OIDC identity (`20Youssef10/golab`), requires a Rekor proof / signed certificate timestamp, and refuses a release without a signed SBOM attestation for your platform. It also refuses downgrades and signer-identity mismatches. For manual verification, download the `.tar.gz`, its checksums, the `.sigstore.json` bundle and the `*.sbom.sigstore.json` attestation from the matching GitHub Release and check them with `cosign verify-blob` / `cosign verify-blob-attestation --type https://cyclonedx.org/schema/bom-1.5` (the counterpart of `attest-blob`; `verify-attestation` is for OCI images). The **exact predicate URI** matters: the `cyclonedx` alias makes cosign emit `https://cyclonedx.org/bom`, which `golab` rejects, whereas the URI above is copied verbatim into the DSSE statement and is what `golab update --install` accepts.

**Known status (2026-11-24):** v0.1.6 was signed and passed the workflow's cosign proof, but `golab update --install` against it **failed** — cosign had written legacy bundles that the updater's sigstore v0.3 parser rejects. v0.1.7 was then signed (release run `45414858634` green) with correct v0.3 bundles and passing cosign signatures, but the install still **failed**, now at the SBOM check: `SBOM attestation has an unsupported predicate type`. The `cyclonedx` alias emits `https://cyclonedx.org/bom`, which the updater rejects. The producer now uses the exact accepted URI `--type https://cyclonedx.org/schema/bom-1.5` for both attestation and verification, with a CI guard on the emitted `predicateType`; this is chosen so the unchanged verifier in already-shipped v0.1.6/v0.1.7 binaries can install the next release. Because published assets are immutable, v0.1.9 was cut to prove the updater end-to-end. v0.1.8 was published with correct artifacts but its workflow did **not** go green: run `45428451505` failed at that `predicateType` guard because cosign v2.6.5 writes the DSSE envelope at the bundle root while the guard read `.content.dsseEnvelope.payload`; the guard now accepts both layouts. A separate critical install-path defect (writing the downloaded `.tar.gz` bytes over the executable) was fixed before v0.1.9. **v0.1.9 is verified:** release run `45446096192` is green, and a real v0.1.8 → v0.1.9 `golab update --install` produced an executable `golab 0.1.9` that runs `doctor`. See [CONFORMANCE.md §4](CONFORMANCE.md#4-release-pipeline-and-updater-identity).

## What has not been tested live?

Everything that needs a Google account, a billable VM or a real terminal: interactive `auth`/OAuth consent, `repl` and `ssh` TTY sessions, `drivemount`, a full CPU `run` followed by an empty `golab sessions`, chunked uploads over 75 MiB with SHA-256 comparison, GET-only wire captures for eight inferred response shapes, and Termux/Windows device matrices. Signed releases v0.1.6, v0.1.7 and v0.1.8 all had defects along the updater path; they are documented in §4 of CONFORMANCE.md. v0.1.9 closed that path: its release workflow is green and a real signed self-update from v0.1.8 was verified to install an executable binary. Interactive TTY and billable live probes listed above remain untested. Unit tests, fakes, lint and cross-builds are green, but they are not live proof of those flows. The full list is in [REMEDIATION.md](REMEDIATION.md#human-required-items-not-closed-by-this-release).

## Do I need Python or OpenSSH installed?

No. `golab` is a static `CGO_ENABLED=0` binary with an embedded Jupyter/kernel client and SSH client. The Python package can stay installed during migration — the two tools use the same state files but have different binary names. `golab ssh --proxy-mode` exists if you specifically want to bridge an external OpenSSH client.

## Upstream's `colab auth` hangs (issue #269). Does `golab`?

The code path is different: upstream discards the `stdin_hook` return value, so the kernel's `input()` never gets an `input_reply`. `golab`'s runtime hook explicitly sends `input_reply` with the prompt taken from `content.prompt`, and prints the prompt text rather than a raw message dict. We consider this a code-level fix, but it still needs a live human run before we claim it as proven. If you can run `golab auth` against a real session, please report the result.

## Can I keep my existing Python sessions?

Yes. `golab` reads the same `sessions.json`, `settings.json` and history logs and uses the same config directory, so `golab sessions` should show your existing runtimes without re-login. Walk through [MIGRATION.md](MIGRATION.md) for the details, including `--config`, `--no-public-api` and token-store behaviour.

## How do I report a bug or a security issue?

For bugs and feature requests, read [CONTRIBUTING.md](../CONTRIBUTING.md) and open an issue with the `golab version` output, the exact command, the stderr log (with tokens redacted) and whether you were using a TTY. Do **not** post security issues publicly — follow [SECURITY.md](../SECURITY.md). Never attach raw token values, authorization headers or OAuth codes.
