---
layout: default
title: Reproducibility
lede: "Freeze the Python environment and record a provenance bundle so a run can be replayed or audited."
source_file: docs/USAGE.md
source_section: "Reproducibility"
permalink: /reproducibility/
---

<div class="card-grid">
  <a class="card" href="https://github.com/20Youssef10/golab/blob/main/docs/USAGE.md"><strong>env freeze</strong><span>Capture the session's Python environment.</span></a>
  <a class="card" href="https://github.com/20Youssef10/golab/blob/main/docs/USAGE.md"><strong>bundle record / verify</strong><span>Record and check execution provenance.</span></a>
</div>

{% capture md %}{% include source/reproducibility.md %}{% endcapture %}
{{ md | markdownify }}
