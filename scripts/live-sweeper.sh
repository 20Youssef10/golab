#!/usr/bin/env bash
# Live-e2e orphan/leak sweeper (docs/07 §9 safety rule 2).
#
# Lists Colab sessions and unassigns those older than MAX_AGE_MIN (default 60).
# DRY RUN BY DEFAULT: set APPLY=1 to actually call `colab stop`.
#
# The binary prints named sessions on stdout (formatSessionLine) and the
# "[colab] No active sessions found on server." notice on stderr when empty.
# Orphans are rendered with the name "?" and have no local session record, so
# `colab stop -s` cannot target them; they are reported for manual cleanup
# (TODO: a future CLI flag for unassigning by endpoint would close this gap).
set -euo pipefail

COLAB_BIN="${COLAB_BIN:-colab}"
max_age="${MAX_AGE_MIN:-60}"
apply="${APPLY:-0}"

if ! command -v "$COLAB_BIN" >/dev/null 2>&1 && [[ ! -x "$COLAB_BIN" ]]; then
  echo "sweeper: COLAB_BIN '$COLAB_BIN' not found" >&2
  exit 1
fi

stderr_file="$(mktemp)"
trap 'rm -f "$stderr_file"' EXIT
stdout="$("$COLAB_BIN" sessions 2>"$stderr_file")"
stderr="$(cat "$stderr_file")"

if [[ -z "$stdout" ]]; then
  echo "sweeper: no assignments (${stderr:-no message})"
  exit 0
fi

echo "sweeper: current assignments:"
echo "$stdout"

state_file="${COLAB_SESSIONS_FILE:-}"
if [[ -z "$state_file" ]]; then
  for candidate in "$HOME/.config/colab-cli/sessions.json" "${XDG_CONFIG_HOME:-$HOME/.config}/colab-cli/sessions.json"; do
    if [[ -f "$candidate" ]]; then state_file="$candidate"; break; fi
  done
fi

now="$(date +%s)"
stopped=0
orphans=0
while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  # formatSessionLine: "<name>  <endpoint>  ..." (whitespace separated).
  name="$(printf '%s\n' "$line" | awk '{print $1}')"
  endpoint="$(printf '%s\n' "$line" | awk '{print $2}')"

  if [[ "$name" == "?" || -z "$endpoint" ]]; then
    echo "sweeper: ORPHAN endpoint=$endpoint has no local name; needs manual unassign" >&2
    orphans=$((orphans + 1))
    continue
  fi

  # Age source: per-session CreatedAt is authoritative; fall back to the
  # sessions.json mtime (all sessions share it). TODO: use per-session
  # CreatedAt once exposed via `colab sessions --json`.
  age_min=""
  if [[ -n "$state_file" && -f "$state_file" ]]; then
    mtime="$(date -r "$state_file" +%s 2>/dev/null || stat -c %Y "$state_file" 2>/dev/null || echo "$now")"
    age_min=$(( (now - mtime) / 60 ))
  fi
  if [[ -n "$age_min" ]] && (( age_min < max_age )); then
    echo "sweeper: '$name' is ${age_min}m old (< ${max_age}m), keeping"
    continue
  fi

  if [[ "$apply" == "1" ]]; then
    echo "sweeper: unassigning '$name' (age ${age_min:-unknown}m)"
    "$COLAB_BIN" stop -s "$name" >/dev/null 2>&1 || echo "sweeper: stop '$name' failed" >&2
    stopped=$((stopped + 1))
  else
    echo "sweeper: DRY-RUN would stop '$name' (age ${age_min:-unknown}m)"
  fi
done <<<"$stdout"

echo "sweeper: stopped=$stopped orphans=$orphans apply=$apply"
# Orphans are billable leaks: fail so a human notices, even in dry-run.
if (( orphans > 0 )); then
  echo "sweeper: FAIL - $orphans orphan(s) need explicit unassign (AGENTS.md live-test rules)" >&2
  exit 1
fi
