---
layout: default
title: Agent Skills
lede: "Two portable skills describe safe operation of the CLI and of the MCP server to filesystem-loading agents."
source_file: docs/USAGE.md
source_section: "13. Agent Skills discovery"
permalink: /skills/
---

<div class="card-grid">
  <a class="card" href="https://github.com/20Youssef10/golab/tree/main/.agents/skills/golab-operations"><strong>golab-operations</strong><span>Session lifecycle, transfer, inspection, redaction and path rules.</span></a>
  <a class="card" href="https://github.com/20Youssef10/golab/tree/main/.agents/skills/colab-mcp"><strong>colab-mcp</strong><span>Client setup, transports, confirm gating and Inspector checks.</span></a>
</div>

<p>
  Both skills ship in signed release archives, are installed by
  <code>install.sh</code> / <code>install.ps1</code> (unless skipped), and are
  bundled in the GHCR image at <code>/usr/share/golab/skills</code>.
</p>

{% capture md %}{% include source/skills.md %}{% endcapture %}
{{ md | markdownify }}

<p>
  Skills carry no live proof of their own; they document surfaces whose current
  verification status is recorded in
  <a href="{{ '/conformance/' | relative_url }}">the conformance ledger</a>.
</p>
