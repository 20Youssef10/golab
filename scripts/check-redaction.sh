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
# Config, workflow, script and JSON files can leak credentials too. Keep the
# shapes narrow: provider token prefixes, PEM private keys and quoted
# bearer/proxy-token literals. The public OAuth client_id/client_secret in
# internal/auth/oauth_config.json and this script's own pattern text must not
# trip the gate (verified by running the script against the current tree).
if grep -RIn --exclude-dir=.git \
    --include='*.yml' --include='*.yaml' --include='*.sh' --include='*.json' \
    -E '(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|BEGIN [A-Z ]*PRIVATE KEY|"(Bearer |X-Colab-Runtime-Proxy-Token: )[A-Za-z0-9_-]{20,}")' .; then
  echo "FAIL: possible literal credential found in config/script/data file" >&2; exit 1
fi
echo "redaction scan: OK"
