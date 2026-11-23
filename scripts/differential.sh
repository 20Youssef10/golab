#!/usr/bin/env bash
# L4 differential harness (Python CLI vs Go CLI through an intercepting proxy).
#
# STATUS: SKELETON. The intercepting-proxy plumbing needs an external tool
# (mitmproxy / a small Go reverse proxy with a locally trusted CA) and a
# throw-away account; see docs/07 §6 and docs/CONFORMANCE.md. The scenario
# runner, normalisation and comparison below are usable today for commands
# that do not need the proxy (help/version/url); proxy-backed scenarios are
# gated behind DIFFERENTIAL_PROXY=1 with explicit TODOs.
#
# Usage: scripts/differential.sh [--scenario name ...] [--update]
set -euo pipefail

cd "$(dirname "$0")/.."

py_colab="${PY_COLAB_BIN:-colab}"
go_colab="${GO_COLAB_BIN:-./golab}"
proxy_mode="${DIFFERENTIAL_PROXY:-0}"
update=0
scenarios=()

while (( $# )); do
  case "$1" in
    --scenario) scenarios+=("${2:?--scenario needs a name}"); shift 2 ;;
    --update) update=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
if (( ${#scenarios[@]} == 0 )); then
  scenarios=(help version)
fi

if [[ ! -x "$go_colab" ]]; then
  echo "Go binary not found at '$go_colab' (run 'make build')" >&2
  exit 1
fi
command -v "$py_colab" >/dev/null 2>&1 || { echo "Python colab CLI not found: $py_colab" >&2; exit 1; }

# TODO(human/tooling): build a locally trusted CA and run an intercepting
# reverse proxy that maps colab.research.google.com and the runtime hosts to
# the in-process fakes. Export for BOTH CLIs:
#   export SSL_CERT_FILE=/path/ca.pem REQUESTS_CA_BUNDLE=/path/ca.pem
#   export HTTPS_PROXY=https://127.0.0.1:PORT
# The Python CLI hard-codes Prod(); redirecting at the network layer is the
# only reliable hook (docs/07 §6, ADR-013). Do not add a release host override.
if [[ "$proxy_mode" == "1" ]]; then
  echo "DIFFERENTIAL_PROXY=1: ensure CA + proxy are configured (see TODO above)" >&2
  : "${SSL_CERT_FILE:?set SSL_CERT_FILE to the local CA}"
  : "${HTTPS_PROXY:?set HTTPS_PROXY to the intercepting proxy}"
fi

normalise() {
  # Strip non-deterministic values; keep SGR colour only.
  sed -E \
    -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:.]+Z?/<TIMESTAMP>/g' \
    -e 's/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/<UUID>/g' \
    -e 's/\b[0-9a-f]{6}\b/<SESSION>/g' \
    -e 's/(Bearer |token=|X-Colab-Runtime-Proxy-Token: )[A-Za-z0-9._-]+/\1<TOKEN>/g' \
    -e 's#/tmp/[^ ]+#<TMPDIR>#g' \
    -e 's#(/home|/Users)/[^ ]+#<HOME>#g' \
    -e 's/\r$//'
}

run_case() {
  local impl="$1" scenario="$2"
  case "$scenario" in
    help)    "$impl" help ;;
    version) "$impl" version ;;
    *)
      # TODO(human/tooling): add the remaining >=40 scenarios from docs/07 §6
      # (new/exec/run/ls/url/usage plus every error row of doc 02 §6).
      echo "scenario '$scenario' is not implemented in this skeleton" >&2
      return 127
      ;;
  esac
}

fail=0
for scenario in "${scenarios[@]}"; do
  py_out="$(mktemp)"; go_out="$(mktemp)"
  if ! run_case "$py_colab" "$scenario" >"$py_out" 2>&1; then
    echo "[$scenario] Python CLI failed" >&2; fail=1; continue
  fi
  if ! run_case "$go_colab" "$scenario" >"$go_out" 2>&1; then
    echo "[$scenario] Go CLI failed" >&2; fail=1; continue
  fi
  normalise <"$py_out" >"$py_out.n"
  normalise <"$go_out" >"$go_out.n"
  if ! diff -u "$py_out.n" "$go_out.n"; then
    echo "[$scenario] DIFF (file an entry in testdata/golden/DELTAS.yaml if intentional)" >&2
    if (( update )); then
      cp "$go_out.n" "testdata/golden/$scenario.differential"
      echo "[$scenario] updated testdata/golden/$scenario.differential"
    fi
    fail=1
  else
    echo "[$scenario] OK"
  fi
  rm -f "$py_out" "$go_out" "$py_out.n" "$go_out.n"
done

(( fail == 0 )) || { echo "differential: FAIL" >&2; exit 1; }
echo "differential: OK"
