# 04 · Cross-Platform & Architecture Plan

> **Evidence labels:** ✅ verified in the research sandbox · 📚 reported by independent sources (links in doc 08 §Sources) · ❓ plausible, **must be verified** in Phase 0/4.
> The sandbox had Go **1.22.2** only and no access to the Go module proxy; real dependencies (x/sys, keyring, term…) can narrow the matrix, so **CI must build the full binary for every target**, not a stub.

> **Implementation status — 2026-11-26 (v0.2.0).**
> - **Implemented / green in CI:** every Tier-1 target cross-builds with
>   `CGO_ENABLED=0` (GoReleaser snapshot, 11 cross-builds); `go test -race`
>   runs on 6 OS runners; embedded SSH, platform path/TTY/signal handling, the
>   Termux DNS/CA shim in `internal/platform`, the 0600-file keyring fallback,
>   and a single cross-platform URL launcher (`termux-open-url` / `wslview` /
>   `xdg-open` / `open` / `rundll32`) with a print-URL fallback are in the tree.
>   No `GOOS=android` artefact is shipped (ADR-002).
> - **Recorded live proof:** a billable CPU e2e pass covering `new`, `status`,
>   `ls`, `exec`, `run`, `ssh`, piped `repl`, upload/download, `stop --verify`,
>   `resume` error paths and cleanup (final `golab sessions` empty) is recorded
>   in `CHANGELOG.md` `[0.2.0]` Verification; a real signed v0.1.9 → v0.2.0
>   self-update installed an executable ELF.
> - **Not yet proven (human-required):** real Termux devices
>   (DNS/CA/`faccessat2`), Windows console/ACL/`.old` cleanup, macOS keychain
>   timing and notarisation, and a real-device pass for the `qemu-smoke` /
>   `termux-nightly` jobs. The cross-build smoke run was interrupted by a server
>   restart and is not claimed; no Tier-1 per-OS §9 acceptance pass is recorded.
>   See [AUDIT-INDEX.md](AUDIT-INDEX.md) §5 and
>   [CONFORMANCE.md](CONFORMANCE.md) §1/§6.
> - **Distribution actually configured (updated 2026-11-27):** GitHub Releases
>   (archives + SHA-256 checksums + cosign signatures + signed SBOMs) via
>   `.goreleaser.yaml` and `.github/workflows/release.yml`, plus
>   `go install github.com/20Youssef10/golab/cmd/golab@latest`. Updated
>   2026-11-27 for the `v0.3.0` release target: GoReleaser builds **both**
>   `golab` and `golab-mcp`, and every archive carries both binaries plus the two
>   Agent Skills under `.agents/skills/`. Windows additionally gets three
>   `.zip` repacks (13 `.tar.gz` + 3 `.zip` = 16 signed archives). The root
>   `install.sh` and `install.ps1` installers verify the SHA-256 checksum and
>   cosign keyless signature, then install both binaries and the skills; they
>   accept `--no-skills` / `--skills-dir` and `-NoSkills` / `-SkillsDir`.
>   `install.sh --verify-only ARTIFACT BUNDLE CHECKSUMS` reuses that gate for
>   local/CI checks. `Dockerfile` plus `.github/workflows/docker.yml` push a
>   distroless nonroot image to `ghcr.io/20youssef10/golab` on `main` and `v*`
>   tags with provenance, SBOM and digest smoke tests. None of the install
>   paths has a published v0.3.0 tag or live install proof yet.
>   **Packaging configs were added under `packaging/`**: Homebrew formula
>   (`golab.rb`), Scoop manifest (`golab.json`), winget version/locale/installer
>   manifests and an nfpm config (`.deb`/`.rpm`/`.apk`). They remain **configs,
>   not live channels**: none is wired into release CI, and brew/scoop/nfpm are
>   **unverified on this host**. The winget `.zip` blocker is resolved in code
>   and the manifests point at v0.3.0, but every `InstallerSha256` is still
>   `REPLACE_AT_RELEASE` until the signed tag exists. AUR, Termux TUR and Nix
>   remain planned, not configured.

---

## 1. Target matrix

