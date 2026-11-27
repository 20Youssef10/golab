<!-- Generated from docs/USAGE.md §10. Security and privacy by docs-site/tools/sync-sources.py. Do not edit. -->

## 10. Security and privacy

- Tokens, OAuth codes/verifiers, client secrets and `Authorization` / `Cookie` / proxy-token headers are wrapped in a secret type. They never appear in stdout, logs, error messages or argv. Free-form remote text is redacted defensively before display.
- Only the refresh token is persisted; access tokens are minted on demand. The keyring is probed with a 5-second timeout and falls back to a `0600` file. See [FAQ.md](https://github.com/20Youssef10/golab/blob/main/docs/FAQ.md#where-are-my-credentials-stored) for paths.
- State is written under a lock to a temp file, fsynced and atomically renamed. A corrupt file aborts loudly; it is never treated as empty.
- Untrusted remote output is sanitised before it reaches the terminal (OSC/DCS/APC and query sequences stripped, SGR retained). `edit` aborts on non-404 download errors; uploads are bounded and verify final size.
- TLS verification is always on. There is no host override in release builds.
