#!/usr/bin/env bash
# Install golab ONLY from a release whose SHA-256 checksum and cosign keyless
# signature have both been verified against the pinned release identity.
# There is deliberately no curl|bash path: downloaded bytes are written to a
# temporary file, verified, and only then extracted and installed.
set -euo pipefail

OWNER="20Youssef10"
REPO="golab"
ISSUER="https://token.actions.githubusercontent.com"
IDENTITY_REGEXP='^https://github\.com/20Youssef10/golab/\.github/workflows/release\.yml@refs/tags/v'

die() { printf 'install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command '$1' not found"; }

usage() {
  cat >&2 <<'EOF'
usage: install.sh [--version X.Y.Z] [--dir PATH]
       install.sh --verify-only ARTIFACT BUNDLE CHECKSUMS

Environment: GOLAB_VERSION, GOLAB_INSTALL_DIR (defaults to $HOME/.local/bin).
Requirements: curl, tar, sha256sum, cosign (the signature verifier).
EOF
}

# verify_local is the single gate used by both the installer and CI.
verify_local() {
  local artifact="$1" bundle="$2" checksums="$3"
  need sha256sum
  need cosign
  [[ -f "$artifact" ]] || die "artifact not found: $artifact"
  [[ -f "$bundle" ]] || die "cosign bundle not found: $bundle"
  [[ -f "$checksums" ]] || die "checksums file not found: $checksums"
  local name expected actual
  name="$(basename "$artifact")"
  expected="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "$checksums")"
  [[ -n "$expected" ]] || die "no checksum entry for $name"
  actual="$(sha256sum "$artifact" | awk '{ print $1 }')"
  [[ "$actual" == "$expected" ]] || die "checksum mismatch for $name"
  # Keyless verification pins BOTH the OIDC issuer and the release workflow
  # identity at a tag. A cert from any other workflow/run cannot pass.
  cosign verify-blob "$artifact" \
    --bundle "$bundle" \
    --certificate-oidc-issuer "$ISSUER" \
    --certificate-identity-regexp "$IDENTITY_REGEXP" \
    || die "cosign signature verification failed for $name"
  printf 'install: verified checksum and cosign signature for %s\n' "$name" >&2
}

if [[ "${1:-}" == "--verify-only" ]]; then
  [[ $# -eq 4 ]] || { usage; die "--verify-only expects ARTIFACT BUNDLE CHECKSUMS"; }
  verify_local "$2" "$3" "$4"
  exit 0
fi

version="${GOLAB_VERSION:-}"
install_dir="${GOLAB_INSTALL_DIR:-${HOME}/.local/bin}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || die "--version needs a value"; version="$2"; shift 2 ;;
    --dir) [[ $# -ge 2 ]] || die "--dir needs a value"; install_dir="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

need curl
need tar
need mktemp

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux) os="linux" ;;
  darwin) os="darwin" ;;
  *) die "unsupported OS for install.sh: $os (use install.ps1 on Windows)" ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  i386|i686) arch="386" ;;
  armv7l|armv6l) arch="arm" ;;
  *) die "unsupported architecture: $arch" ;;
esac

if [[ -z "$version" ]]; then
  release_json="$(curl -fsSL "https://api.github.com/repos/${OWNER}/${REPO}/releases/latest")"
  tag="$(printf '%s\n' "$release_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
  [[ -n "$tag" ]] || die "could not read tag_name from the latest release"
else
  tag="v${version#v}"
fi
ver="${tag#v}"
asset="golab_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/${OWNER}/${REPO}/releases/download/${tag}"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/golab-install.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/$asset" "${base}/${asset}"
curl -fsSL -o "$tmp/checksums.txt" "${base}/checksums.txt"
curl -fsSL -o "$tmp/${asset}.sigstore.json" "${base}/${asset}.sigstore.json"
verify_local "$tmp/$asset" "$tmp/${asset}.sigstore.json" "$tmp/checksums.txt"

# Extraction happens only after verification and never executes archive content.
tar -xzf "$tmp/$asset" -C "$tmp"
[[ -f "$tmp/golab" ]] || die "verified archive has no golab executable"
mkdir -p "$install_dir"
install -m 0755 "$tmp/golab" "$install_dir/golab"
printf 'install: installed golab %s to %s/golab\n' "$ver" "$install_dir"