### Tier 1: release-blocking (real runners or devices, full test suite)
| OS | Arch | `GOOS/GOARCH` | Notes |
|---|---|---|---|
| Linux | x86-64 | `linux/amd64` | baseline `GOAMD64=v1`; optional `v3` asset |
| Linux | ARM64 | `linux/arm64` | Raspberry Pi 4/5, AWS Graviton, Apple-silicon VMs |
| macOS | Apple silicon | `darwin/arm64` | universal binary shipped for Homebrew |
| macOS | Intel | `darwin/amd64` | |
| Windows | x86-64 | `windows/amd64` | Windows 10 1809+ (VT console) |
| **Termux (Android)** | ARM64 | `linux/arm64` + Android shim | primary mobile target; emulator + device checklist |

### Tier 2: built every release, smoke-tested (QEMU / emulator / runner), fixes welcome
`windows/arm64` · `linux/arm` (GOARM=7, plus 6) · `linux/386` · `linux/riscv64` · `freebsd/amd64` · Termux `linux/arm` (32-bit phones) · Termux `linux/amd64` (x86_64 emulators, Chromebooks)

### Tier 3: compile-only, community-supported
`linux/ppc64le` · `linux/s390x` · `linux/loong64` · `freebsd/arm64` · `openbsd/amd64` · `netbsd/amd64` · `windows/386`

### Spike result (stdlib-only probe program, `CGO_ENABLED=0 -trimpath -ldflags="-s -w"`, Go 1.22.2) ✅
| Target | Result | Size |
|---|---|---|
| linux/amd64, linux/arm64, linux/386, linux/riscv64 | OK | 1.4–1.5 MB |
| linux/arm GOARM=6 and 7 | OK | 1.5 MB |
| darwin/amd64, darwin/arm64 | OK | 1.5 MB |
| windows/amd64, windows/arm64, windows/386 | OK | 1.5 MB |
| freebsd/amd64 | OK | 1.4 MB |
| android/arm64 (no cgo) | **builds**, but is a *PIE linked against `/system/bin/linker64`* (see §3) | 2.3 MB |
| `GOAMD64=v3`, android/amd64, loong64, ppc64le, s390x, BSDs other than freebsd/amd64 | **not run** (time limit); assume OK, verify in CI | – |

Expected real binary size with TLS, WebSocket, SSH and markdown rendering: **roughly 12–20 MB stripped** ❓ (UPX is not recommended: AV false positives on Windows).

---

## 2. Concern-by-platform matrix

| Concern | Linux | macOS | Windows | Termux |
|---|---|---|---|---|
| **Config dir** | `~/.config/colab-cli` (honour `XDG_CONFIG_HOME`) | `~/.config/colab-cli` (matches Python) | `%APPDATA%\colab-cli`; migrate `~\.config\colab-cli` if present | `$HOME/.config/colab-cli` |
| **File privacy** | mode `0600` | mode `0600` | restrictive ACL on create (owner-only) via `x/sys/windows`; `0600` is a no-op | `0600` |
| **Token store** | Secret Service → file | Keychain → file | Credential Manager → file | **file** (no D-Bus) |
| **Locking** | `flock` | `flock` | `LockFileEx` | `flock` |
| **Atomic write** | `rename(2)` | `rename(2)` | `MoveFileEx(REPLACE_EXISTING)` (Go `os.Rename`) | `rename(2)` |
| **Open browser** | `xdg-open` / `wslview` (WSL) | `open` | `rundll32 url.dll,FileProtocolHandler` | `termux-open-url` (needs `termux-api`), else print URL |
| **TTY / raw mode** | `x/term` | `x/term` | `x/term` + enable VT input/output | `x/term` |
| **Window resize** | `SIGWINCH` | `SIGWINCH` | poll `GetConsoleScreenBufferInfo` every 250 ms ❓ | `SIGWINCH` |
| **Ctrl-C / SIGHUP** | `SIGINT/SIGTERM/SIGHUP` | same | `os.Interrupt`; no SIGHUP, `CTRL_CLOSE_EVENT` via `SetConsoleCtrlHandler` | same as Linux |
| **Console encoding** | UTF-8 | UTF-8 | `SetConsoleOutputCP(65001)`; enable `ENABLE_VIRTUAL_TERMINAL_PROCESSING` | UTF-8 |
| **`/dev/tty` prompt** | `/dev/tty` | `/dev/tty` | `CONIN$` / `CONOUT$` | `/dev/tty` |
| **Null device** | `/dev/null` | `/dev/null` | `NUL` | `/dev/null` |
| **ssh-agent** | `SSH_AUTH_SOCK` | `SSH_AUTH_SOCK` (launchd) | `\\.\pipe\openssh-ssh-agent`, Pageant | `SSH_AUTH_SOCK` if user runs `ssh-agent`; else key files |
| **DNS / CA roots** | system | system | system | **shim required** (§3) |
| **Remote paths** | `path` pkg | `path` | **`path`, never `filepath`** (VM is Linux) | `path` |
| **Shebang `#!/usr/bin/env colab`** | works | works | not supported by the OS: provide `colab run` docs and a `.cmd` shim | `/usr/bin/env` absent; Termux's `termux-exec` rewrites it ❓ |
| **Self-update of running binary** | write temp + rename over | same | rename running `.exe` → `.old`, put new in place, delete `.old` next start | like Linux |

