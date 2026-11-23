#!/usr/bin/env bash
# Remote paths use `path`; local paths use `filepath` (AGENTS.md #10).
#
# internal/contents/contents.go deals exclusively with VM-side paths, so
# filepath there is always a bug. internal/contents/recursive.go (recursive
# upload/download) legitimately mixes a LOCAL tree (filepath) with REMOTE paths
# (path.Join/path.Base) - it is excluded from the blanket scan and instead
# checked for the positive use of `path.Join`. internal/cli/files.go also
# legitimately mixes both (LOCAL upload argument and the local temp file used
# by `edit`) and is reviewed by hand.
set -euo pipefail
cd "$(dirname "$0")/.."

if grep -RIn --include='*.go' \
    --exclude='recursive.go' --exclude='recursive_test.go' \
    'filepath\.' internal/contents; then
  echo "FAIL: filepath used for a VM-side path" >&2
  exit 1
fi

# Recursive transfers must build the remote side with `path`, never by
# concatenating filepath results with OS separators.
if ! grep -q 'path\.Join' internal/contents/recursive.go; then
  echo "FAIL: internal/contents/recursive.go does not use path.Join for remote paths" >&2
  exit 1
fi

echo "remote-path scan: OK"
