# MCP client configuration and Inspector recipes

All snippets use placeholders where the installed `golab-mcp` build must supply exact flags (`golab-mcp --help`). Never invent a flag to complete a snippet.

## Claude Desktop

Config file: macOS `~/Library/Application Support/Claude/claude_desktop_config.json`; Windows `%APPDATA%\Claude\claude_desktop_config.json`; Linux `~/.config/Claude/claude_desktop_config.json`. Use absolute paths; fully quit and restart the app after editing.

stdio:

```json
{
  "mcpServers": {
    "golab": {
      "command": "/absolute/path/to/golab-mcp",
      "args": ["<stdio flags from golab-mcp --help>"]
    }
  }
}
```

streamable HTTP (client remote-server form):

```json
{
  "mcpServers": {
    "golab": {
      "type": "http",
      "url": "http://127.0.0.1:PORT/mcp"
    }
  }
}
```

Some clients spell the HTTP transport `streamable-http` instead of `http`; use the value your client documents. Keep a local HTTP server bound to loopback unless authentication and TLS are in place. Check the connector/tool list in the client after restart; MCP log files capture stdio server stderr (`mcp-server-golab.log` in Claude Desktop's log directory).

## Codex / VS Code Copilot / OpenCode

These clients have their own config files, but the same two shapes apply: stdio = command + args + optional env; HTTP = URL + transport type. Do not assume a product-specific directory or directive belongs in the portable skill — consult each product's own MCP config surface. Prefer stdio for a single local agent; use HTTP only when several clients must share the server. Never put credentials in a checked-in config file.

## Inspector CLI recipes

Requires a supported Node release for `@modelcontextprotocol/inspector`. The package exposes `mcp-inspector` via `npx @modelcontextprotocol/inspector --cli`.

```bash
# stdio: launch command first; list tools in each era
npx @modelcontextprotocol/inspector --cli /absolute/path/to/golab-mcp \
  --method tools/list --protocol-era modern
npx @modelcontextprotocol/inspector --cli /absolute/path/to/golab-mcp \
  --method tools/list --protocol-era legacy

# streamable HTTP: /mcp suffix infers streamable-http; otherwise pass --transport http
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:PORT/mcp \
  --method tools/list --protocol-era modern --format json

# initialize probe (serverInfo, protocolVersion, capabilities)
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:PORT/mcp \
  --method initialize --protocol-era modern

# call a read-only tool, then confirm a billable one
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:PORT/mcp \
  --method tools/call --tool-name <name> \
  --tool-args-json '{"session":"ci"}' --format json
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:PORT/mcp \
  --method tools/call --tool-name <billable-tool> \
  --tool-args-json '{"session":"ci","confirm":true}' --format json
```

`--protocol-era` accepts `legacy`, `auto` and `modern`; it sets the era an ad-hoc run negotiates or overrides a config file's `protocolEra`. `modern` and `legacy` are the compatibility checks; `auto` is for normal operation. `--tool-arg key=value` coerces values as JSON, while `--tool-args-json` passes an object verbatim (use it when a string like `"012"` must stay a string).

Inspector CLI exit codes: `0` success, `1` usage/unexpected, `2` no app (with `--app-info`), `3` auth required, `4` unreachable, `5` tool error, `6` schema portability errors under `--strict`. Failures write a JSON error envelope to stderr. In CI, add `--stored-auth-only` so the Inspector never opens an interactive browser flow.

## Inspector gotchas

- `--config PATH` is a read-only session file; `--catalog PATH` is the writable server list (default `~/.mcp-inspector/mcp.json`). They are mutually exclusive and neither combines with an ad-hoc command/URL target.
- Under `--cli`, when the command line starts with the target, everything before `--` is the target and everything after is Inspector options — the reverse of web/TUI mode.
- A URL whose path ends in neither `/mcp` nor `/sse` requires an explicit `--transport http`; the Inspector no longer guesses.
- The Inspector manages secrets (OAuth tokens, stdio `env` values) in the OS keychain, or unencrypted `~/.mcp-inspector/secrets.json` on headless Linux/Termux/containers. Do not paste inspector configs or captures into issues; redact first.
- Generated completions come from the CLI's own flags: `mcp-inspector --cli --completion bash|zsh|fish`. Regenerate after upgrading.