Rule for remote paths: anything that refers to the **VM** uses `path`/`path.Join`; anything local uses `filepath`. A CI lint (`forbidigo`) bans `filepath` in `internal/contents` and `internal/cli/files*.go` for remote values.

---

## 3. Termux deep-dive

### 3.1 Detect
`runtime.GOOS == "android"` **or** (`linux` **and** (`$TERMUX_VERSION` set **or** `$PREFIX` contains `com.termux` **or** `/data/data/com.termux` exists)). Compute once in `internal/platform`.

### 3.2 Problems and fixes (📚 independently reported; ✅ where noted)
| Problem | Symptom | Fix in the binary |
|---|---|---|
| **No `/etc/resolv.conf`** 📚 | pure-Go resolver falls back to `127.0.0.1:53` / `[::1]:53` → every lookup fails (`dial tcp: lookup … on [::1]:53`) | at startup install `net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: …}` that reads `$PREFIX/etc/resolv.conf`, then falls back to `1.1.1.1`, `8.8.8.8`; build a single shared `http.Client`/`net.Dialer` that uses it |
| **CA bundle not at a standard path** 📚 | `x509: certificate signed by unknown authority` | if `SSL_CERT_FILE` is unset and `$PREFIX/etc/tls/cert.pem` exists, set it **before first TLS use**; additionally blank-import `golang.org/x/crypto/x509roots/fallback` ❓ (embedded roots used only when the system store is empty) |
| **`GOOS=android` without cgo** ✅📚 | builds as PIE using `/system/bin/linker64`; reported DNS failures and `argv` mis-parsing when launched via the linker | **do not ship `GOOS=android`** as the primary artefact (ADR-002) |
| **`faccessat2` blocked by seccomp on some Android versions** 📚❓ | `SIGSYS` crash when Go calls `faccessat2` (e.g. `exec.LookPath`) | on Android implement `findExecutable` with `os.Stat` + mode bits (build-tagged) and avoid `exec.LookPath` on the hot path; **test on Android 10/11/12/14** before release |
| **No `/tmp`** | `os.TempDir()` fails if `TMPDIR` unset | use `$TMPDIR`, else `$PREFIX/tmp`, else cache dir |
| **No Secret Service / D-Bus** | keyring probe errors or hangs | skip the keyring on Termux; use the 0600 file store |
| **Android kills background processes (Phantom Process Killer, doze)** ❓ | long `colab ssh`/`run` sessions die | print a one-time hint to run `termux-wake-lock`; document disabling battery optimisation; keep the tool **fire-and-forget** so a killed client never kills the VM |
| **Shebang** ❓ | `#!/usr/bin/env colab` fails | rely on `termux-exec`; document `colab run script.py` as the portable form |

