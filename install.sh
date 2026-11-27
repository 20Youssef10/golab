#!/usr/bin/env bash
# Install golab AND golab-mcp ONLY from a release whose SHA-256 checksum and
# cosign keyless signature have both been verified against the pinned release
# identity. There is deliberately no curl|bash path: downloaded bytes are
# written to a temporary file, verified, and only then extracted and installed.
set -euo pipefail

OWNER="20Youssef10"
REPO="golab"
ISSUER="https://token.actions.githubusercontent.com"
IDENTITY_REGEXP='^https://github\.com/20Youssef10/golab/\.github/workflows/release\.yml@refs/tags/v'
SKILLS=(golab-operations colab-mcp)

die() { printf 'install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command '$1' not found"; }

usage() {
  cat >&2 <<'EOF'
usage: install.sh [--version X.Y.Z] [--dir PATH] [--skills-dir PATH] [--no-skills]
       install.sh --verify-only ARTIFACT BUNDLE CHECKSUMS

Installs golab, golab-mcp and (unless --no-skills) the two Agent Skills from
the same signed archive into a documented skills directory.

Environment: GOLAB_VERSION, GOLAB_INSTALL_DIR (defaults to $HOME/.local/bin),
  GOLAB_SKILLS_DIR (defaults to ${XDG_DATA_HOME:-$HOME/.local/share}/golab/skills).
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
skills_dir="${GOLAB_SKILLS_DIR:-${XDG_DATA_HOME:-${HOME}/.local/share}/golab/skills}"
install_skills=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || die "--version needs a value"; version="$2"; shift 2 ;;
    --dir) [[ $# -ge 2 ]] || die "--dir needs a value"; install_dir="$2"; shift 2 ;;
    --skills-dir) [[ $# -ge 2 ]] || die "--skills-dir needs a value"; skills_dir="$2"; shift 2 ;;
    --no-skills) install_skills=0; shift ;;
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
# Extract ONLY known flat members: no arbitrary nested path from the outer
# archive is ever written to disk (and a v0.2.0 updater discards the bundle).
tar -xzf "$tmp/$asset" -C "$tmp" golab golab-mcp
tar -xzf "$tmp/$asset" -C "$tmp" golab-skills.tar.gz
for binary in golab golab-mcp; do
  [[ -f "$tmp/$binary" && -x "$tmp/$binary" ]] || die "verified archive has no executable $binary"
done
mkdir -p "$install_dir"
for binary in golab golab-mcp; do
  install -m 0755 "$tmp/$binary" "$install_dir/$binary"
  [[ -x "$install_dir/$binary" ]] || die "installed $binary is not executable"
  # Smoke test the bytes we just installed, not the downloaded archive.
  # Cobra exposes --help on golab; golab-mcp uses Go's standard flag package.
  "$install_dir/$binary" --help >/dev/null 2>&1 || die "$binary --help failed after install"
done
printf 'install: installed golab and golab-mcp %s to %s\n' "$ver" "$install_dir"

if (( install_skills )); then
  bundle="$tmp/golab-skills.tar.gz"
  [[ -f "$bundle" ]] || die "verified archive has no golab-skills.tar.gz"
  # Defence in depth: the bundle is inside signed bytes, but still refuse to
  # extract anything outside the two known skill trees.
  while IFS= read -r member; do
    case "$member" in
      golab-operations|golab-operations/*|colab-mcp|colab-mcp/*) ;;
      *) die "unexpected member in skills bundle: $member" ;;
    esac
  done < <(tar -tzf "$bundle")
  mkdir -p "$skills_dir"
  # Overwrite files in place; never delete the user's local edits.
  tar -xzf "$bundle" -C "$skills_dir" golab-operations colab-mcp
  for skill in "${SKILLS[@]}"; do
    [[ -f "$skills_dir/$skill/SKILL.md" ]] || die "failed to install skill $skill"
  done
  printf 'install: installed skills %s to %s\n' "${SKILLS[*]}" "$skills_dir"
else
  printf 'install: skipped skills (--no-skills)\n' >&2
fi
