<!-- Generated from docs/05-command-parity.md §Global flags (all commands), Deliberate deviations (Δ) summary by docs-site/tools/sync-sources.py. Do not edit. -->

## Global flags (all commands)
| Flag | Default | Go notes |
|---|---|---|
| `-c`, `--client-oauth-config PATH` | `~/.colab-cli-oauth-config.json` | missing file ⇒ bundled client |
| `--config PATH` | `<cfgdir>/sessions.json` | per-process state isolation |
| `--logtostderr` | off | Δ also `COLAB_DEBUG=1`; logging off by default |
| `--auth {oauth2,adc}` | `oauth2` | case-insensitive; Δ add env `COLAB_AUTH` |
| `-h`, `--help` | – | alphabetical command list |

Auto-update background check runs for every command **except** `update version log pay help url whoami readme README skill SKILL` and piped `ssh` (stdout not a TTY). Δ In Go it must **never block or write to stdout**; run it concurrent with the command, print the notice to stderr at exit, throttled to once per 24 h.

---

## Deliberate deviations (Δ) summary
| ID | Deviation | Why |
|---|---|---|
| Δ1 | `[colab] …` chatter on **stderr** for every command | clean stdout for piping (DEC-07) |
| Δ2 | Embedded SSH client; `--proxy-mode` retained | no OpenSSH dependency (ADR-005) |
| Δ3 | Atomic state writes; abort on corrupt state | fixes D-1 |
| Δ4 | Session-name validation | fixes D-2 |
| Δ5 | Chunked upload / streamed download | fixes D-4 |
| Δ6 | Logging off by default; rotating file | fixes D-6 |
| Δ7 | Always-set HTTP timeouts | fixes D-5 |
| Δ8 | SIGINT interrupts the remote kernel first | `run` should behave like `python script.py` (DEC-05) |
| Δ9 | Token store tiers + one-time import of Python's keyring entry | ADR-006 |
| Δ10 | Signed self-update | fixes D-9 |
| Δ12 | `edit` aborts on non-404 download errors | fixes D-11 |
| Δ11 | `COLAB_AUTH`, `COLAB_CONFIG_DIR`, `COLAB_DEBUG` env vars; `completion` command | ergonomics |
