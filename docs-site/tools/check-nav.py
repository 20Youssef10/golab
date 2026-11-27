#!/usr/bin/env python3
"""Verify that every URL in _data/nav.yml resolves to a page permalink."""

from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml

SITE = Path(__file__).resolve().parents[1]


def permalinks() -> set[str]:
    links: set[str] = set()
    for path in (SITE / "pages").glob("*.md"):
        text = path.read_text(encoding="utf-8")
        match = re.match(r"\A---\n(.*?)\n---\n", text, re.S)
        if not match:
            raise SystemExit(f"missing front matter: {path.name}")
        meta = yaml.safe_load(match.group(1))
        permalink = meta.get("permalink")
        if not permalink:
            raise SystemExit(f"missing permalink: {path.name}")
        if permalink in links:
            raise SystemExit(f"duplicate permalink: {permalink}")
        links.add(permalink)
    return links


def main() -> int:
    nav = yaml.safe_load((SITE / "_data" / "nav.yml").read_text(encoding="utf-8"))
    config = yaml.safe_load((SITE / "_config.yml").read_text(encoding="utf-8"))
    if config.get("baseurl") != "/golab":
        raise SystemExit("baseurl must match the published project-site path /golab")
    known = permalinks()
    urls = [item["url"] for group in nav for item in group["items"]]
    if len(urls) != len(set(urls)):
        raise SystemExit("duplicate URL in nav.yml")
    broken = [url for url in urls if url not in known]
    unused = sorted(known - set(urls))
    if broken or unused:
        print("nav URLs without a page:", broken)
        print("pages without a nav entry:", unused)
        return 1
    print(f"OK: {len(urls)} nav entries resolve to {len(known)} pages")
    return 0


if __name__ == "__main__":
    sys.exit(main())
