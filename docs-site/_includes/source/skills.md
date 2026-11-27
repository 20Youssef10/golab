<!-- Generated from docs/USAGE.md §13. Agent Skills discovery by docs-site/tools/sync-sources.py. Do not edit. -->

## 13. Agent Skills discovery

Two portable Agent Skills live in the repository for agents that load skills
from a filesystem. They are also shipped in release archives, installed by the
release installers, and bundled in the GHCR image under
`/usr/share/golab/skills`:

| Path | Purpose |
|---|---|
| `.agents/skills/golab-operations` | Safe operating procedure for the CLI: session lifecycle, `exec`/`run` parity, file transfer, VM inspection, detached jobs, spend guards, redaction, stdout/stderr contract, TTY and `--yes` rules, and remote-vs-local path semantics. |
| `.agents/skills/colab-mcp` | Setting up and verifying `golab-mcp` from MCP clients: stdio and streamable HTTP, Claude Desktop `mcpServers` config, tool categories, `confirm: true` gating, redaction, and MCP Inspector checks with modern and legacy protocol eras. |

Each is a directory with a `SKILL.md` frontmatter (`name`, `description`,
`metadata`) and a `references/` subdirectory. Both validate with the official
`agentskills` CLI:

```bash
agentskills validate .agents/skills/golab-operations
agentskills validate .agents/skills/colab-mcp
```

These skills carry no live proof themselves: they document the CLI surfaces
above, whose verification status is recorded in
[CONFORMANCE.md](https://github.com/20Youssef10/golab/blob/main/docs/CONFORMANCE.md). Command and flag facts in them were checked
against `golab --help` and `<command> --help`; re-check the installed binary with
`golab version`, because flags can drift between versions.
