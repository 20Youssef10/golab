#!/usr/bin/env bash
# Goleak wiring gate (docs/07 §11 / AUDIT-03 §4.2).
#
# A goroutine leak in a websocket/goroutine-heavy package can keep a
# fire-and-forget CLI alive after its work is done. This script checks that
# `go.uber.org/goleak` is a direct dependency and that the four packages
# called out in AUDIT-03 have an active goleak check in their tests.
#
# Missing wiring is always a FAIL: there is no advisory mode. CI's
# `go test -race ./...` executes these checks, so a leaked goroutine fails
# the build on the runners (the race detector itself is host-dependent).
set -euo pipefail

cd "$(dirname "$0")/.."
fail=0

if ! grep -q 'go.uber.org/goleak' go.mod; then
  echo "FAIL: go.uber.org/goleak is not a direct dependency in go.mod" >&2
  fail=1
fi

# runtime/sshx/repl have no pre-existing TestMain, so they wire the
# end-of-suite goleak check directly through TestMain.
for pkg in runtime sshx repl; do
  if ! grep -Rqs --include='*_test.go' 'goleak.VerifyTestMain' "internal/$pkg"; then
    echo "FAIL: internal/$pkg has no TestMain calling goleak.VerifyTestMain" >&2
    fail=1
  fi
done

# update already has a TestMain in helpers_test.go (owned by another agent):
# a second one would not compile, so its goleak check is a regular test that
# calls VerifyNone during `go test`. Verify the active check instead.
if ! grep -Rqs --include='*_test.go' 'goleak.VerifyNone' internal/update; then
  echo "FAIL: internal/update has no test calling goleak.VerifyNone" >&2
  fail=1
fi

if (( fail != 0 )); then
  echo "goleak check: FAIL" >&2
  exit 1
fi
echo "goleak check: OK"
