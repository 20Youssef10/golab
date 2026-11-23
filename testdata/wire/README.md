# Captured wire fixtures (L3) — HUMAN REQUIRED

This directory is intentionally empty. It will hold **redacted**, ground-truth
HTTP/WebSocket traces from the **official Python CLI**, not from our Go code.
Fakes are not evidence of live behaviour (AGENTS.md §3).

## What a human must capture

Use `scripts/live-capture.sh` (TLS key log, GET-only by default) with a
throw-away Google account. Capture these endpoints first — none allocate a VM:

| Endpoint | Method | Payload | Proves |
|---|---|---|---|
| `/tun/m/assignments` | GET | – | C6 `nbh`, C12 mapping, XSSI prefix |
| `/tun/m/ccu-info` | GET | – | C15 CCU shape |
| `/tun/m/assign?...` | GET | fresh `nbh` | C7/C8 variant/accelerator/shape, 400/412/429 bodies |
| kernel REST + WS | GET/upgrade | on an existing session | K-1/K-2 headers, close/ping (🔴 item 7) |
| `/api/contents/<path>?content=1` | GET | scratch file | F-1 header set, F-4 chunk support (🔴 S6/HIGH) |
| OAuth authorization URL | – | copy from stderr | O-5 PKCE S256 + `access_type=offline` |
| `/colab/ssh` upgrade | WS | valid pubkey | H-1 agent value, close codes (🔴 item 7) |

## Fixture format (one JSON file per endpoint)

```json
{
  "endpoint": "GET /tun/m/assignments",
  "captured_at": "<TIMESTAMP>",
  "request":  { "path": "...", "query": { "authuser": "0" }, "headers": { "Accept": "application/json" } },
  "response": { "status": 200, "headers": {}, "body": {} }
}
```

## Redaction checklist (before any file enters git)

- [ ] `Authorization: Bearer …`, `X-Goog-Colab-Token`, runtime-proxy tokens
- [ ] `Cookie` / `Set-Cookie`, `id_token`, `refresh_token`, OAuth `code`
- [ ] account email, GAIA id, project number, endpoint UUIDs, `nbh`
- [ ] local paths → `<TMPDIR>`; timestamps → `<TIMESTAMP>`
- [ ] `bash scripts/golden.sh --scenario wire` passes (no secret-looking strings)

Raw PCAPs and SSL key logs stay **outside** the repo and are deleted after
fixtures are derived. Re-capture monthly and on every upstream release that
touches `client.py`/`runtime.py` (docs/07 §5.7). Update docs/02 🔴→🟢 only after
a human verifies the fixture against the model.
