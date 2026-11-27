---
layout: default
title: Overview & status
lede: "golab is an unofficial, single-binary Go port of Google Colab's CLI. This site is a navigable view of the repository's canonical docs; the code wins when they disagree."
source_file: docs/INDEX.md
source_section: "Project in one paragraph"
permalink: /
---

<div class="card-grid">
  <a class="card" href="{{ '/quickstart/' | relative_url }}"><strong>Quickstart</strong><span>Allocate a VM, run code, stop it.</span></a>
  <a class="card" href="{{ '/commands/' | relative_url }}"><strong>Commands map</strong><span>All 54 top-level commands by area.</span></a>
  <a class="card" href="{{ '/architecture/' | relative_url }}"><strong>Architecture</strong><span>Two planes, two credentials, no daemon.</span></a>
  <a class="card" href="{{ '/conformance/' | relative_url }}"><strong>Verification</strong><span>What is proven, faked, or human-only.</span></a>
</div>

<h2>Canonical project summary</h2>

{% capture md %}{% include source/overview.md %}{% endcapture %}
{{ md | markdownify }}

<h2>Newer than the dated design docs</h2>

<p>
  The excerpt above is a faithful copy of <code>docs/INDEX.md</code>, whose proof-status
  paragraph is dated. For changes landed after it — the MCP server, Agent Skills, spend
  guards, VM inspection, detached-run watchdog and packaging configs — read the
  <a href="https://github.com/20Youssef10/golab/blob/main/CHANGELOG.md">CHANGELOG</a>
  <code>[0.3.0]</code> section and
  <a href="{{ '/conformance/' | relative_url }}">the verification ledger</a> on this site.
</p>
