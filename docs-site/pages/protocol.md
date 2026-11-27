---
layout: default
title: Control & data plane wire spec
lede: "Reconstructed wire behaviour with explicit confidence markers. Green is code- or capture-backed; red items remain unverified probes."
source_file: docs/02-protocol-spec.md
source_section: "0–4 (constants, auth, control plane, runtime proxy, SSH)"
permalink: /protocol/
---

<figure class="diagram">
  {% include diagrams/protocol.svg %}
  <figcaption>Request flow. The bearer token is used only on the control plane; the runtime proxy receives its own short-lived token.</figcaption>
</figure>

<p>
  Kernel WebSocket details follow Jupyter's default protocol:
  <code>colab_request</code> arrives on iopub, while <code>input_reply</code> on stdin
  wraps <code>colab_reply</code>. Control-plane mutations strip the XSSI prefix and
  use GET-then-POST XSRF; every HTTP call has explicit connect, TLS, header and idle
  timeouts.
</p>

{% capture md %}{% include source/protocol.md %}{% endcapture %}
{{ md | markdownify }}
