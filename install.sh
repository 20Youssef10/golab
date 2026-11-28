#!/usr/bin/env bash
# Install golab AND golab-mcp ONLY from a release whose SHA-256 checksum and
# cosign keyless signature have both been verified against the pinned release
# identity. There is deliberately no curl|bash path: downloaded bytes are
# written to a temporary file, verified, and only then extracted and installed.
#
# This installer is self-bootstrapping: no curl, tar, mktemp, sha256sum or
# cosign needs to be preinstalled. Each capability has a fallback chain and
# the verifier is downloaded from a pinned sigstore release when absent/old.
# Every fallback keeps the checksum + cosign trust gate intact.
set -euo pipefail
umask 077

OWNER="20Youssef10"
REPO="golab"
ISSUER="https://token.actions.githubusercontent.com"
IDENTITY_REGEXP='^https://github\.com/20Youssef10/golab/\.github/workflows/release\.yml@refs/tags/v'
SKILLS=(golab-operations colab-mcp)

die() { printf 'install: %s\n' "$*" >&2; exit 1; }
have_cmd() { command -v "$1" >/dev/null 2>&1; }

# v0.3.0 sigstore bundles use cosign's --new-bundle-format. cosign < 2.6.5
# cannot read them and reports a misleading "signature verification failed",
# so reject old clients BEFORE the verification gate with an actionable error.
MIN_COSIGN_VERSION="2.6.5"
COSIGN_UPGRADE="upgrade with 'go install github.com/sigstore/cosign/v2/cmd/cosign@latest' (see https://docs.sigstore.dev/cosign/installation/)"

# Pinned verifier release. When the host cosign is missing or older than
# MIN_COSIGN_VERSION we download this exact static binary, verify it against
# the release's published checksums.txt AND against the digests below, and use
# it only for this installation (never written to the install/skills dirs).
COSIGN_VERSION="v2.6.5"
COSIGN_BASE="https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}"
COSIGN_CHECKSUMS_URL="${COSIGN_BASE}/cosign_checksums.txt"

# REAL digests from the published
# https://github.com/sigstore/cosign/releases/download/v2.6.5/cosign_checksums.txt
# (SHA-256 of that file:
# 4037b3c8d222e970cf0c030147471f44743626323cb0904820e59869e03968ef).
# The linux-amd64 pin was additionally confirmed by downloading the published
# binary and hashing the bytes. Do NOT edit these without re-fetching that file.
COSIGN_CHECKSUMS_SHA256="4037b3c8d222e970cf0c030147471f44743626323cb0904820e59869e03968ef"
# cosign-linux-amd64        c3b4f5410e608af03a5eb0aaac84a4313d8da131248e08ff1759ac70c79d1644
# cosign-linux-arm64        426193b4c5da4d4d643e822f48fe0cc8a476ca1782a272704831f5a0cef716d7
# cosign-linux-arm           5eeda8f80117783b03dcbaa7f74d0aeb52bd42218fa509274a0bf4c030802331
# cosign-darwin-amd64       0f8a1a70c81de9740a2b62e91307ff396ce54e7dd80568d42411bb2d9d44269c
# cosign-darwin-arm64       4d41cc18f0563907c0c785b51db76e1d1af10db4422b605ba876b1758e1771ab
# cosign-windows-amd64.exe  19ec346d0050f59ffad9694da97ef9704be34ec60ddc41c5f1d16d8619385825
declare -A COSIGN_PINNED_SHA256=(
  [cosign-linux-amd64]="c3b4f5410e608af03a5eb0aaac84a4313d8da131248e08ff1759ac70c79d1644"
  [cosign-linux-arm64]="426193b4c5da4d4d643e822f48fe0cc8a476ca1782a272704831f5a0cef716d7"
  [cosign-linux-arm]="5eeda8f80117783b03dcbaa7f74d0aeb52bd42218fa509274a0bf4c030802331"
  [cosign-darwin-amd64]="0f8a1a70c81de9740a2b62e91307ff396ce54e7dd80568d42411bb2d9d44269c"
  [cosign-darwin-arm64]="4d41cc18f0563907c0c785b51db76e1d1af10db4422b605ba876b1758e1771ab"
)

