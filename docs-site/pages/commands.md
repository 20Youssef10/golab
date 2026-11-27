---
layout: default
title: Commands map (54)
lede: "Every top-level command in <code>golab help</code>, grouped by area. Flags and exit codes live in the canonical user guide."
source_file: docs/05-command-parity.md
source_section: "Global flags · Deliberate deviations"
permalink: /commands/
---

<p>
  The table below is generated from <code>golab help</code> on
  <code>{{ site.release }}</code>. Hidden commands (<code>auth</code>, <code>whoami</code>,
  <code>test-public-api</code>) are intentionally absent from the help list and from
  this map. For full flags and worked recipes, use the
  <a href="https://github.com/20Youssef10/golab/blob/main/docs/USAGE.md">user guide</a>.
</p>

{% for group in site.data.commands %}
<h2>{{ group.group }}</h2>
<table>
  <thead><tr><th>Command</th><th>Help summary</th></tr></thead>
  <tbody>
    {% for cmd in group.items %}
    <tr><td><code>golab {{ cmd.name }}</code></td><td>{{ cmd.summary }}</td></tr>
    {% endfor %}
  </tbody>
</table>
{% endfor %}

<h2>Canonical flag and parity reference</h2>

{% capture md %}{% include source/commands.md %}{% endcapture %}
{{ md | markdownify }}
