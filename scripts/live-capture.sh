#!/usr/bin/env bash
# L3 wire-capture harness — TLS key log based, GET-ONLY (docs/02 §7, AUDIT-02 §2).
#
# PURPOSE
#   Produce ground-truth fixtures for the 8 🔴 protocol unknowns using the
#   OFFICIAL Python CLI and a throw-away Google account. This script NEVER
#   allocates a VM: only GET-only read commands are run by default. No raw
#   capture is ever written inside this repository.
#
# HUMAN PRECONDITIONS (all required)
#   1. A throw-away Google account is authenticated in the Python CLI.
#   2. `tshark` (or Wireshark) is installed; CPython honours SSLKEYLOGFILE.
#   3. You accept that captured files contain secrets and must stay OUTSIDE
#      the repo. Set CAPTURE_DIR to a private directory (chmod 0700).
#   4. You have read docs/02 §7 and AUDIT-02 §2 and agree to the GET-only plan.
#
# USAGE
#   CAPTURE_DIR=/secure/path I_ACKNOWLEDGE_GET_ONLY=1 scripts/live-capture.sh
#
# After capture: REDACT (checklist below), then copy only redacted JSON into
# testdata/wire/ and update docs/02 (🔴 -> 🟢). Delete raw files at the end.
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ "${I_ACKNOWLEDGE_GET_ONLY:-}" != "1" ]]; then
  echo "refusing to run: set I_ACKNOWLEDGE_GET_ONLY=1 after reading docs/02 §7" >&2
  exit 2
fi
capture_dir="${CAPTURE_DIR:-}"
if [[ -z "$capture_dir" ]]; then
  echo "refusing to run: set CAPTURE_DIR to a private directory outside the repo" >&2
  exit 2
fi
case "$capture_dir" in
  "$PWD"|"$PWD"/*|/*golab/*|*/golab) echo "CAPTURE_DIR must be outside the repository" >&2; exit 2 ;;
esac
mkdir -p "$capture_dir"
chmod 0700 "$capture_dir" 2>/dev/null || true

py_colab="${PY_COLAB_BIN:-colab}"
command -v "$py_colab" >/dev/null 2>&1 || { echo "official colab CLI not found: $py_colab" >&2; exit 1; }
command -v tshark >/dev/null 2>&1 || echo "WARN: tshark not found; capture manually with Wireshark" >&2

keylog="$capture_dir/sslkeys.log"
pcap="$capture_dir/wire.pcapng"
: >"$keylog"
chmod 0600 "$keylog"

echo "[1/4] GET-only probes (no allocation, no mutation):"
echo "       - colab sessions            (GET /tun/m/assignments, /ccu-info)"
echo "       - colab usage               (GET /tun/m/ccu-info)"
echo "       - colab url --open=false    (no network, exercises host parsing)"
# TODO(human): start tshark in a second terminal with TLS decryption:
#   tshark -i any -o tls.keylog_file:"$keylog" -w "$pcap" \
#     -Y 'http2 or tls' &
# Leave the official CLI's HTTPS_PROXY unset; the key log decrypts REST and WS.
export SSLKEYLOGFILE="$keylog"

"$py_colab" sessions   >/dev/null 2>&1 || echo "WARN: 'sessions' failed (auth?)" >&2
"$py_colab" usage      >/dev/null 2>&1 || echo "WARN: 'usage' failed" >&2

echo "[2/4] REDACTION CHECKLIST (perform before any file enters the repo):"
cat <<'CHECKLIST'
  [ ] Authorization: Bearer <token> and X-Goog-Colab-Token removed
  [ ] runtime proxy tokens (query `token=`, X-Colab-Runtime-Proxy-Token) removed
  [ ] Set-Cookie / Cookie headers removed
  [ ] account email, GAIA id, project number, endpoint UUIDs replaced
  [ ] OAuth `code`, `code_verifier`, `id_token`, `refresh_token` removed
  [ ] local filesystem paths replaced with <TMPDIR>
  [ ] `grep -RIE '(Bearer |token=|cookie:|_token)'` over fixtures returns nothing
CHECKLIST

echo "[3/4] Derive redacted fixtures into testdata/wire/ (one JSON per endpoint):"
echo "       request: {method,path,headers(redacted),query(redacted),body?}"
echo "       response:{status,headers(redacted),body(redacted)}"
echo "       Use the skeletons in testdata/wire/README.md; never copy raw PCAPs."
echo "[4/4] Stop the tshark capture, remove '$pcap' and '$keylog', then run:"
echo "       bash scripts/golden.sh --scenario wire"
echo
echo "Capture scaffold ready in $capture_dir. NOTHING was written to the repo."
