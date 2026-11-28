# Using the golab MCP server

This server exposes the `golab` CLI as MCP tools over stdio (`--transport stdio`)
or streamable HTTP (`--transport http --addr :8080`, endpoint `/mcp`). It is a
separate static binary, `golab-mcp`, and invokes the `golab` binary found on
PATH (override with `--golab-bin`). It exposes **25 tools**.

## Safety contract

- Read-only tools (`colab_sessions`, `colab_status`, `colab_usage`, `colab_cost`,
  `colab_ps`, `colab_cat`, `colab_tail`, `colab_grep`, `colab_du`, `colab_find`,
  `colab_logs`, `colab_wait`, `colab_jobs`, `colab_doctor`, `colab_compat`, and
  `colab_update` with `action:"check"`) are annotated `readOnlyHint` for the
  subprocess tools and never require confirmation.
- Billable or destructive tools (`colab_new`, `colab_exec`, `colab_run`,
  `colab_stop`, `colab_fetch`, `colab_resume`) require `confirm: true`. Without
  it the tool returns `isError` and no subprocess is started.
- `colab_reap` is a dry-run by default. Setting `dry_run: false` makes it
  destructive and requires `confirm: true`.
- `colab_update` with `action:"install"` verifies and installs a signed release
  (`golab update --install --yes`) and requires `confirm: true`. `action:"check"`
  is read-only. The tool never handles tokens.
- `colab_install_mcp` writes MCP config for `opencode`, `claude-code`, `cursor`,
  `gemini`, `codex` and `vscode`. `dry_run` defaults to true; `mode:"manual"`
  returns a snippet and writes nothing. A write (`dry_run:false` in auto mode)
  requires `confirm: true`, is atomic (temp + fsync + rename, `.bak`, mode
  preserved), is idempotent and never clobbers unrelated keys. It never stores
  credentials — every `env` is empty.
- `colab_install_skills` copies only the two portable skill trees
  (`colab-mcp`, `golab-operations`) into `unified`, a specific agent dir,
  `project`, or `all`. `dry_run` defaults to true; a write requires
  `confirm: true`. Unknown or path-shaped targets are rejected.
- Code passed to `colab_exec` travels on stdin, never in argv or a shell.
  Credential names passed via `secret` are resolved by the CLI; values are
  never echoed in tool results.
- Cancelling an MCP request cancels the underlying `golab` subprocess.
- All stdout in stdio mode is reserved for JSON-RPC. Diagnostics go to stderr.

## Typical workflow

1. `colab_sessions` — check for an existing runtime.
2. `colab_status` — inspect one runtime.
3. `colab_new` with `confirm: true` — allocate only when needed.
4. `colab_exec` with `code` and `confirm: true` — run Python on the VM.
5. `colab_stop` with `confirm: true` — release the runtime when finished.

`colab_run` performs allocation, execution and teardown in one call; it is
billable and always requires `confirm: true`.

## Setting up a client

1. Call `colab_install_mcp` with `agents: ["all"]`, an absolute `golab_bin`, and
   the default `dry_run: true` to preview every target.
2. Present the per-target outcomes to the user; then call again with
   `dry_run: false` and `confirm: true` to write.
3. Call `colab_install_skills` with `targets: ["all"]` (or a specific agent),
   preview first, then confirm to copy the two portable skills.
4. Restart the client; `tools/list` should show 25 tools.

Use `mode: "manual"` instead of a write to obtain a pasteable snippet. Use
`colab_update` with `action: "check"` to preview a release, and with
`action: "install", confirm: true` to apply it.

## Result shapes

The 22 subprocess tools return `command`, `exit_code`, `output` (redacted
stdout) and `diagnostics` (redacted stderr). The three local tools return
`kind`, `mode`, `dry_run` and a `targets` array with per-target `target`,
`path`, `outcome` (`snippet` | `would-write` | `written` | `error`) and optional
`snippet`, `backup` or `error`. Both shapes are exposed as structured content
and text content; raw argv is never returned.