Optional later improvement: Tailscale's `androiddns` approach (query Android's `dnsproxyd` socket) honours VPN/Private DNS, but adds complexity; ship the resolv.conf+fallback shim first.

### 3.3 Installing on Termux
1. **`go install`** from source (`pkg install golang`): works because Termux's Go package is patched for Android; fastest for developers ❓ (verify the patch set at release time).
2. **Release tarball** (`linux/arm64`) + `install.sh` that detects Termux, installs to `$PREFIX/bin`, and prints the `termux-wake-lock` hint.
3. **Package repos**: `termux-user-repository` (community) first; `termux-packages` upstream later (requires their source-build conventions) ❓ check current policy.
4. A `.deb` built with `nfpm` **must use the Termux prefix** (`/data/data/com.termux/files/usr`), not `/usr`.

### 3.4 Testing on Termux
* Every PR: `linux/arm64` binary under **QEMU user-mode** (functional, not Android-specific).
* Nightly: Android emulator (x86_64 image) running Termux; run the **shim tests** (DNS, CA, TMPDIR, no-D-Bus keyring fallback) and the fake-server integration suite.
* Pre-release: manual checklist on **real arm64 phones** (Android 10, 12, 14) including a live GPU `colab run` and an interactive `colab ssh`.

---

## 4. Windows deep-dive
* **Console:** enable VT processing and VT input for raw SSH/REPL sessions; restore modes on exit (also on panic and Ctrl-C). Verify `x/term.MakeRaw` flags on Windows and set `ENABLE_VIRTUAL_TERMINAL_INPUT` manually if it does not ❓.
* **Encoding:** UTF-8 everywhere; avoid BOM in generated notebooks; read scripts as UTF-8 (strip BOM).
* **Signals:** teardown (`run`, `ssh --rm`) must also fire on `CTRL_CLOSE_EVENT`/`CTRL_LOGOFF_EVENT` (console window closed).
* **Credential Manager limit (~2.5 KB blob):** store the refresh token only (ADR-006).
* **Self-update:** rename-then-replace pattern; never delete the running exe.
* **Proxy mode on Windows:** emit a ready-to-paste `ssh_config` block using `colab.exe ssh --proxy-mode …` with correct quoting (upstream tests this: `test_windows_ssh`, `repro_windows_ssh`).
* **Distribution:** `scoop` bucket, `winget` manifest, signed `.exe` (SignPath OSS programme or Azure Trusted Signing ❓ eligibility) to reduce SmartScreen friction.
* **Shell:** ship PowerShell completion; test in Windows Terminal, conhost, PowerShell 5.1 and 7, Git-Bash/MSYS (TTY detection differs).

## 5. macOS deep-dive
* Universal2 binary (`lipo`) via GoReleaser; **notarisation** needs an Apple Developer account; Homebrew installs avoid the quarantine attribute, direct downloads do not (document `xattr -d com.apple.quarantine`).
* Keychain via `go-keyring` shells out to the `security` tool; confirm it does **not** expose secrets on a process command line before relying on it ❓ (else store only a short refresh token or use the file fallback).
* Rosetta: ship native arm64; do not rely on x86 emulation.

## 6. Linux deep-dive
* Static binary, no libc dependency (works on glibc, musl/Alpine, NixOS, `scratch` images).
* Headless/CI/WSL/containers have **no Secret Service**: fail fast and use the file store; `COLAB_AUTH=adc` is the recommended non-interactive path.
* `scratch`/distroless images may lack CA certificates: the embedded roots fallback covers this.
* WSL: open URLs via `wslview` or `explorer.exe`.

---

## 7. Distribution channels

