---
layout: default
title: Spend safety
lede: "Billable work is gated before allocation: validate hardware and scripts locally, inspect account-level CU, set TTLs, and reap orphaned runtimes."
source_file: docs/USAGE.md
source_section: "Spend guard for run · golab reap"
permalink: /spend-safety/
---

<table>
  <thead><tr><th>Control</th><th>What it does</th><th>Limit</th></tr></thead>
  <tbody>
    <tr><td><code>golab new --ttl D</code></td><td>Records a local expiry checked on every invocation.</td><td>No daemon; use <code>reap</code> for server-side orphans.</td></tr>
    <tr><td><code>golab cost</code></td><td>Reports observed compute units and rate.</td><td>CU only, account-level, no prices.</td></tr>
    <tr><td><code>golab run --max-cu N --enforce</code></td><td>Guards a run against the same account-level balance.</td><td>Fails closed if usage cannot be read.</td></tr>
    <tr><td><code>golab reap --dry-run</code></td><td>Previews unassignment of server runtimes with no local record.</td><td>Review the preview before running it for real.</td></tr>
    <tr><td><code>golab budget</code></td><td>Shows an advisory decision without allocating.</td><td>Advisory; the guard, not this command, enforces.</td></tr>
  </tbody>
</table>

<p>
  Validate accelerators, scripts and flags <em>before</em> any allocation. There is no
  <code>--debug</code> flag: use <code>COLAB_DEBUG=1</code>. <code>--explain</code>
  prints a redacted dry-run plan for billable commands, and <code>--yes</code> never
  bypasses a budget guard.
</p>

<h2>Canonical spend sections</h2>

{% capture md %}{% include source/spend.md %}{% endcapture %}
{{ md | markdownify }}
