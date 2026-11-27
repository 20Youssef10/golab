# Using the golab MCP server

This server exposes the `golab` CLI as MCP tools over stdio (`--transport stdio`)
or streamable HTTP (`--transport http --addr :8080`, endpoint `/mcp`). It is a
separate static binary, `golab-mcp`, and invokes the `golab` binary found on
PATH (override with `--golab-bin`).

## Safety contract

- Read-only tools (`colab_sessions`, `colab_status`, `colab_usage`, `colab_cost`,
  `colab_ps`, `colab_cat`, `colab_tail`, `colab_grep`, `colab_du`, `colab_find`,
  `colab_logs`, `colab_wait`, `colab_jobs`, `colab_doctor`, `colab_compat`) are
  annotated `readOnlyHint` and never require confirmation.
- Billable or destructive tools (`colab_new`, `colab_exec`, `colab_run`,
  `colab_stop`, `colab_fetch`, `colab_resume`) require `confirm: true`. Without
  it the tool returns `isError` and no subprocess is started.
- `colab_reap` is a dry-run by default. Setting `dry_run: false` makes it
  destructive and requires `confirm: true`.
- Code passed to `colab_exec` travels on stdin, never in argv or a shell.
  Credential names passed via `secret` are resolved by the CLI; values are
  never echoed in tool results.
- Cancelling the MCP request cancels the underlying `golab` subprocess.
- All stdout in stdio mode is reserved for JSON-RPC. Diagnostics go to stderr.

## Typical workflow

1. `colab_sessions` — check for an existing runtime.
2. `colab_status` — inspect one runtime.
3. `colab_new` with `confirm: true` — allocate only when needed.
4. `colab_exec` with `code` and `confirm: true` — run Python on the VM.
5. `colab_stop` with `confirm: true` — release the runtime when finished.

`colab_run` performs allocation, execution and teardown in one call; it is
billable and always requires `confirm: true`.
