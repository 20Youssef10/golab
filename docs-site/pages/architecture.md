---
layout: default
title: Two planes, two credentials
lede: "One static binary, no daemon: commands authenticate, do one job and exit. The control plane and data plane use different, deliberately short-lived credentials."
source_file: docs/03-go-architecture.md
source_section: "1–5 · 9 (ADRs)"
permalink: /architecture/
---

<figure class="diagram">
  {% include diagrams/architecture.svg %}
  <figcaption>The two credential planes. <code>colab auth</code> is a third operation that injects credentials inside the VM; it is not a plane credential.</figcaption>
</figure>

<h2>Boundaries that must not be blurred</h2>

<ul>
  <li><strong>Control plane</strong> — user OAuth or ADC bearer token; assignment, unassignment, status.</li>
  <li><strong>Data plane</strong> — short-lived runtime-proxy token; kernel WebSocket, Contents API, embedded SSH.</li>
  <li><strong>colab auth</strong> — injects credentials into the VM; a separate operation, not a token for either plane.</li>
  <li><strong>Package boundaries</strong> — protocol packages (<code>colabapi</code>, <code>runtime</code>, <code>contents</code>, <code>sshx</code>) never import <code>state</code> or <code>cli</code>; only <code>cmd/golab</code> imports <code>internal/cli</code>.</li>
</ul>

{% capture md %}{% include source/architecture.md %}{% endcapture %}
{{ md | markdownify }}
