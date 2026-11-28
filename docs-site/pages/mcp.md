---
layout: default
title: MCP server (25 tools)
lede: "A separate <code>golab-mcp</code> binary exposes the CLI to agents over stdio or streamable HTTP, with an explicit confirmation gate on every mutating call."
source_file: docs/USAGE.md
source_section: "12. MCP server"
permalink: /mcp/
---

{% for group in site.data.mcp.tools %}
<h2>{{ group.group }}</h2>
<table>
  <thead><tr><th>Tool</th><th>Contract (from <code>internal/mcpserver/tools.go</code>)</th></tr></thead>
  <tbody>
    {% for tool in group.items %}
    <tr><td><code>{{ tool.name }}</code></td><td>{{ tool.summary }}</td></tr>
    {% endfor %}
  </tbody>
</table>
{% endfor %}

<h2>Setup, transports and packaging</h2>

<p>
  For the current v0.4.0 release, <code>golab-mcp</code> is built by GoReleaser and
  ships in the same signed archives as <code>golab</code>, alongside both Agent
  Skills. It has no <code>update</code> subcommand: replace it with the next
  signed release archive.
</p>

{% capture md %}{% include source/mcp.md %}{% endcapture %}
{{ md | markdownify }}
