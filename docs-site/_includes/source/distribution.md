<!-- Generated from docs/04-platform-matrix.md §7. Distribution channels by docs-site/tools/sync-sources.py. Do not edit. -->

## 7. Distribution channels

| Channel | Platforms | Mechanism | Status (current: v0.4.0; v0.3.0 remains published) |
|---|---|---|---|
| GitHub Releases (13 `.tar.gz` + 3 Windows `.zip`, checksums + cosign sigs + SBOM) | all | GoReleaser, both binaries + both skills in every archive | **v0.3.0 published** (49 assets, release run `46062649930`); direct-archive download + checksum + both binaries smoke-tested in a live Colab VM |
| Standalone `golab-mcp_<version>_<os>_<arch>.tar.gz` (+ Windows `.zip`) | all | second GoReleaser archive set, signed and attested like the CLI archives | shipped in v0.4.0 with the same signatures and SBOMs; `go install github.com/20Youssef10/golab/cmd/golab-mcp@v0.3.0` was verified live |
| `install.sh` (SHA-256 + cosign, installs both binaries and skills; `--verify-only`, `--no-skills`, `--skills-dir`) | Linux, macOS, Termux | curl installer | **live-verified in a Colab VM with cosign v2.6.5** (checksum + signature + both binaries + both skills). Requires cosign >= v2.6.5; older versions are rejected with an actionable error |
| `install.ps1` (SHA-256 + cosign, installs both binaries and skills; `-NoSkills`, `-SkillsDir`) | Windows | PowerShell installer | config/schema verified; no Windows host available here for a live install |
| GHCR image `ghcr.io/20youssef10/golab` | all with Docker | `Dockerfile` (distroless nonroot, both binaries + skills) + `.github/workflows/docker.yml` | GHCR publish workflow green; image pull smoke test not run on this host |
| Homebrew tap | macOS, Linux | `packaging/golab.rb` formula | manifests pinned to signed release archives; no brew binary here — schema only |
| Scoop bucket | Windows | `packaging/golab.json` manifest | manifests pinned to signed release archives; no scoop binary here — schema only |
| winget | Windows | `packaging/20Youssef10.golab*.yaml` → PR to `winget-pkgs`, using the new `.zip` assets | `.zip` blocker resolved; manifests pin real signed `.zip` hashes from `checksums.txt`; `winget validate` still needs a Windows host |
| `.deb` / `.rpm` / `.apk` | Linux | `packaging/nfpm.yaml` | config added; unverified here, not CI-wired |
| AUR `colab-go-bin` | Arch | PKGBUILD | planned |
| Termux (`go install`, tarball, TUR) | Termux | see §3.3 | `go install` configured; TUR planned |
| `go install github.com/20Youssef10/golab/cmd/golab@latest` | all with Go | source | configured |
| Nix flake | optional | community | planned |

Rows marked "in tree", "config added" or "configured" describe repository
contents, not verified live channels. No packaging config has been validated by a
package-manager install on this host or in CI; `winget validate` and a real
`winget-pkgs` PR remain maintainer steps on a Windows host.
