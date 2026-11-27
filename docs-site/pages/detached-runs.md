---
layout: default
title: Detached runs (ADR-001)
lede: "Submit long work to the VM, close the local process, and come back later with <code>jobs</code> and <code>logs</code>. No daemon, no keep-alive."
source_file: docs/adr-001-detached-runs.md
source_section: "Context · Decision · Consequences · Verification"
permalink: /detached-runs/
---

```bash
golab run --detach --ttl 1h train.py       # remote child; the VM stays allocated
golab jobs                                 # find the detached run ID
golab logs <ID> -f --interval 2s --max 30  # inspect output on demand
golab stop -s batch                        # ending the job does not stop the VM
```

<p>
  <code>--ttl</code> is mandatory with <code>--detach</code> and enforced remotely by a
  5-second watchdog (the job exits <code>124</code> with
  <code>ttl-reason=ttl_exceeded</code>). <code>--max-cu</code> is foreground-only; combining
  it with <code>--detach</code> is rejected. The watchdog ends the <em>job</em>, not the
  runtime — you still have to <code>golab stop</code> the VM.
</p>

{% capture md %}{% include source/detached.md %}{% endcapture %}
{{ md | markdownify }}
