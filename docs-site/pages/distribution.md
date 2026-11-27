---
layout: default
title: Install & distribution
lede: "Install from source, a signed release archive or a package manager config. Publication and live verification status are stated per channel."
source_file: docs/04-platform-matrix.md
source_section: "7. Distribution channels"
permalink: /distribution/
---

{% capture install %}{% include source/install.md %}{% endcapture %}
{{ install | markdownify }}

<h2>Channels and their real status</h2>

{% capture dist %}{% include source/distribution.md %}{% endcapture %}
{{ dist | markdownify }}

<div class="card-grid">
  <a class="card" href="https://github.com/20Youssef10/golab/tree/main/packaging"><strong>packaging/</strong><span>brew, Scoop, winget and nfpm configs.</span></a>
  <a class="card" href="https://github.com/20Youssef10/golab/blob/main/docs/INSTALL-TERMUX.md"><strong>Termux guide</strong><span>Checksum and cosign verification, keyring fallback.</span></a>
</div>

<h2>What is not live</h2>

<p>
  Homebrew, Scoop and nfpm configs are in the repository but are neither wired into
  release CI nor live-proven here. winget is blocked pending a <code>.zip</code> repack
  (releases currently publish <code>.tar.gz</code>). Docker/GHCR and Nix are
  <strong>planned, not published</strong>: there is no Dockerfile, image workflow or
  <code>ghcr.io</code> artifact in the repository. AUR and Termux TUR are likewise future
  plans. Only GitHub Releases archives with checksums, cosign signatures and SBOMs are
  published today, alongside <code>go install</code>.
</p>

{% capture artifacts %}{% include source/release-artifacts.md %}{% endcapture %}
{{ artifacts | markdownify }}