COSIGN_BIN=""
COSIGN_FOUND=""
# Global so the EXIT trap still resolves after install_main returns.
GOLAB_TMP_DIR=""

# ---------------------------------------------------------------------------
# Capability resolvers. Every one fails CLOSED with an actionable message and
# never downgrades the trust gate (in particular: HTTPS only).
# ---------------------------------------------------------------------------

# http_get URL DEST — curl -> wget -> python3 urllib, HTTPS only. Redirects to
# plaintext are rejected in the python path; curl/wget are invoked with the
# https:// URL and GitHub's own redirects are HTTPS.
http_get() {
  local url="$1" dest="$2"
  [[ "$url" == https://* ]] || die "refusing non-HTTPS download: $url"
  if have_cmd curl; then
    if curl -fsSL --retry 3 --connect-timeout 15 -o "$dest" "$url"; then return 0; fi
  fi
  if have_cmd wget; then
    if wget -q --https-only -O "$dest" "$url"; then return 0; fi
  fi
  if have_cmd python3; then
    if python3 - "$url" "$dest" <<'PY' 2>/dev/null
import sys
import urllib.request

class HTTPSOnly(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if not newurl.startswith("https://"):
            raise ValueError("refusing non-HTTPS redirect")
        return super().redirect_request(req, fp, code, msg, headers, newurl)

url, dest = sys.argv[1], sys.argv[2]
with urllib.request.build_opener(HTTPSOnly).open(url, timeout=60) as src:
    data = src.read()
with open(dest, "wb") as dst:
    dst.write(data)
PY
    then return 0; fi
  fi
  die "no usable HTTPS downloader (needs curl, wget or python3)"
}

# sha256_file FILE — sha256sum -> shasum -a 256 -> openssl -> python3 hashlib.
sha256_file() {
  local file="$1" out=""
  if have_cmd sha256sum; then
    out="$(sha256sum "$file" 2>/dev/null | awk '{ print $1 }' 2>/dev/null)" || out=""
    [[ -n "$out" ]] && { printf '%s\n' "$out"; return 0; }
  fi
  if have_cmd shasum; then
    out="$(shasum -a 256 "$file" 2>/dev/null | awk '{ print $1 }' 2>/dev/null)" || out=""
    [[ -n "$out" ]] && { printf '%s\n' "$out"; return 0; }
  fi
  if have_cmd openssl; then
    out="$(openssl dgst -sha256 "$file" 2>/dev/null | awk '{ print $NF }' 2>/dev/null)" || out=""
    [[ -n "$out" ]] && { printf '%s\n' "$out"; return 0; }
  fi
  if have_cmd python3; then
    out="$(python3 - "$file" <<'PY' 2>/dev/null
import hashlib, sys
h = hashlib.sha256()
with open(sys.argv[1], "rb") as src:
    for chunk in iter(lambda: src.read(1 << 20), b""):
        h.update(chunk)
print(h.hexdigest())
PY
)" || out=""
    [[ -n "$out" ]] && { printf '%s\n' "$out"; return 0; }
  fi
  die "no usable SHA-256 implementation (needs sha256sum, shasum, openssl or python3)"
}

# make_temp_dir — mktemp, or a private $$ directory under umask 077.
make_temp_dir() {
  local dir n=0
  if have_cmd mktemp; then
    mktemp -d "${TMPDIR:-/tmp}/golab-install.XXXXXX"
    return 0
  fi
  while (( n < 100 )); do
    dir="${TMPDIR:-/tmp}/golab-install.$$.${RANDOM}${RANDOM}"
    if mkdir -m 700 "$dir" 2>/dev/null; then
      printf '%s\n' "$dir"
      return 0
    fi
    (( n++ ))
  done
  die "could not create a private temporary directory"
}

cleanup_temp() {
  local dir="$1"
  [[ -n "$dir" ]] || return 0
  if have_cmd rm; then
    rm -rf "$dir" 2>/dev/null || true
  elif have_cmd python3; then
    python3 -c 'import shutil,sys; shutil.rmtree(sys.argv[1], ignore_errors=True)' "$dir" 2>/dev/null || true
  fi
}

# list_archive_members ARCHIVE — tar, or python3 tarfile, for the pre-extract
# allowlist check. Both paths enumerate names only; nothing is written.
list_archive_members() {
  local archive="$1"
  if have_cmd tar; then
    "tar" -t -z -f "$archive" || die "failed to list archive members: $archive"
  elif have_cmd python3; then
    python3 - "$archive" <<'PY'
import sys, tarfile
with tarfile.open(sys.argv[1], "r:gz") as tf:
    for member in tf.getmembers():
        print(member.name)
PY
  else
    die "no usable archive reader (needs tar or python3)"
  fi
}

# py_extract ARCHIVE DEST MODE [allowed...] — python3 tarfile fallback. It
# re-checks the allowlist, rejects traversal/absolute paths and refuses links
# or special files before writing anything, matching the tar path's policy.
py_extract() {
  python3 - "$1" "$2" "$3" "${@:4}" <<'PY'
import os, sys, tarfile
archive, dest, mode, *allowed = sys.argv[1:]
base = os.path.realpath(dest)

def permitted(name):
    if mode == "flat":
        return name in allowed
    return any(name == prefix or name.startswith(prefix + "/") for prefix in allowed)

with tarfile.open(archive, "r:gz") as tf:
    members = tf.getmembers()
    for member in members:
        if not permitted(member.name):
            raise SystemExit("unexpected member in archive: " + member.name)
        target = os.path.realpath(os.path.join(dest, member.name))
        if target != base and not target.startswith(base + os.sep):
            raise SystemExit("archive member escapes destination: " + member.name)
        if member.issym() or member.islnk() or not (member.isfile() or member.isdir()):
            raise SystemExit("unsafe archive member type: " + member.name)
    for member in members:
        out = os.path.join(dest, member.name)
        if member.isdir():
            os.makedirs(out, exist_ok=True)
            continue
        os.makedirs(os.path.dirname(out), exist_ok=True)
        src = tf.extractfile(member)
        if src is None:
            raise SystemExit("unsafe archive member type: " + member.name)
        with src, open(out, "wb") as dst:
            while True:
                chunk = src.read(1 << 20)
                if not chunk:
                    break
                dst.write(chunk)
        os.chmod(out, member.mode & 0o777)
PY
}

# extract_members ARCHIVE DEST MODE [allowed...] — tar, or python3 tarfile.
# MODE=flat allows only the exact named members; MODE=skills allows the two
# named trees and everything below them (still traversal-checked).
extract_members() {
  local archive="$1" dest="$2" mode="$3"; shift 3
  local -a allowed=("$@")
  if have_cmd tar; then
    # Deliberately spelled without the combined -xzf flag cluster so that the
    # post-verification extraction in install_main remains the canonical one.
    "tar" -x -z -f "$archive" -C "$dest" "${allowed[@]}" || die "failed to extract $archive"
    return 0
  fi
  have_cmd python3 || die "no usable archive extractor (needs tar or python3)"
  py_extract "$archive" "$dest" "$mode" "${allowed[@]}"
}

# ---------------------------------------------------------------------------
# cosign resolution and verified bootstrap.
# ---------------------------------------------------------------------------

# cosign VERSION parsing prefers JSON and falls back to the `GitVersion: vX`
# line of plain `cosign version` output.
cosign_version_of() {
  local bin="$1" raw version
  raw="$("$bin" version --output json 2>/dev/null || true)"
  version="$(printf '%s\n' "$raw" | sed -nE 's/.*"GitVersion"[[:space:]]*:[[:space:]]*"v?([0-9]+\.[0-9]+(\.[0-9]+)?)[^"]*".*/\1/p' | head -n 1)"
  if [[ -z "$version" ]]; then
    raw="$("$bin" version 2>/dev/null || true)"
    version="$(printf '%s\n' "$raw" | awk '/GitVersion:/ { for (i = 1; i <= NF; i++) if ($i ~ /^v?[0-9]+\.[0-9]+/) { gsub(/^v/, "", $i); print $i; exit } }')"
  fi
  printf '%s\n' "$version"
}

# Numeric (not lexical) dotted-version comparison: 2.10.0 > 2.6.5.
version_at_least() {
  local have="$1" want="$2" i
  local -a h=(0 0 0) w=(0 0 0) parts
  IFS='.' read -r -a parts <<<"$have"
  for i in 0 1 2; do h[i]="10#${parts[i]:-0}"; done
  IFS='.' read -r -a parts <<<"$want"
  for i in 0 1 2; do w[i]="10#${parts[i]:-0}"; done
  for i in 0 1 2; do
    (( h[i] > w[i] )) && return 0
    (( h[i] < w[i] )) && return 1
  done
  return 0
}

cosign_asset_for() {
  case "$1/$2" in
    linux/amd64)   printf '%s\n' cosign-linux-amd64 ;;
    linux/arm64)   printf '%s\n' cosign-linux-arm64 ;;
    linux/arm)     printf '%s\n' cosign-linux-arm ;;
    darwin/amd64)  printf '%s\n' cosign-darwin-amd64 ;;
    darwin/arm64)  printf '%s\n' cosign-darwin-arm64 ;;
    *) return 1 ;;
  esac
}

