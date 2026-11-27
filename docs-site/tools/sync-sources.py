#!/usr/bin/env python3
"""Regenerate the Jekyll include fragments under docs-site/_includes/source/.

The canonical prose lives in docs/*.md (and README.md). Jekyll's GitHub Pages
build runs in safe mode, so an include cannot reach outside the Jekyll source
directory. This script extracts the heading-delimited sections the site needs,
rewrites relative Markdown links to permanent GitHub blob URLs, and writes the
fragments into _includes/source/ where Jekyll can include them.

Usage:
    python3 docs-site/tools/sync-sources.py           # write fragments
    python3 docs-site/tools/sync-sources.py --check   # fail if snapshots drift

No third-party dependencies. This is a copy step, not a second source of truth.
"""

from __future__ import annotations

import argparse
import posixpath
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SITE = REPO / "docs-site"
OUT_DIR = SITE / "_includes" / "source"
BLOB = "https://github.com/20Youssef10/golab/blob/main/"

# output filename -> (canonical file relative to repo root, list of heading
# prefixes, or None to include the whole document without its H1 title)
SLICES: dict[str, tuple[str, list[str] | None]] = {
    "overview.md": ("docs/INDEX.md", ["Project in one paragraph"]),
    "quickstart.md": (
        "docs/USAGE.md",
        ["Conventions used throughout", "1. Session lifecycle"],
    ),
    "commands.md": (
        "docs/05-command-parity.md",
        ["Global flags (all commands)", "Deliberate deviations (Δ) summary"],
    ),
    "mcp.md": ("docs/USAGE.md", ["12. MCP server"]),
    "skills.md": ("docs/USAGE.md", ["13. Agent Skills discovery"]),
    "spend.md": (
        "docs/USAGE.md",
        ["Spend guard for", "golab reap", "Upstream-compatible utilities"],
    ),
    "vm-inspection.md": ("docs/USAGE.md", ["VM inspection"]),
    "detached.md": (
        "docs/adr-001-detached-runs.md",
        ["Context", "Decision", "Consequences", "Verification"],
    ),
    "reproducibility.md": ("docs/USAGE.md", ["Reproducibility"]),
    "architecture.md": (
        "docs/03-go-architecture.md",
        [
            "1. Goals and non-goals",
            "2. Repository layout",
            "3. Key interfaces",
            "4. Concurrency model",
            "5. Error model",
            "9. Architecture Decision Records",
        ],
    ),
    "protocol.md": (
        "docs/02-protocol-spec.md",
        [
            "0. Constants",
            "1. Authentication",
            "2. Control plane",
            "3. Data plane",
            "4. SSH over WebSocket",
        ],
    ),
    "security.md": (
        "docs/USAGE.md",
        ["10. Security and privacy"],
    ),
    "security-model.md": (
        "docs/08-risks-decisions-security.md",
        ["3. Security model"],
    ),
    "conformance.md": (
        "docs/CONFORMANCE.md",
        [
            "1. Test-layer status",
            "3. Coverage gate",
            '6. Human-only / external steps blocking "done"',
        ],
    ),
    "distribution.md": (
        "docs/04-platform-matrix.md",
        ["7. Distribution channels"],
    ),
    "install.md": ("README.md", ["Install"]),
    "release-artifacts.md": ("docs/RELEASING.md", ["3. Required signed artifacts"]),
    "faq.md": ("docs/FAQ.md", None),
    "migration.md": ("docs/MIGRATION.md", None),
}

HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*$")
LINK = re.compile(r"\[([^\]\n]+)\]\(([^)\s]+)\)")


def norm(text: str) -> str:
    """Normalise a heading for prefix comparison."""
    text = text.replace("`", "").replace("**", "")
    return re.sub(r"\s+", " ", text).strip().lower()


def strip_front_matter(text: str) -> str:
    if text.startswith("---\n"):
        end = text.find("\n---\n", 4)
        if end != -1:
            text = text[end + 5 :]
    return text


def sections(text: str, wanted: list[str]) -> str:
    lines = strip_front_matter(text).splitlines()
    found: set[str] = set()
    out: list[str] = []
    for i, line in enumerate(lines):
        match = HEADING.match(line)
        if not match:
            continue
        level, title = len(match.group(1)), match.group(2)
        title_norm = norm(title)
        hit = next(
            (w for w in wanted if title_norm.startswith(norm(w)) and w not in found),
            None,
        )
        if hit is None:
            continue
        found.add(hit)
        block = [line]
        for following in lines[i + 1 :]:
            next_heading = HEADING.match(following)
            if next_heading and len(next_heading.group(1)) <= level:
                break
            block.append(following)
        out.extend(block)
    missing = [w for w in wanted if w not in found]
    if missing:
        raise SystemExit(f"heading prefix not found: {missing}")
    return "\n".join(out).strip() + "\n"


def whole_document(text: str) -> str:
    body = strip_front_matter(text).splitlines()
    # Drop the document H1: the Jekyll layout already renders the page title.
    if body and body[0].startswith("# "):
        body = body[1:]
    while body and not body[0].strip():
        body.pop(0)
    return "\n".join(body).strip() + "\n"


def rewrite_links(md: str, src: str) -> str:
    """Point relative Markdown links at the canonical file on GitHub."""
    result: list[str] = []
    in_fence = False
    base_dir = posixpath.dirname(src)
    for line in md.splitlines():
        if line.lstrip().startswith(("```", "~~~")):
            in_fence = not in_fence
            result.append(line)
            continue
        if in_fence:
            result.append(line)
            continue

        def repl(match: re.Match[str]) -> str:
            label, target = match.group(1), match.group(2)
            if target.startswith(("http://", "https://", "#", "mailto:", "/")):
                return match.group(0)
            path, _, fragment = target.partition("#")
            resolved = posixpath.normpath(posixpath.join(base_dir, path))
            if resolved.startswith("../") or resolved.startswith("/"):
                return match.group(0)
            suffix = f"#{fragment}" if fragment else ""
            return f"[{label}]({BLOB}{resolved}{suffix})"

        result.append(LINK.sub(repl, line))
    return "\n".join(result) + "\n"


def build() -> dict[str, str]:
    fragments: dict[str, str] = {}
    for name, (src, wanted) in SLICES.items():
        text = (REPO / src).read_text(encoding="utf-8")
        body = sections(text, wanted) if wanted else whole_document(text)
        body = rewrite_links(body, src)
        header = (
            f"<!-- Generated from {src} "
            + ("§" + ", ".join(wanted) if wanted else "whole document")
            + " by docs-site/tools/sync-sources.py. Do not edit. -->\n\n"
        )
        fragments[name] = header + body
    return fragments


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check", action="store_true", help="verify committed fragments are current"
    )
    args = parser.parse_args()
    fragments = build()
    if args.check:
        stale: list[str] = []
        for name, content in fragments.items():
            path = OUT_DIR / name
            if not path.exists() or path.read_text(encoding="utf-8") != content:
                stale.append(name)
        extra = sorted(p.name for p in OUT_DIR.glob("*.md") if p.name not in fragments)
        if stale or extra:
            print("stale fragments: " + ", ".join(stale))
            print("unexpected fragments: " + ", ".join(extra))
            print("run: python3 docs-site/tools/sync-sources.py")
            return 1
        print(f"OK: {len(fragments)} include fragments match canonical docs")
        return 0
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    for name, content in fragments.items():
        (OUT_DIR / name).write_text(content, encoding="utf-8")
    print(f"wrote {len(fragments)} fragments to {OUT_DIR.relative_to(REPO)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
