#!/usr/bin/env bash
# Cross-build smoke gate. Validates every artifact produced by `make cross`
# and actually executes the host-native binary.
#
# Usage: scripts/cross-smoke.sh [dist-dir]
#
# A cross-compiled binary for a foreign OS/arch cannot be executed on the host,
# so for those we verify a non-empty file with the expected object magic
# (ELF / PE / Mach-O). The host binary is executed (`version`) and its output
# is checked so a successful `go build` of a broken CLI cannot pass silently.
set -euo pipefail

dist_dir="${1:-dist}"

if [[ ! -d "$dist_dir" ]]; then
  echo "FAIL: cross-build output directory '$dist_dir' does not exist" >&2
  exit 1
fi

shopt -s nullglob
artifacts=("$dist_dir"/golab-*)
shopt -u nullglob
if (( ${#artifacts[@]} == 0 )); then
  echo "FAIL: no golab-* binaries found in '$dist_dir'" >&2
  exit 1
fi

host_os="$(go env GOOS)"
host_arch="$(go env GOARCH)"
host_bin="$dist_dir/golab-${host_os}-${host_arch}"
[[ "$host_os" == "windows" ]] && host_bin="${host_bin}.exe"

# `make cross` writes binaries without a .exe suffix; tolerate both spellings.
if [[ ! -f "$host_bin" && -f "${host_bin%.exe}" ]]; then
  host_bin="${host_bin%.exe}"
fi

fail=0
for bin in "${artifacts[@]}"; do
  name="$(basename "$bin")"
  if [[ ! -s "$bin" ]]; then
    echo "FAIL: $name is empty" >&2
    fail=1
    continue
  fi
  # Verify object magic. `file` is present on GitHub runners; fall back to od.
  if command -v file >/dev/null 2>&1; then
    desc="$(file -b "$bin")"
    case "$desc" in
      *ELF*|*PE32*|*Mach-O*) : ;;
      *) echo "FAIL: $name has unexpected object type: $desc" >&2; fail=1 ;;
    esac
  else
    magic="$(od -An -tx1 -N4 "$bin" | tr -d ' \n')"
    case "$magic" in
      7f454c46|4d5a*|cffaedfe|feedfacf|cefaedfe|feedface) : ;;
      *) echo "FAIL: $name has unknown magic: $magic" >&2; fail=1 ;;
    esac
  fi
done

if [[ ! -f "$host_bin" ]]; then
  echo "WARN: host binary $host_bin not found; skipping execution smoke" >&2
elif [[ ! -x "$host_bin" ]]; then
  echo "FAIL: host binary $host_bin is not executable" >&2
  fail=1
else
  out="$("$host_bin" version 2>/dev/null)" || {
    echo "FAIL: $host_bin version exited non-zero" >&2
    fail=1
  }
  if [[ "${out:-}" != golab\ * ]]; then
    echo "FAIL: unexpected version output: ${out:-<empty>}" >&2
    fail=1
  fi
  echo "host smoke ($host_os/$host_arch): ${out:-<empty>}"
fi

if (( fail != 0 )); then
  echo "cross-build smoke: FAIL" >&2
  exit 1
fi
echo "cross-build smoke: OK (${#artifacts[@]} artifacts)"