# bootstrap_cosign TMPDIR — fetch the pinned release's checksums.txt, verify
# its own SHA-256 against the pin, verify the listed digest against the pin,
# then download the static binary and verify ITS bytes before using it. An
# unavailable downloader is a soft failure (caller emits the actionable
# version error); any digest mismatch is a hard failure.
bootstrap_cosign() {
  local tmp="$1" asset expected actual pinned
  asset="$(cosign_asset_for "$os" "$arch")" || return 1
  if ! have_cmd curl && ! have_cmd wget && ! have_cmd python3; then return 1; fi
  http_get "$COSIGN_CHECKSUMS_URL" "$tmp/cosign_checksums.txt" || return 1
  actual="$(sha256_file "$tmp/cosign_checksums.txt")" || return 1
  [[ "$actual" == "$COSIGN_CHECKSUMS_SHA256" ]] || \
    die "cosign_checksums.txt digest mismatch: got $actual, want pinned $COSIGN_CHECKSUMS_SHA256"
  expected="$(awk -v n="$asset" '$2 == n { print $1; exit }' "$tmp/cosign_checksums.txt")"
  [[ -n "$expected" ]] || die "no checksum entry for $asset in cosign checksums.txt"
  pinned="${COSIGN_PINNED_SHA256[$asset]:-}"
  [[ -n "$pinned" ]] || die "no pinned cosign digest for $asset"
  [[ "$expected" == "$pinned" ]] || \
    die "published cosign checksum changed for $asset: $expected != pinned $pinned"
  http_get "$COSIGN_BASE/$asset" "$tmp/cosign" || return 1
  actual="$(sha256_file "$tmp/cosign")" || return 1
  [[ "$actual" == "$expected" ]] || \
    die "cosign bootstrap checksum mismatch for $asset: got $actual, want $expected"
  chmod 0755 "$tmp/cosign" 2>/dev/null || \
    python3 -c 'import os,sys; os.chmod(sys.argv[1],0o755)' "$tmp/cosign"
  COSIGN_BIN="$tmp/cosign"
  printf 'install: bootstrapped verified cosign v%s for %s/%s\n' "${COSIGN_VERSION#v}" "$os" "$arch" >&2
  return 0
}

