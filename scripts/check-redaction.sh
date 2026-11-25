#!/usr/bin/env bash
# Defensive check: no credential literal or insecure TLS option may enter the tree.
#
# Scans EVERY repository file, not just source extensions: secrets leak into
# markdown, testdata, Makefiles and config files too (AUDIT gap 4.14). Only the
# VCS metadata directory and build output are skipped. The grep rules below are
# a fast, deterministic floor; if a full-tree secret scanner is installed it is
# layered on top as defence in depth.
set -euo pipefail
cd "$(dirname "$0")/.."

# .git holds history (which may contain old commits) and dist holds build
# artifacts. Neither is part of the source tree being gated.
EX=(--exclude-dir=.git --exclude-dir=dist)

die() {
  echo "FAIL: $1" >&2
  exit 1
}

# 1. TLS verification disabled — never acceptable, in any file type.
if grep -RInE "${EX[@]}" 'InsecureSkipVerify[[:space:]]*:[[:space:]]*true' .; then
  die "InsecureSkipVerify enabled"
fi

# 2. Quoted bearer / runtime-proxy token literals.
if grep -RInE "${EX[@]}" '"(Bearer |X-Colab-Runtime-Proxy-Token: )[A-Za-z0-9_-]{20,}"' .; then
  die "possible literal credential (bearer/proxy token)"
fi

# 3. Known provider key prefixes: GitHub, AWS, GCP (API key / OAuth / generic),
#    Anthropic and OpenAI-style keys.
if grep -RInE "${EX[@]}" \
    '(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{20,}|ya29\.[0-9A-Za-z_-]{20,}|gcp-[A-Za-z0-9_-]{20,}|sk-ant-[A-Za-z0-9_-]{20,}|sk-(proj|svcacct|api)-[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9]{32,})' .; then
  die "possible literal credential (provider key)"
fi

# 4. JSON Web Tokens: three base64url segments with a JWT-shaped header/payload.
if grep -RInE "${EX[@]}" 'eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}' .; then
  die "possible literal credential (JWT)"
fi

# 5. PEM private key blocks.
if grep -RInE "${EX[@]}" 'BEGIN [A-Z ]*PRIVATE KEY' .; then
  die "possible literal credential (PEM private key)"
fi

# 6. High-entropy base64 blobs. Two anchors, both excluding *.sum: public
#    module hashes in go.sum are base64-looking but are not secrets.
#    a) a long token containing '+' (base64-specific; excludes lowercase hex
#       commit SHAs and URL/import paths, which never contain '+');
#    b) a long token assigned to a secret-ish key (catches URL-safe base64
#       where '+'/'/' do not occur).
if grep -RInE "${EX[@]}" --exclude='*.sum' \
    '[A-Za-z0-9+/]{20,}\+[A-Za-z0-9+/]{10,}={0,2}' .; then
  die "possible literal credential (high-entropy base64)"
fi
if grep -RIniE "${EX[@]}" --exclude='*.sum' \
    '(api[_-]?key|apikey|secret|token|password|passwd|credential|private[_-]?key)["'"'"'[:space:]]*[:=]["'"'"'[:space:]]*[A-Za-z0-9+/]{28,}={0,2}' .; then
  die "possible literal credential (secret-key base64 assignment)"
fi

# 7. Optional defence in depth: run a full-tree secret scanner when one is
#    available. The grep floor above still runs first as a fast pre-filter.
if command -v gitleaks >/dev/null 2>&1; then
  echo "[redaction] gitleaks available: full-tree scan" >&2
  gitleaks detect --no-banner --redact --source . --exit-code 1 \
    || die "gitleaks reported a potential secret"
fi
if command -v trufflehog >/dev/null 2>&1; then
  echo "[redaction] trufflehog available: full-tree scan" >&2
  trufflehog filesystem . --no-verification --fail \
    || die "trufflehog reported a potential secret"
fi

echo "redaction scan: OK"
