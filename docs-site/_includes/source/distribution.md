<!-- Generated from docs/04-platform-matrix.md §7. Distribution channels by docs-site/tools/sync-sources.py. Do not edit. -->

## 7. Distribution channels

| Channel | Platforms | Mechanism | Status (v0.3.0 target, 2026-11-27) |
|---|---|---|---|
| GitHub Releases (13 `.tar.gz` + 3 Windows `.zip`, checksums + cosign sigs + SBOM) | all | GoReleaser, both binaries + both skills in every archive | configured in `.goreleaser.yaml` / `release.yml`; tag pending |
| `install.sh` (SHA-256 + cosign, installs both binaries and skills; `--verify-only`, `--no-skills`, `--skills-dir`) | Linux, macOS, Termux | curl installer | in tree; no published-tag install proof yet |
| `install.ps1` (SHA-256 + cosign, installs both binaries and skills; `-NoSkills`, `-SkillsDir`) | Windows | PowerShell installer | in tree; no published-tag install proof yet |
| GHCR image `ghcr.io/20youssef10/golab` | all with Docker | `Dockerfile` (distroless nonroot, both binaries + skills) + `.github/workflows/docker.yml` | pushes on `main` / `v*` tags with provenance, SBOM and digest smoke test |
| Homebrew tap | macOS, Linux | `packaging/golab.rb` formula | config added; unverified here, not CI-wired |
| Scoop bucket | Windows | `packaging/golab.json` manifest | config added; unverified here, not CI-wired |
| winget | Windows | `packaging/20Youssef10.golab*.yaml` → PR to `winget-pkgs`, using the new `.zip` assets | `.zip` blocker resolved in code; manifests pin v0.3.0 but `InstallerSha256: REPLACE_AT_RELEASE` until the signed tag exists |
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