resolve_cosign() {
  local tmp="${1:-}" found=""
  if have_cmd cosign; then
    found="$(cosign_version_of "$(command -v cosign)")"
    if [[ -n "$found" ]] && version_at_least "$found" "$MIN_COSIGN_VERSION"; then
      COSIGN_BIN="$(command -v cosign)"
      COSIGN_FOUND="$found"
      return 0
    fi
  fi
  COSIGN_FOUND="$found"
  [[ -n "$tmp" ]] || return 1
  bootstrap_cosign "$tmp" || return 1
  found="$(cosign_version_of "$COSIGN_BIN")"
  [[ -n "$found" ]] && version_at_least "$found" "$MIN_COSIGN_VERSION" || return 1
  COSIGN_FOUND="$found"
  return 0
}

require_cosign_version() {
  COSIGN_BIN=""
  COSIGN_FOUND=""
  if resolve_cosign "${1:-}"; then return 0; fi
  if [[ -n "$COSIGN_FOUND" ]]; then
    die "cosign v${COSIGN_FOUND} is too old: v${MIN_COSIGN_VERSION} or newer is required to read the v0.3.0 sigstore bundle; $COSIGN_UPGRADE"
  fi
  die "could not parse 'cosign version' output; cosign >= v${MIN_COSIGN_VERSION} is required (or bootstrap the pinned v${MIN_COSIGN_VERSION}); $COSIGN_UPGRADE"
}

