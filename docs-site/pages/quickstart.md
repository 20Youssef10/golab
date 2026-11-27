---
layout: default
title: Quickstart
lede: "Install the static binary, allocate a runtime, run one piece of Python, and release the VM."
source_file: docs/USAGE.md
source_section: "Conventions used throughout · 1. Session lifecycle"
permalink: /quickstart/
---

```bash
# Canonical Go module path (or download a signed release archive)
go install github.com/20Youssef10/golab/cmd/golab@latest

golab new -s demo --gpu t4                      # allocate a named T4 runtime
echo 'print(2 + 2)' | golab exec -s demo        # run code from stdin
golab run train.py --gpu t4                     # fresh VM, run script, release it
golab sessions                                  # list what is still allocated
golab stop -s demo                              # shut it down
```

<p>
  With one active session you may omit <code>-s</code>; <code>golab</code> selects it and
  says which one it picked on stderr. Program output goes to stdout, diagnostics to
  stderr, so all of the above are pipe-safe. See the
  <a href="{{ '/distribution/' | relative_url }}">install and distribution page</a> for
  release verification and package-manager status.
</p>

{% capture md %}{% include source/quickstart.md %}{% endcapture %}
{{ md | markdownify }}
