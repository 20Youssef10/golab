#!/usr/bin/env bash
# Defensive check: no token literal or insecure TLS option may enter the tree.
set -euo pipefail
cd "$(dirname "$0")/.."
if grep -RIn --include='*.go' -E 'InsecureSkipVerify[[:space:]]*:[[:space:]]*true' .; then
  echo "FAIL: InsecureSkipVerify found" >&2; exit 1
fi
if grep -RIn --include='*.go' -E '"(Bearer |X-Colab-Runtime-Proxy-Token: )[A-Za-z0-9_-]{20,}"' .; then
  echo "FAIL: possible literal credential found" >&2; exit 1
fi
echo "redaction scan: OK"