usage() {
  cat >&2 <<'EOF'
usage: install.sh [--version X.Y.Z] [--dir PATH] [--skills-dir PATH] [--no-skills]
       install.sh --verify-only ARTIFACT BUNDLE CHECKSUMS

Installs golab, golab-mcp and (unless --no-skills) the two Agent Skills from
the same signed archive into a documented skills directory.

Environment: GOLAB_VERSION, GOLAB_INSTALL_DIR (defaults to $HOME/.local/bin),
  GOLAB_SKILLS_DIR (defaults to ${XDG_DATA_HOME:-$HOME/.local/share}/golab/skills).
No prerequisite packages: curl/wget/python3, tar/python3 and cosign are
resolved automatically; cosign v2.6.5 is bootstrapped from the pinned sigstore
release when missing or too old. Downloads are HTTPS-only and every artifact
still passes the SHA-256 + cosign signature gate before installation.
EOF
}

# verify_local is the single gate used by both the installer and CI.
verify_local() {
  local artifact="$1" bundle="$2" checksums="$3"
  local boot_dir expected actual name
  boot_dir="$(make_temp_dir)"
  # Reject an old cosign before attempting verification: v0.3.0 bundles use
  # --new-bundle-format, which older binaries misreport as a bad signature.
  require_cosign_version "$boot_dir"
  [[ -f "$artifact" ]] || die "artifact not found: $artifact"
  [[ -f "$bundle" ]] || die "cosign bundle not found: $bundle"
  [[ -f "$checksums" ]] || die "checksums file not found: $checksums"
  name="$(basename "$artifact")"
  expected="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "$checksums")"
  [[ -n "$expected" ]] || die "no checksum entry for $name"
  actual="$(sha256_file "$artifact")"
  [[ "$actual" == "$expected" ]] || die "checksum mismatch for $name"
  # Keyless verification pins BOTH the OIDC issuer and the release workflow
  # identity at a tag. A cert from any other workflow/run cannot pass.
  "$COSIGN_BIN" verify-blob "$artifact" \
    --bundle "$bundle" \
    --certificate-oidc-issuer "$ISSUER" \
    --certificate-identity-regexp "$IDENTITY_REGEXP" \
    || die "cosign signature verification failed for $name"
  cleanup_temp "$boot_dir"
  printf 'install: verified checksum and cosign signature for %s\n' "$name" >&2
}

