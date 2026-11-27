---
layout: default
title: Conformance & verification ledger
lede: "An honest ledger: what is proven live, what is fake-tested, and what still needs a human with a Google account, a billable VM or a real terminal."
source_file: docs/CONFORMANCE.md
source_section: "1 · 3 · 6"
permalink: /conformance/
---

<aside class="source-note" aria-label="Verification status">
  <span class="source-note-label">Current status</span>
  <span>
    Latest published release: <code>{{ site.release }}</code>. Fake-backed tests cover the
    newer MCP, skills, packaging and guard surfaces; a billable CPU pass on 2026-11-26/27
    covered allocation, inspection, detach submission, <code>reap</code>, <code>cost</code>
    and cleanup. OAuth re-consent, Drive browser approval, interactive TTY, real device
    matrices, the GCP quota project and uploads over 75 MiB remain human-only.
  </span>
</aside>

{% capture md %}{% include source/conformance.md %}{% endcapture %}
{{ md | markdownify }}

<p>
  The excerpt above is generated from <code>docs/CONFORMANCE.md</code>. The most recent
  live-probe results are recorded in the
  <a href="https://github.com/20Youssef10/golab/blob/main/CHANGELOG.md">CHANGELOG</a>
  under <code>[0.3.0]</code>; read both together.
</p>
