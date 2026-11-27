---
layout: default
title: Migrating from Python colab
lede: "Switch to the Go binary without re-authenticating: same config directory and state schema, with explicit differences in output, paths and newer flags."
source_file: docs/MIGRATION.md
source_section: "whole document"
permalink: /migration/
---

{% capture md %}{% include source/migration.md %}{% endcapture %}
{{ md | markdownify }}
