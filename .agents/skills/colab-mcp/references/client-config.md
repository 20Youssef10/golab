# MCP client configuration and Inspector recipes

Install the server first: `go install github.com/20Youssef10/golab/cmd/golab-mcp@latest`. Verified flags: `--transport stdio|http` (default `stdio`), `--addr :8080` (HTTP listen address), `--golab-bin PATH` (default `golab` on PATH). Streamable HTTP is served at `/mcp` and `/`; there is no `/sse` endpoint.

## Claude Desktop

Config file: macOS `~/Library/Application Support/Claude/claude_desktop_config.json`; Windows `%APPDATA%\Claude\claude_desktop_config.json`; Linux `~/.config/Claude/claude_desktop_config.json`. Use absolute paths; fully quit and restart the app after editing.

stdio (default, recommended for a local agent):

```json
{
  "mcpServers": {
    "golab": {
      "command": "/absolute/path/to/golab-mcp",
      "args": ["--transport", "stdio"],
      "env": {}
    }
  }
}
```

Client-launched HTTP server (spawn the server, then point the connector at `/mcp`):

```json
{
  "mcpServers": {
    "golab-http": {
      "command": "/absolute/path/to/golab-mcp",
      "args": ["--transport", "http", "--addr", "127.0.0.1:8080"]
    }
  }
}
```

Connect to an already-running HTTP server:

```json
{
  "mcpServers": {
    "golab": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

Some clients spell the HTTP transport `streamable-http` instead of `http`; use the value their docs require. Keep the listener on loopback unless TLS/auth is in place. Check the connector tool list after restart: 22 `colab_*` tools should appear. Claude Desktop writes stdio server stderr to `mcp-server-golab.log` in its log directory.

## Codex / VS Code Copilot / OpenCode

Their config files differ, but the shapes are the same: stdio = command + args + env (`["--transport","stdio"]`); HTTP = URL + transport type. Prefer stdio for a single local agent; use HTTP only when several clients share the server. Never check credentials into a config file. Do not add product-only skill directories or directives to a portable skill.

## Inspector CLI recipes

Requires a supported Node release for `@modelcontextprotocol/inspector`, exposed as `mcp-inspector` via `npx @modelcontextprotocol/inspector --cli`.

```bash
# stdio: launching the binary with no transport flag uses its stdio default
npx @modelcontextprotocol/inspector --cli /absolute/path/to/golab-mcp \
  --method tools/list --protocol-era modern
npx @modelcontextprotocol/inspector --cli /absolute/path/to/golab-mcp \
  --method tools/list --protocol-era legacy

# streamable HTTP: start the server, then point the Inspector at /mcp
golab-mcp --transport http --addr :8080 &
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp \
  --method tools/list --protocol-era modern --format json
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp \
  --method initialize --protocol-era legacy

# read-only call, then a gated call rejected without confirm, then confirmed
npx @modelcontextprotocol/inspector --cli /absolute/path/to/golab-mcp \
  --method tools/call --tool-name colab_status --tool-args-json '{}' --format json
npx @modelcontextprotocol/inspector --cli /absolute/path/to/golab-mcp \
  --method tools/call --tool-name colab_new --tool-args-json '{"session":"ci"}' --format json
npx @modelcontextprotocol/inspector --cli /absolute/path/to/golab-mcp \
  --method tools/call --tool-name colab_new \
  --tool-args-json '{"session":"ci","confirm":true}' --format json
```

`--protocol-era` accepts `legacy`, `auto`, `modern`: use `modern` and `legacy` explicitly for compatibility checks (legacy tested to protocol `2025-06-18`), `auto` for normal operation. `--tool-args-json` passes an object verbatim; `--tool-arg key=value` coerces values as JSON. Inspector CLI exit codes: `0` success, `1` usage/unexpected, `2` no app (`--app-info`), `3` auth required, `4` unreachable, `5` tool error, `6` schema portability errors under `--strict`. In CI add `--stored-auth-only` so no interactive browser flow starts.

## Inspector gotchas

- `--config PATH` is a read-only session file; `--catalog PATH` is the writable server list (default `~/.mcp-inspector/mcp.json`). Neither combines with an ad-hoc target.
- Under `--cli`, when the command line starts with the target, everything before `--` is the target and everything after is Inspector options — the reverse of web/TUI mode.
- A URL whose path ends in neither `/mcp` nor `/sse` requires an explicit `--transport http`. The golab server itself has no `/sse` route; do not configure one.
- The Inspector stores managed secrets in the OS keychain, or unencrypted `~/.mcp-inspector/secrets.json` on headless Linux/Termux/containers. Do not paste inspector configs or captures into issues unreviewed.
- Generated completions: `mcp-inspector --cli --completion bash|zsh|fish`; regenerate after upgrades.
