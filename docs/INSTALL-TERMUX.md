# Installing `golab` on Termux

`golab` is an **unofficial** Go port of `google-colab-cli`. This guide covers a native Termux install of the released static binary.

> **Why this is not the Python guide.** Upstream issue #131 describes a Python-only Termux setup: TUR wheels, a forked `jupyter-kernel-client` and a `pip check` step. None of that applies here. `golab` is one CGO-free Go executable — no Python interpreter, virtualenv, wheel, pip or external `ssh` binary is installed or required. The two credential planes (control-plane OAuth/ADC bearer and short-lived data-plane runtime token) still apply, but the install path is entirely Go.

> **Read [Verification status](#8-verification-status) before relying on this.** The binary was executed on one Termux host in this repository's environment; there is **no device matrix and no real-device run**. Steps that need a device, a Google account or live network are marked.

## 1. Prerequisites

- Termux with `$PREFIX` set (install from F-Droid or the official GitHub builds; the Play Store package is outdated).
- `curl` (or `wget`), `tar` and `sha256sum` — all from Termux `coreutils`/`curl` packages.
- Optional: `cosign` v2 available on your device, to check the keyless signature and SBOM yourself (install it by your preferred method; availability in Termux packages is not checked here).
- No root. Everything is installed under `$PREFIX/bin` and `$HOME/.config/colab-cli`.

```bash
echo "$PREFIX"          # e.g. /data/data/com.termux/files/usr
command -v curl tar sha256sum
```

## 2. Download the linux/arm64 release (v0.1.9)

Termux on ARM devices uses the standard **`linux/arm64`** Go release asset. There is no `GOOS=android` artifact — `.goreleaser.yaml` builds only `linux`, `darwin`, `windows` and `freebsd`, and `arm64` is in the GOARCH matrix.

```bash
cd "$HOME"
REL=https://github.com/20Youssef10/golab/releases/download/v0.1.9
for f in \
  golab_0.1.9_linux_arm64.tar.gz \
  golab_0.1.9_linux_arm64.tar.gz.sigstore.json \
  golab_0.1.9_linux_arm64.tar.gz.sbom.sigstore.json \
  checksums.txt; do
  curl -fsSLO "$REL/$f"
done
ls -1 golab_0.1.9_linux_arm64*
```

## 3. Verify before installing

### 3.1 Checksum

`checksums.txt` lists **every** asset in the release, so the plain command only passes when all of them are present. Verify the whole file, or just your archive's line:

```bash
sha256sum -c checksums.txt                                   # all downloaded assets
grep 'golab_0.1.9_linux_arm64.tar.gz$' checksums.txt | sha256sum -c -   # this archive only
```

### 3.2 Keyless signature (needs `cosign`)

This is the same identity the release workflow pins: repository `20Youssef10/golab`, workflow file `release.yml`, tag `v0.1.9`, issuer `https://token.actions.githubusercontent.com`.

```bash
cosign verify-blob \
  --certificate-identity 'https://github.com/20Youssef10/golab/.github/workflows/release.yml@refs/tags/v0.1.9' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle golab_0.1.9_linux_arm64.tar.gz.sigstore.json \
  golab_0.1.9_linux_arm64.tar.gz
```

### 3.3 Signed SBOM attestation (needs `cosign`)

The updater also binds a CycloneDX SBOM to the archive; verify that too. The exact predicate URI matters — the `cyclonedx` alias emits a predicate the verifier rejects.

```bash
cosign verify-blob-attestation \
  --type https://cyclonedx.org/schema/bom-1.5 \
  --certificate-identity 'https://github.com/20Youssef10/golab/.github/workflows/release.yml@refs/tags/v0.1.9' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle golab_0.1.9_linux_arm64.tar.gz.sbom.sigstore.json \
  golab_0.1.9_linux_arm64.tar.gz
```

If `cosign` is not installed, skip 3.2/3.3 and rely on the checksum plus the signed self-update step in §7 — but note that `golab update --install` performs all three checks for you.

## 4. Install to `$PREFIX/bin`

The archive is flat (no directory components) and contains the `golab` binary plus `LICENSE`, `NOTICE`, `README.md` and `CHANGELOG.md`. Extract only the binary:

```bash
mkdir -p "$PREFIX/bin"
tar -xzf golab_0.1.9_linux_arm64.tar.gz -C "$PREFIX/bin" golab
chmod 755 "$PREFIX/bin/golab"
hash -r            # or open a new shell so PATH picks up the new file
golab version
```

You should see `golab 0.1.9`. The file is a statically linked ELF (`CGO_ENABLED=0`, `-trimpath`): it has no libc or Python dependency and does not need a bundled interpreter, virtualenv or system OpenSSH. `which colab` may still find a globally installed Python CLI; use `golab version` to confirm which binary you are running.

## 5. Configuration, state and credentials

- **Base directory:** `$HOME/.config/colab-cli/` on Termux (same name as the Python CLI, so existing state is picked up). Override it with `COLAB_CONFIG_DIR`.
- **Files inside it:** `sessions.json` (active runtimes and proxy tokens), `settings.json` (update throttle and API probes) and `history/` (per-session JSONL logs). Writes are locked, temp-file + fsync + rename; a corrupt file aborts rather than resetting.
- **Token store:** `golab` probes the OS keyring first (`go-keyring`, Secret Service over D-Bus). Secret Service is unavailable under Termux, so it falls back to an **owner-only `0600`** file at `$HOME/.config/colab-cli/oauth_token.json`. This is a routine fallback, not an auth failure.
- **Diagnose the store:** `golab audit` reports the actual tier, the keyring probe result and each file's mode — no request is issued and no credential value is printed.
- **See the reason:** `COLAB_DEBUG=1 golab audit` (or any command) prints the keyring-fallback hint on stderr and writes a redacted `oauth2: keyring tier unavailable` debug log entry. There is **no `--debug` flag**.
- **Avoid token storage:** use `golab --auth=adc` (or `COLAB_AUTH=adc`) to use Application Default Credentials instead of the OAuth2 file/keyring.

```bash
golab audit
COLAB_DEBUG=1 golab audit
golab --auth=adc sessions
```

## 6. Termux runtime notes

- **Temporary files:** `golab` prefers `$TMPDIR`, then `$PREFIX/tmp` on Termux (see `internal/platform.TempDir`). Set `TMPDIR` if you want a different scratch directory.
- **DNS / CA shim:** `internal/platform/netfix` installs a Go resolver using `$PREFIX/etc/resolv.conf` nameservers and points `SSL_CERT_FILE` at `$PREFIX/etc/tls/cert.pem` when present. It is applied automatically at startup, once per process — there is nothing to install or configure.
- **Opening URLs:** `pay`, `url --open` and the Google Drive consent flow try `termux-open-url` when it is on `PATH`, falling back to printing the URL. The URL is always printed first, so headless use stays usable; a failed launcher never aborts the command. `termux-open-url` comes from the `termux-api` package.
- **Long runs:** take a Termux wake lock so Android does not suspend the session: `termux-wake-lock` before and `termux-wake-unlock` after. These are Termux host commands, not `golab` flags.
- **Unofficial binary:** `golab` reads and writes the Python CLI's state format, so you can migrate without re-authenticating.

```bash
termux-wake-lock          # optional, for long-lived runs (needs termux-api)
golab new -s demo         # allocate a runtime (billable; human-only step — see §8)
golab stop -s demo        # release it when done
termux-wake-unlock
```

## 7. Quick start

These are the safe, non-allocating checks first, then the self-update:

```bash
golab version                 # static binary identity, no network
golab sessions                # list active runtimes (read-only)
golab usage                   # account compute-unit balance — requires auth/network
golab doctor                  # offline config/auth/update/wire checks by default
GOLAB_LIVE_PROBES=1 golab doctor --live   # opt-in GET-only probes, still no VM allocation
golab update                  # read-only: check for a newer release, install nothing
golab update --install        # signed self-update: checksum + cosign + SBOM, then extract & replace
```

`doctor --live` is additionally gated by `LIVE_E2E_ENABLED`: it is refused when that variable is `false`. `golab update --install` verifies the checksum, keyless signature and signed SBOM **before** touching the filesystem, extracts the single `golab` binary from the verified archive and replaces the running executable in place (so an install under `$PREFIX/bin/golab` updates that file). A downgrade is refused.

The billable commands (`new`, `run`, `exec`, `ssh`, `upload`, …) are documented in [docs/USAGE.md](USAGE.md); use `golab --explain <command>` for a redacted dry-run plan before spending anything.

## 8. Verification status

This is an honest account, not a certification:

- **Done in this repository environment (one Termux *host*, not a device):** the static `linux/arm64` binary was built with `CGO_ENABLED=0` and executed (`golab version`, `golab doctor`); the offline CLI surface and the flags shown above were checked against `internal/cli/`. The v0.1.9 release's checksum, cosign signature and signed SBOM were verified in CI (release run `45446096192`), and a signed self-update was proven end-to-end on that host — see [docs/CONFORMANCE.md §4](CONFORMANCE.md#4-release-pipeline-and-updater-identity).
- **Device-unverified — needs a human on real hardware:** every step in §2–§7 as run on a physical phone/tablet; the Termux `termux-open-url` launcher for `pay`/`url`/Drive consent; `termux-wake-lock` behaviour during a long run; interactive `repl`/`ssh`; `drivemount`; the OS-keyring probe and its fallback on a specific device; and any billable `new`/`run` flow. No device matrix was run and no real-device result is claimed.
- **Also unverified here:** the local `cosign verify-blob` / `verify-blob-attestation` commands in §3.2–3.3 (they require `cosign` on your device); `golab usage` (needs live auth); and `doctor --live` (opt-in, network).

The authoritative list of human-only gates is [docs/CONFORMANCE.md §6](CONFORMANCE.md#6-human-only--external-steps-blocking-done) (Termux device matrix is item 7) and the [docs/REMEDIATION.md human checklist](REMEDIATION.md#human-required-items-not-closed-by-this-release). Those items block any "supported platform" or "live-proven" claim.
