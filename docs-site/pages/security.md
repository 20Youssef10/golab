---
layout: default
title: Security & redaction
lede: "Secrets never reach logs, argv, environment dumps or errors. State writes are atomic; TLS verification is never disabled in release builds."
source_file: docs/08-risks-decisions-security.md
source_section: "3. Security model · USAGE §10"
permalink: /security/
---

<h2>Invariants enforced by the code and CI</h2>

<table>
  <thead><tr><th>Invariant</th><th>Where it is checked</th></tr></thead>
  <tbody>
    <tr><td>Tokens, OAuth codes/verifiers, client secrets and <code>Authorization</code>/<code>Cookie</code>/proxy-token headers never reach logs, argv, env dumps or errors.</td><td><code>Secret</code> type + <code>scripts/check-redaction.sh</code></td></tr>
    <tr><td>No <code>InsecureSkipVerify</code>; no release flag or env var that changes Colab hosts.</td><td>Redaction and host gates in CI</td></tr>
    <tr><td><code>sessions.json</code> / <code>settings.json</code> writes are locked, temp-file, fsync, rename; a corrupt file aborts instead of resetting.</td><td><code>internal/state</code></td></tr>
    <tr><td>Remote paths use <code>path</code>, local paths use <code>filepath</code>.</td><td><code>scripts/check-paths.sh</code></td></tr>
    <tr><td>Scripts, flags, accelerators and keys are validated before allocation; user strings are JSON/<code>repr</code>-quoted, never spliced into VM code.</td><td>Command implementations and tests</td></tr>
  </tbody>
</table>

<h2>Security model</h2>

{% capture model %}{% include source/security-model.md %}{% endcapture %}
{{ model | markdownify }}

<h2>User-facing security and privacy notes</h2>

{% capture md %}{% include source/security.md %}{% endcapture %}
{{ md | markdownify }}

<p>
  Report security issues privately via
  <a href="https://github.com/20Youssef10/golab/blob/main/SECURITY.md">SECURITY.md</a>,
  not in a public issue.
</p>
