---
layout: default
title: Install & distribution
lede: "v0.3.0 is published with both binaries and both skills in signed archives, plus a GHCR image. Channel status is stated per row."
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
  The published <code>v0.3.0</code> release has 13 <code>.tar.gz</code> archives
  (the updater's input), 3 Windows <code>.zip</code> repacks from the same
  binaries, and 30 signature/SBOM sidecars — 49 assets in total. Every archive
  carries both <code>golab</code> and <code>golab-mcp</code> plus one flat
  <code>golab-skills.tar.gz</code> member holding the two Agent Skills; the flat
  layout keeps signed self-update compatible with the older v0.2.0 verifier.
  <code>install.sh</code> and <code>install.ps1</code> verify checksum and cosign
  signature first, then install both binaries and the skills. Skip skills with
  <code>--no-skills</code> / <code>-NoSkills</code>, or relocate them with
  <code>--skills-dir PATH</code> / <code>-SkillsDir PATH</code>.
  <strong>cosign v2.6.5 or newer is required</strong>: releases use
  <code>--new-bundle-format</code> (sigstore v0.3 protobuf), which older cosign
  cannot verify. The installers now reject older versions with an actionable
  error. A second archive set for a standalone <code>golab-mcp</code> download is
  configured for the next release.
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
  Homebrew, Scoop and nfpm are pinned to the published signed v0.3.0 archives
  with real <code>checksums.txt</code> digests; no package-manager binary was
  available here, so those channels are schema-verified only. The winget
  <code>.zip</code> blocker is resolved and the manifests carry the three real
  v0.3.0 <code>.zip</code> hashes — a maintainer still needs
  <code>winget validate</code> on a Windows host before opening the
  <code>winget-pkgs</code> PR. AUR, Termux TUR and Nix remain plans. The GHCR
  workflow is green; image pull was not exercised on this host.
</p>

{% capture artifacts %}{% include source/release-artifacts.md %}{% endcapture %}
{{ artifacts | markdownify }}

<div class="card-grid">
  <a class="card" href="https://github.com/20Youssef10/golab/tree/main/packaging"><strong>packaging/</strong><span>brew, Scoop, winget and nfpm configs.</span></a>
  <a class="card" href="https://github.com/20Youssef10/golab/blob/main/docs/INSTALL-TERMUX.md"><strong>Termux guide</strong><span>Checksum and cosign verification, keyring fallback.</span></a>
</div>
