# Documentation site (GitHub Pages)

This directory is the source of the published site at
<https://20youssef10.github.io/golab/>. It is a Jekyll site built by
`.github/workflows/docs.yml` with the GitHub Pages-supported actions.

## Layout

| Path | Purpose |
|---|---|
| `_config.yml` | Site identity, `baseurl: /golab`, Markdown/highlighting options. |
| `_data/nav.yml` | The hierarchical sidebar — groups, titles and page URLs. |
| `_data/commands.yml` | 55 top-level commands, generated from `golab help`. |
| `_data/mcp-tools.yml` | 25 allow-listed MCP tools, generated from `internal/mcpserver/tools.go`. |
| `_layouts/default.html` | One shell for every page: header, sidebar, breadcrumbs, article, footer. |
| `_includes/` | Header, sidebar, search, breadcrumbs, footer, inline SVG diagrams. |
| `_includes/source/` | **Generated excerpts of canonical docs** — do not edit by hand. |
| `assets/css/site.css`, `assets/js/site.js` | Local styling and progressive enhancement only; no CDN, fonts or analytics. |
| `pages/` | One topic per page; prose is included from `_includes/source/`. |
| `tools/sync-sources.py` | Regenerates excerpts from `docs/*.md` and rewrites relative links. |
| `tools/check-nav.py` | Verifies every nav URL resolves to a page permalink. |

## Source of truth

The prose lives in the repository's `docs/` directory (and `README.md`).
Jekyll's GitHub Pages build runs in safe mode, so an include cannot reach outside
the Jekyll source directory. `tools/sync-sources.py` extracts the heading-delimited
sections this site uses into `_includes/source/`, and rewrites relative Markdown links
to permanent GitHub blob URLs. Each generated fragment starts with a comment naming
its source; each page renders a visible "Canonical source" note.

Edit `docs/X.md`, then run:

```bash
python3 docs-site/tools/sync-sources.py       # refresh excerpts
python3 docs-site/tools/sync-sources.py --check
python3 docs-site/tools/check-nav.py
```

CI reruns `--check` before building: if a committed excerpt drifts from its source,
the docs build fails rather than publishing stale prose.

## Local preview

Jekyll is not installable on every host (notably Termux, where a native gem
dependency fails to compile). On a host with Ruby/Bundler and the `github-pages`
gem available:

```bash
cd docs-site
bundle exec jekyll serve --baseurl /golab
```

Then open <http://localhost:4000/golab/>. Without Jekyll, the files are still
reviewable as text; the site cannot be fully rendered locally.

## UX notes

- Navigation groups: Overview, Operations, Architecture, Protocol, Security, Testing, Release.
- The sidebar filter is client-side; `/` focuses it, `Esc` clears it.
- Theme defaults to `prefers-color-scheme`; the toggle cycles system → light → dark
  and persists the choice in `localStorage` (wrapped in try/catch for private mode).
- Diagrams are inline SVG, not a CDN script: no third-party request can carry data.
- Code-copy buttons are added by JavaScript; with JS disabled, code remains readable.
- All controls are native buttons/links with visible `:focus-visible` rings.
