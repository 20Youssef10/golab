<!-- Generated from docs/04-platform-matrix.md §7. Distribution channels by docs-site/tools/sync-sources.py. Do not edit. -->

## 7. Distribution channels

| Channel | Platforms | Mechanism | Phase |
|---|---|---|---|
| GitHub Releases (archives + checksums + cosign sigs + SBOM) | all | GoReleaser | 1 |
| `install.sh` (verifies SHA-256 + cosign signature; `--verify-only`) | Linux, macOS, Termux | curl installer | exists (2026-11-26), not CI-wired/live-proven |
| `install.ps1` (verifies SHA-256 + cosign signature) | Windows | PowerShell installer | exists (2026-11-26), not CI-wired/live-proven |
| Homebrew tap | macOS, Linux | `packaging/golab.rb` formula | configs added; unverified here, not CI-wired |
| Scoop bucket | Windows | `packaging/golab.json` manifest | configs added; unverified here, not CI-wired |
| winget | Windows | `packaging/20Youssef10.golab*.yaml` version/locale/installer manifests → PR to `winget-pkgs` | configs added; **blocked pending `.zip` repack** |
| `.deb` / `.rpm` / `.apk` | Linux | `packaging/nfpm.yaml` | configs added; unverified here, not CI-wired |
| AUR `colab-go-bin` | Arch | PKGBUILD | 5 |
| Termux (`go install`, tarball, TUR) | Termux | see §3.3 | 4 |
| `go install github.com/20Youssef10/golab/cmd/golab@latest` | all with Go | source | 1 (configured) |
| Nix flake / Docker `scratch` image | optional | community | 5+ |

Rows marked "exists" or "configs added" are files present in the repository,
not configured release channels; the remaining Phase values are the original
plan, not a claim that those channels are live. None of the packaging configs
has been validated by a package-manager install on this host or in CI.
