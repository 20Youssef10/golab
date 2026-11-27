#!/usr/bin/env bash
# Build the ONE flat member that carries the Agent Skills in every release
# archive. GoReleaser calls this from before.hooks; the resulting file lives in
# /tmp (never in dist, which GoReleaser cleans after before hooks) and is
# added to each archive with dst: golab-skills.tar.gz.
#
# The outer archive must stay FLAT: a v0.2.0 binary's safeArchivePath rejects
# any member containing "/", so nested .agents/skills/... members broke signed
# self-update to v0.3.0. This nested tar.gz is simply discarded by the updater.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="/tmp/golab-skills.tar.gz"
tmp="$(mktemp /tmp/golab-skills.XXXXXX)"
trap 'rm -f "$tmp"' EXIT

[[ -d "$root/.agents/skills/golab-operations" ]] || { echo "missing golab-operations skill" >&2; exit 1; }
[[ -d "$root/.agents/skills/colab-mcp" ]] || { echo "missing colab-mcp skill" >&2; exit 1; }

# Reproducible bytes: sorted names, fixed mtime/owner/mode and a gzip stream
# without a timestamp or original filename.
tar --sort=name --format=gnu --owner=0 --group=0 --numeric-owner \
  --mtime='@0' --mode=0644 -cf "$tmp" \
  -C "$root/.agents/skills" golab-operations colab-mcp
gzip -n -c "$tmp" > "$out"
printf 'created %s\n' "$out" >&2