| Channel | Platforms | Mechanism | Status (v0.3.0 published 2026-11-27; next-release items noted) |
|---|---|---|---|
| GitHub Releases (13 `.tar.gz` + 3 Windows `.zip`, checksums + cosign sigs + SBOM) | all | GoReleaser, both binaries + both skills in every archive | **v0.3.0 published** (49 assets, release run `46062649930`); direct-archive download + checksum + both binaries smoke-tested in a live Colab VM |
| Standalone `golab-mcp_<version>_<os>_<arch>.tar.gz` (+ Windows `.zip`) | all | second GoReleaser archive set, signed and attested like the CLI archives | configured for the **next release**; v0.3.0 assets are immutable and do not contain it. `go install github.com/20Youssef10/golab/cmd/golab-mcp@v0.3.0` was verified live |
| `install.sh` (SHA-256 + cosign, installs both binaries and skills; `--verify-only`, `--no-skills`, `--skills-dir`) | Linux, macOS, Termux | curl installer | **live-verified in a Colab VM with cosign v2.6.5** (checksum + signature + both binaries + both skills). Requires cosign >= v2.6.5; older versions are rejected with an actionable error |
| `install.ps1` (SHA-256 + cosign, installs both binaries and skills; `-NoSkills`, `-SkillsDir`) | Windows | PowerShell installer | config/schema verified; no Windows host available here for a live install |
| GHCR image `ghcr.io/20youssef10/golab` | all with Docker | `Dockerfile` (distroless nonroot, both binaries + skills) + `.github/workflows/docker.yml` | GHCR publish workflow green; image pull smoke test not run on this host |
| Homebrew tap | macOS, Linux | `packaging/golab.rb` formula | manifests pinned to the signed v0.3.0 archives; no brew binary here — schema only |
| Scoop bucket | Windows | `packaging/golab.json` manifest | manifests pinned to the signed v0.3.0 archives; no scoop binary here — schema only |
| winget | Windows | `packaging/20Youssef10.golab*.yaml` → PR to `winget-pkgs`, using the new `.zip` assets | `.zip` blocker resolved; manifests pin the three **real v0.3.0 zip hashes** from signed `checksums.txt`; `winget validate` still needs a Windows host |
| `.deb` / `.rpm` / `.apk` | Linux | `packaging/nfpm.yaml` | config added; unverified here, not CI-wired |
| AUR `colab-go-bin` | Arch | PKGBUILD | planned |
| Termux (`go install`, tarball, TUR) | Termux | see §3.3 | `go install` configured; TUR planned |
| `go install github.com/20Youssef10/golab/cmd/golab@latest` | all with Go | source | configured |
| Nix flake | optional | community | planned |

Rows marked "in tree", "config added" or "configured" describe repository
contents, not verified live channels. No packaging config has been validated by a
package-manager install on this host or in CI, and no v0.3.0 release tag has been
pushed yet; the release-time checksum step for winget is mandatory before a
`winget-pkgs` PR.

## 8. Build & CI matrix

```yaml
# .github/workflows/ci.yml (sketch)
jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, ubuntu-24.04-arm, macos-latest, macos-15-intel, windows-latest, windows-11-arm]
    runs-on: ${{ matrix.os }}          # runner names ❓ confirm availability/pricing for the repo's plan
    steps: [checkout, setup-go, "go vet ./...", "go test -race -count=1 ./...", govulncheck]
  cross-build:                          # every GOOS/GOARCH in the tier table, full binary, CGO_ENABLED=0
    runs-on: ubuntu-latest
    steps: ["goreleaser build --snapshot --clean"]
  qemu-smoke:                           # armv7, riscv64, 386, arm64 under qemu-user: `colab version` + fake-server suite
  termux-nightly:                       # Android emulator + Termux
  live-e2e:                             # nightly/manual, secrets-gated (doc 07 §Live)
```
GoReleaser `ignore` list removes unsupported combos (e.g. `darwin/386`, `windows/arm` unless needed).

## 9. Definition of done per platform

A platform is **supported** only when all are green on that platform:
1. `go test ./...` (unit + fake-server) passes.
2. `colab version`, `colab help`, shell completion install.
3. Fake-server end-to-end: `new → exec → upload → download → ls → rm → stop`.
4. Token store works (keyring or documented fallback); no hang > 5 s on a locked/absent keyring.
5. `colab ssh -- echo ok` and an interactive session (PTY, resize, Ctrl-C, `exit 7` status propagation) against the fake sshd.
6. Self-update dry-run verifies a signed artefact.
7. Live smoke on a CPU runtime (`colab run` of a one-liner) by a human at least once per release.
