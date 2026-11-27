<!-- Generated from docs/USAGE.md §VM inspection by docs-site/tools/sync-sources.py. Do not edit. -->

### VM inspection

These commands read the running VM through server-side execution/polling; they do not mount a local filesystem or hold a WebSocket open. Relative paths for `tail`, `grep`, `du` and `fetch` are resolved against the VM root (`/`), matching `upload` and `cat`; already-absolute paths are unchanged. `find NAME` always searches `/content`.

```bash
golab ps -s train --json                     # alias: golab top
golab cat -s train content/out.txt
golab tail -s train content/train.log -n 50 -f --interval 2s
golab grep -s train ERROR content/ -i --glob '*.log' --max-matches 50
golab du -s train content/ --max-entries 100
golab find -s train checkpoint --max-results 50
golab fetch -s train https://example.com/data.zip content/data.zip \
      --sha256 <hex> --timeout 600
```

| Command | Flags (beyond `-s`) |
|---|---|
| `ps` / `top` | `--json`, `--timeout` (idle seconds, default `20`). Processes, GPU, memory and disk snapshot. |
| `cat REMOTE` | none; binary-safe. |
| `tail REMOTE` | `-n`/`--lines` (default `10`), `-f`/`--follow`, `--interval` (default `2s`), `--timeout` (default `15`). |
| `grep PATTERN PATH` | `-i`/`--ignore-case`, `--glob`, `--max-matches` (default `200`). |
| `du PATH` | `--max-entries` (default `200`). |
| `find NAME` | `--max-results` (default `200`); searches under remote `/content`. |
| `fetch URL [DEST]` | `--sha256 HEX`, `--timeout` (seconds, default `600`); `wget` with a `curl` fallback. |

`tail -f` polls server-side with a byte offset; it is **not byte-perfect across log rotation** — a truncated or replaced file can skip early lines or re-read unrelated bytes. Treat it as a live tail for observing new output, not as a complete log archive. `fetch --sha256` verifies the downloaded bytes against the expected digest. All output is sanitised before it reaches the terminal.