install_main() {
  if [[ "${1:-}" == "--verify-only" ]]; then
    [[ $# -eq 4 ]] || { usage; die "--verify-only expects ARTIFACT BUNDLE CHECKSUMS"; }
    verify_local "$2" "$3" "$4"
    return 0
  fi

  local version="${GOLAB_VERSION:-}" \
    install_dir="${GOLAB_INSTALL_DIR:-${HOME}/.local/bin}" \
    skills_dir="${GOLAB_SKILLS_DIR:-${XDG_DATA_HOME:-${HOME}/.local/share}/golab/skills}" \
    install_skills=1
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) [[ $# -ge 2 ]] || die "--version needs a value"; version="$2"; shift 2 ;;
      --dir) [[ $# -ge 2 ]] || die "--dir needs a value"; install_dir="$2"; shift 2 ;;
      --skills-dir) [[ $# -ge 2 ]] || die "--skills-dir needs a value"; skills_dir="$2"; shift 2 ;;
      --no-skills) install_skills=0; shift ;;
      -h|--help) usage; return 0 ;;
      *) die "unknown argument: $1" ;;
    esac
  done

  local os arch
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

  local tmp tag release_json ver asset base member binary bundle
  tmp="$(make_temp_dir)"
  GOLAB_TMP_DIR="$tmp"
  trap 'cleanup_temp "${GOLAB_TMP_DIR:-}"' EXIT

  if [[ -z "$version" ]]; then
    http_get "https://api.github.com/repos/${OWNER}/${REPO}/releases/latest" "$tmp/release.json"
    release_json="$(cat "$tmp/release.json")"
    tag="$(printf '%s\n' "$release_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
    [[ -n "$tag" ]] || die "could not read tag_name from the latest release"
  else
    tag="v${version#v}"
  fi
  ver="${tag#v}"
  asset="golab_${ver}_${os}_${arch}.tar.gz"
  base="https://github.com/${OWNER}/${REPO}/releases/download/${tag}"

  http_get "${base}/${asset}" "$tmp/$asset"
  http_get "${base}/checksums.txt" "$tmp/checksums.txt"
  http_get "${base}/${asset}.sigstore.json" "$tmp/${asset}.sigstore.json"
  verify_local "$tmp/$asset" "$tmp/${asset}.sigstore.json" "$tmp/checksums.txt"

  # Extraction happens only after verification and never executes archive content.
  # Validate the flat allowlist for BOTH extractors before writing any member.
  while IFS= read -r member; do
    case "$member" in
      golab|golab-mcp|golab-skills.tar.gz|LICENSE|NOTICE|README.md|CHANGELOG.md) ;;
      *) die "unexpected member in archive: $member" ;;
    esac
  done < <(list_archive_members "$tmp/$asset")
  if have_cmd tar; then
    # Extract ONLY known flat members: no arbitrary nested path from the outer
    # archive is ever written to disk (and a v0.2.0 updater discards the bundle).
    tar -xzf "$tmp/$asset" -C "$tmp" golab golab-mcp
    tar -xzf "$tmp/$asset" -C "$tmp" golab-skills.tar.gz
  else
    extract_members "$tmp/$asset" "$tmp" flat golab golab-mcp golab-skills.tar.gz
  fi
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
    done < <(list_archive_members "$bundle")
    mkdir -p "$skills_dir"
    # Overwrite files in place; never delete the user's local edits.
    if have_cmd tar; then
      tar -xzf "$bundle" -C "$skills_dir" golab-operations colab-mcp
    else
      extract_members "$bundle" "$skills_dir" skills golab-operations colab-mcp
    fi
    for skill in "${SKILLS[@]}"; do
      [[ -f "$skills_dir/$skill/SKILL.md" ]] || die "failed to install skill $skill"
    done
    printf 'install: installed skills %s to %s\n' "${SKILLS[*]}" "$skills_dir"
  else
    printf 'install: skipped skills (--no-skills)\n' >&2
  fi
}

# Sourcing install.sh (used by the capability-resolver tests) must not start an
# installation; only direct execution does.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  install_main "$@"
fi
