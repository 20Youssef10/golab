#!/usr/bin/env bash
# L3 golden harness (docs/07 §4). Runs a scenario set against COLAB_BIN,
# normalises timestamps / tokens / UUIDs / session names / temp paths, and
# compares with checked-in fixtures under testdata/golden/<scenario>/.
#
# Usage: scripts/golden.sh [--scenario name ...] [--update]
#        scripts/golden.sh --scenario wire   # validate redacted wire fixtures
#
# Fixture layout: testdata/golden/<scenario>/{cmd,stdout,stderr,exit}
#   cmd    : one shell command per line, run with COLAB_BIN available as $BIN
#   stdout : expected normalised stdout
#   stderr : expected normalised stderr
#   exit   : expected exit code (single integer)
set -euo pipefail

cd "$(dirname "$0")/.."

COLAB_BIN="${COLAB_BIN:-./golab}"
update=0
scenarios=()
while (( $# )); do
  case "$1" in
    --scenario) scenarios+=("${2:?--scenario needs a name}"); shift 2 ;;
    --update) update=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

normalise() {
  sed -E \
    -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:.]+Z?/<TIMESTAMP>/g' \
    -e 's/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/<UUID>/g' \
    -e 's/\b[0-9a-f]{6}\b/<SESSION>/g' \
    -e 's/(Bearer |token=|X-Colab-Runtime-Proxy-Token: |"access_token"[[:space:]]*:[[:space:]]*")[A-Za-z0-9._-]+/\1<TOKEN>/g' \
    -e 's#/tmp/[^ "'"'"']+#<TMPDIR>#g' \
    -e 's/\r$//'
}

# Special scenario: only validate that redacted wire fixtures exist and carry
# no obvious secret material (they are produced by scripts/live-capture.sh).
if (( ${#scenarios[@]} == 1 )) && [[ "${scenarios[0]}" == "wire" ]]; then
  shopt -s nullglob
  files=(testdata/wire/*.json)
  if (( ${#files[@]} == 0 )); then
    echo "golden(wire): no fixtures yet; see testdata/wire/README.md (HUMAN REQUIRED)" >&2
    exit 1
  fi
  if grep -RIE 'Bearer [A-Za-z0-9]|token=[A-Za-z0-9]{12}|cookie:|refresh_token' testdata/wire/*.json; then
    echo "golden(wire): FAIL - raw secret material in fixtures" >&2
    exit 1
  fi
  echo "golden(wire): OK (${#files[@]} redacted fixtures)"
  exit 0
fi

if (( ${#scenarios[@]} == 0 )); then
  shopt -s nullglob
  for d in testdata/golden/*/; do
    [[ -f "$d/cmd" ]] && scenarios+=("$(basename "$d")")
  done
fi
if (( ${#scenarios[@]} == 0 )); then
  echo "golden: no scenarios found; add testdata/golden/<scenario>/{cmd,stdout,stderr,exit}" >&2
  exit 1
fi

fail=0
for scenario in "${scenarios[@]}"; do
  dir="testdata/golden/$scenario"
  if [[ ! -f "$dir/cmd" ]]; then
    echo "[$scenario] missing $dir/cmd" >&2; fail=1; continue
  fi
  out="$(mktemp)"; err="$(mktemp)"; code_file="$(mktemp)"
  BIN="$COLAB_BIN"
  export BIN
  while IFS= read -r cmdline || [[ -n "$cmdline" ]]; do
    [[ -z "$cmdline" || "$cmdline" == \#* ]] && continue
    # Commands are fixed fixtures checked into the repo, never user input.
    # shellcheck disable=SC2086
    bash -c "$cmdline" >>"$out" 2>>"$err" || echo "$?" >"$code_file"
  done <"$dir/cmd"
  normalise <"$out" >"$out.n"
  normalise <"$err" >"$err.n"
  code="$(cat "$code_file" 2>/dev/null || echo 0)"

  if (( update )); then
    mkdir -p "$dir"
    cp "$out.n" "$dir/stdout"; cp "$err.n" "$dir/stderr"; echo "$code" >"$dir/exit"
    echo "[$scenario] updated fixture (review the diff!)"
  else
    if [[ "$(cat "$dir/exit" 2>/dev/null || echo '<missing>')" != "$code" ]]; then
      echo "[$scenario] exit mismatch: $(cat "$dir/exit" 2>/dev/null || echo '<missing>') != $code" >&2
      fail=1
    fi
    if ! diff -u "$dir/stdout" "$out.n"; then
      echo "[$scenario] stdout mismatch" >&2; fail=1
    fi
    if ! diff -u "$dir/stderr" "$err.n"; then
      echo "[$scenario] stderr mismatch" >&2; fail=1
    fi
    (( fail == 0 )) && echo "[$scenario] OK"
  fi
  rm -f "$out" "$err" "$code_file" "$out.n" "$err.n"
done

(( fail == 0 )) || { echo "golden: FAIL" >&2; exit 1; }
echo "golden: OK"
