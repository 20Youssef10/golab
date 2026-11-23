#!/usr/bin/env bash
# Remote paths use `path`; local paths use `filepath` (AGENTS.md #10).
# internal/contents deals exclusively with VM-side paths, so filepath there is
# always a bug. internal/cli/files.go legitimately mixes both (upload's LOCAL
# argument and the local temp file used by `edit`) and is reviewed by hand.
set -euo pipefail
cd "$(dirname "$0")/.."
if grep -RIn --include='*.go' 'filepath\.' internal/contents; then
  echo "FAIL: filepath used for a VM-side path" >&2; exit 1
fi
echo "remote-path scan: OK"
