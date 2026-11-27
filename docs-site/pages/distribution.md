---
layout: default
title: Install & distribution
lede: "The v0.3.0 release target ships both binaries and both skills in signed archives, plus a GHCR image. Channel status is stated per row."
source_file: docs/04-platform-matrix.md
source_section: "7. Distribution channels"
permalink: /distribution/
---

{% capture install %}{% include source/install.md %}{% endcapture %}
{{ install | markdownify }}

<h2>Channels and their real status</h2>

{% capture dist %}{% include source/distribution.md %}{% endcapture %}
{{ dist | markdownify }}

<h2>Release archives and install scripts</h2>

<p>
  For the <code>v0.3.0</code> release target, <code>.goreleaser.yaml</code> builds
  <code>golab</code> <em>and</em> <code>golab-mcp</code>, and every archive carries
  both binaries plus the two Agent Skills under <code>.agents/skills/</code>.
  There are 13 <code>.tar.gz</code> archives (the updater's input) and 3 Windows
  <code>.zip</code> repacks built from the same binaries — 16 assets, each with a
  checksum entry, a cosign signature bundle and an SBOM attestation.
  <code>install.sh</code> and <code>install.ps1</code> still verify checksum and
  cosign signature first, then install both binaries and the skills. Skip skills
  with <code>--no-skills</code> / <code>-NoSkills</code>, or relocate them with
  <code>--skills-dir PATH</code> / <code>-SkillsDir PATH</code>.
</p>

<h2>Docker / GHCR</h2>

<p>
  The multi-stage <code>Dockerfile</code> builds both static binaries with
  <code>CGO_ENABLED=0</code> and copies them, with both skills, into a distroless
  nonroot runtime image. <code>.github/workflows/docker.yml</code> pushes
  <code>ghcr.io/20youssef10/golab</code> on pushes to <code>main</code> and on
  <code>v*</code> tags, with BuildKit provenance and SBOM attestations, a
  verify-stage smoke test and a smoke test of the exact pushed digest.
</p>

```bash
# After the image workflow has run:
docker pull ghcr.io/20youssef10/golab:main
docker run --rm ghcr.io/20youssef10/golab --help
docker run --rm --entrypoint /golab-mcp ghcr.io/20youssef10/golab --help
```

<p>
  Skills live at <code>/usr/share/golab/skills</code> in the image
  (<code>GOLAB_SKILLS_DIR</code>).
</p>

<h2>Still not live / release-time steps</h2>

<p>
  Homebrew, Scoop and nfpm remain configs: they are neither wired into release CI
  nor live-verified on a package-manager install. The winget <code>.zip</code> blocker is
  resolved in code and the manifests point at v0.3.0, but each
  <code>InstallerSha256</code> is still <code>REPLACE_AT_RELEASE</code>: a
  maintainer must copy the three Windows <code>.zip</code> digests from the signed
  v0.3.0 <code>checksums.txt</code> into the manifest before a
  <code>winget-pkgs</code> PR. AUR, Termux TUR and Nix remain plans. The Docker
  workflow and install scripts are configured in code, but no <code>v0.3.0</code>
  tag has been pushed yet, so treat these as configured paths rather than
  release-day proof.
</p>

{% capture artifacts %}{% include source/release-artifacts.md %}{% endcapture %}
{{ artifacts | markdownify }}

<div class="card-grid">
  <a class="card" href="https://github.com/20Youssef10/golab/tree/main/packaging"><strong>packaging/</strong><span>brew, Scoop, winget and nfpm configs.</span></a>
  <a class="card" href="https://github.com/20Youssef10/golab/blob/main/docs/INSTALL-TERMUX.md"><strong>Termux guide</strong><span>Checksum and cosign verification, keyring fallback.</span></a>
</div>
