#!/usr/bin/env bash
# Goleak wiring check (docs/07 §11 / AUDIT-03 §4.2).
#
# A goroutine leak in a websocket/goroutine-heavy package can keep a
# fire-and-forget CLI alive after its work is done. This script checks that
# `go.uber.org/goleak` is a direct dependency and that the four packages
# called out in AUDIT-03 have a TestMain that calls goleak.VerifyTestMain.
#
# By default a missing wiring is a FAIL (a real gate). CI currently invokes
# this with ADVISORY=1 while the Go-side wiring lands; remove that override
# once the TestMains exist. Once wired, `go test -race ./...` exercises it.
set -euo pipefail

cd "$(dirname "$0")/.."
advisory="${ADVISORY:-0}"
fail=0

if ! grep -q 'go.uber.org/goleak' go.mod; then
  echo "FAIL: go.uber.org/goleak is not a direct dependency in go.mod" >&2
  fail=1
fi

for pkg in runtime sshx repl update; do
  if ! grep -Rqs --include='*_test.go' 'goleak.VerifyTestMain' "internal/$pkg"; then
    echo "FAIL: internal/$pkg has no TestMain calling goleak.VerifyTestMain" >&2
    fail=1
  fi
done

if (( fail != 0 )); then
  if [[ "$advisory" == "1" ]]; then
    echo "goleak check: ADVISORY (would fail as a gate; wiring still pending)" >&2
    exit 0
  fi
  echo "goleak check: FAIL" >&2
  exit 1
fi
echo "goleak check: OK"
