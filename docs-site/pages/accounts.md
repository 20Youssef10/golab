---
layout: default
title: Accounts and Web UI sessions
lede: "Named Google accounts with isolated session stores, plus browser-created runtimes discoverable and attachable from either control plane."
permalink: /accounts/
---

<div class="card-grid">
  <a class="card" href="https://github.com/20Youssef10/golab/blob/main/docs/10-accounts-and-web-sessions.md"><strong>Canonical guide</strong><span>Account model, commands, safety rules and verification status.</span></a>
  <a class="card" href="{{ '/migration/' | relative_url }}"><strong>Migrating from Python colab</strong><span>Compatibility and state-file notes.</span></a>
</div>

<h2>Accounts at a glance</h2>

<table>
  <thead><tr><th>Concept</th><th>Storage</th><th>Compatibility</th></tr></thead>
  <tbody>
    <tr><td>Account registry</td><td><code>accounts.json</code>, mode 0600, atomic write</td><td>Missing file ⇒ <code>default</code></td></tr>
    <tr><td>Default token / sessions</td><td>Legacy keyring entry and <code>sessions.json</code></td><td><strong>Unchanged</strong></td></tr>
    <tr><td>Named account token / sessions</td><td>Per-account keyring user and derived session file</td><td>New</td></tr>
  </tbody>
</table>

<pre><code>golab account add NAME      # OAuth consent is human-only
golab account list          # --json supported
golab account show NAME
golab account use NAME
golab account remove NAME   # refuses the active / last account without override

GOLAB_ACCOUNT=work golab sessions
golab --account work sessions</code></pre>

<h2>Browser-created sessions</h2>

<p>
  <code>golab sessions --web</code> lists runtimes created in the Colab Web UI
  using the same read-only <code>List</code> call on both planes. The legacy
  tunnel path needs no GCP setup; the Public API path keeps its documented,
  human-only quota-project prerequisites.
</p>

<pre><code>golab sessions --web
golab attach --all
golab attach --runtime runtimes/r-abc --session mynotebook
golab attach --runtime runtimes/r-abc --refresh</code></pre>

<p>
  <code>attach</code> never calls <code>POST /assign</code> and never allocates a
  runtime: it writes a local session record from data the authenticated List
  already returned. <code>--all</code> is idempotent and <code>--refresh</code>
  renews an expired proxy token without changing the session name.
</p>

<h2>Safety</h2>

<ul>
  <li>Confirm the runtime id and active account before <code>stop</code> — a Web UI runtime is a billable resource.</li>
  <li>Never paste proxy tokens, cookies or OAuth codes into tool arguments; <code>attach</code> obtains them from the authenticated List.</li>
  <li><code>account remove</code> deletes local credentials only; it does not stop remote runtimes.</li>
  <li>Session names are local to an account; the active account decides which one a command addresses.</li>
</ul>
