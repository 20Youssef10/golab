#!/usr/bin/env bash
# Coverage gate (docs/07 §11 / AUDIT-03 §4.2).
#
# Usage: scripts/coverage-gate.sh [coverage.out]
#
# Aggregates statement coverage per Go package from a profile produced by
# `go test -coverprofile=coverage.out ./...` (use `-covermode=atomic`). The
# default minimum is 80% for the five gate packages named in docs/07 §11;
# override with COVERAGE_MIN (global) or COVERAGE_MIN_<pkg> (e.g.
# COVERAGE_MIN_sshx=75). No profile entry for a package is treated as 0%.
set -euo pipefail

cd "$(dirname "$0")/.."
profile="${1:-dist/coverage.out}"
if [[ ! -f "$profile" ]]; then
  echo "FAIL: coverage profile '$profile' not found (run 'make coverage')" >&2
  exit 1
fi

default_min="${COVERAGE_MIN:-80}"
# Gate packages from docs/07 §11. Extend here as more packages are gated.
packages=(colabapi runtime state sshx auth)

# Aggregate by package directory using statement counts (accurate, not a mean
# of per-function percentages). Output: "<pkg> <pct> <covered> <total>".
report="$(awk '
  /^mode:/ { next }
  NF >= 3 {
    loc = $1; stmts = $2 + 0; count = $3 + 0
    n = split(loc, a, ":")
    file = a[1]
    m = split(file, parts, "/")
    # Gate packages are addressed as internal/<pkg>; keep the last two
    # components so module paths of any depth work.
    # parts[m] is the filename; parts[m-1] is the package dir (colabapi),
    # parts[m-2] the parent (internal) -> "internal/colabapi".
    pkg = (m >= 3 ? parts[m-2] "/" parts[m-1] : file)
    total[pkg] += stmts
    if (count > 0) covered[pkg] += stmts
  }
  END {
    for (pname in total) {
      pct = total[pname] > 0 ? (100 * covered[pname] / total[pname]) : 0
      printf "%s %.2f %d %d\n", pname, pct, covered[pname], total[pname]
    }
  }
' "$profile")"

fail=0
printf '%-12s %8s %8s %s\n' PACKAGE COVERAGE MINIMUM STATUS
for pkg in "${packages[@]}"; do
  var="COVERAGE_MIN_${pkg}"
  min="${!var:-$default_min}"
  row="$(printf '%s\n' "$report" | awk -v want="internal/$pkg" '$1 == want { print; exit }')"
  if [[ -z "$row" ]]; then
    pct="0.00"; covered=0; totald=0
  else
    read -r _ pct covered totald <<<"$row"
  fi
  status="PASS"
  if awk -v p="$pct" -v m="$min" 'BEGIN { exit !(p + 0 < m + 0) }'; then
    status="FAIL"
    fail=1
  fi
  printf '%-12s %7s%% %7s%% %s (%s/%s stmts)\n' "$pkg" "$pct" "$min" "$status" "$covered" "$totald"
done

if (( fail != 0 )); then
  echo "coverage gate: FAIL (docs/07 §11 target is ${default_min}%)" >&2
  exit 1
fi
echo "coverage gate: OK"
