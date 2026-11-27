---
layout: default
title: VM inspection
lede: "Read the running VM through server-side polling: processes, files, disk usage, search and verified downloads."
source_file: docs/USAGE.md
source_section: "VM inspection"
permalink: /vm-inspection/
---

{% capture md %}{% include source/vm-inspection.md %}{% endcapture %}
{{ md | markdownify }}

<p>
  These commands were exercised on billable CPU VMs on 2026-11-26/27; two defects
  found there (array-shaped <code>du</code> payloads and relative paths resolving
  against kernel cwd <code>/content</code>) were fixed and re-verified. See the
  <a href="https://github.com/20Youssef10/golab/blob/main/CHANGELOG.md">CHANGELOG</a>
  and <a href="{{ '/conformance/' | relative_url }}">conformance ledger</a> for the exact scope.
</p>
