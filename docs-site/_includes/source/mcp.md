<!-- Generated from docs/USAGE.md §12. MCP server by docs-site/tools/sync-sources.py. Do not edit. -->

## 12. MCP server (`golab-mcp`)

`golab-mcp` is a **separate static binary** that exposes the CLI as a Model
Context Protocol server. It is not a subcommand of `golab` and is not shipped in
the release archives yet (see the caveat below).

```bash
go install github.com/20Youssef10/golab/cmd/golab-mcp@latest

golab-mcp                                   # stdio transport (default)
golab-mcp --transport stdio
golab-mcp --transport http --addr :8080
golab-mcp --golab-bin /path/to/golab         # binary invoked by tool calls
```

| Flag | Default | Meaning |
|---|---|---|
| `--transport` | `stdio` | `stdio` for desktop agents, or `http` for streamable HTTP. Any other value exits 2. |
| `--addr` | `:8080` | HTTP listen address; used only with `--transport http`. |
| `--golab-bin` | `golab` | Path to the `golab` binary that tool calls invoke. A missing binary is a startup warning, not a fatal error: `tools/list` must still work. |

Go's `flag` package accepts both `-flag` and `--flag`; the forms above are
equivalent. In stdio mode nothing but SDK JSON-RPC is written to stdout — all
diagnostics go to stderr (`COLAB_DEBUG=1` enables debug logging).

**Tools and safety.** The server exposes 22 allow-listed `colab_*` tools: 15
read-only (`colab_sessions`, `colab_status`, `colab_usage`, `colab_cost`,
`colab_ps`, `colab_cat`, `colab_tail`, `colab_grep`, `colab_du`, `colab_find`,
`colab_logs`, `colab_wait`, `colab_jobs`, `colab_doctor`, `colab_compat`) and 7
that mutate billable/remote state (`colab_new`, `colab_run`, `colab_exec`,
`colab_stop`, `colab_reap`, `colab_fetch`, `colab_resume`). There is no shell
tool, no arbitrary-code tool and no raw-request tool. **Every mutating tool
requires an explicit `confirm: true` in its arguments**; without it the call
fails before invoking `golab`. `colab_reap` is dry-run by default — it needs both
`dry_run: false` and `confirm: true` to delete records. Code for `colab_exec` is
passed on stdin, never in argv or a shell. Tool errors and output are redacted
with the same filter as the CLI.

**Resources and transport.** Two read-only resources are available:
`golab://docs/skill` and `golab://docs/usage`. Over HTTP the streamable MCP
endpoint is `/mcp` (the root `/` is also accepted for client compatibility);
there is **no** legacy `/sse` endpoint. The implementation uses the official
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
(v1.7.0), which negotiates the protocol version; tests drive both the modern
handshake and a legacy client requesting `2025-06-18`, confirming the same tool
list.

**Packaging:** as of the v0.4.0 release, `.goreleaser.yaml` builds
`golab-mcp` alongside `golab` and ships both binaries plus the two Agent Skills
in every signed archive. `install.sh` / `install.ps1` install both binaries and
the skills (skip with `--no-skills` / `-NoSkills`); the GHCR image carries them
too. `golab-mcp` has no separate `update` command — replace it with the next
signed release archive. `go install …/cmd/golab-mcp@latest` remains available.
Tool behaviour is covered by fake-backed tests; that is not a claim of live
Colab proof. See [CONFORMANCE.md §6](https://github.com/20Youssef10/golab/blob/main/docs/CONFORMANCE.md#6-human-only--external-steps-blocking-done).
