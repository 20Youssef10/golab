<!-- Generated from docs/08-risks-decisions-security.md §3. Security model by docs-site/tools/sync-sources.py. Do not edit. -->

## 3. Security model

**Assets:** (1) Google OAuth refresh token (the scope set includes `cloud-platform`: broad); (2) runtime-proxy tokens (short-lived, VM control); (3) VM contents and any Drive credentials propagated into it; (4) the user's SSH private keys; (5) the integrity of the installed binary.

**Threats → controls**

| Threat | Control |
|---|---|
| Tokens in logs, argv, env dumps, crash output | typed `Secret`; redaction tests; no secrets on command lines (also for keyring helpers); `slog` off by default |
| Other local users reading state | `0600` files, `0700` dir; Windows owner-only ACL; test both |
| Malicious update / downgrade | pinned release source; checksum **and** cosign identity verification; no user-editable update URL (fixes D-9); refuse downgrade |
| Redirecting tokens to an attacker host | no env/flag to change Colab hosts in release builds (ADR-013); exact host matching (fixes D-7); no `InsecureSkipVerify` |
| Code injection into the VM kernel | never splice user strings into code; JSON/`repr`-quoted payloads (D-3) |
| Path traversal (local & remote) | session-name validator (D-2); clean/reject `..` in history paths; remote paths via `path.Clean`; download refuses to write outside the target |
| Data loss | atomic state writes; abort on corrupt state (D-1); `edit` aborts on non-404 (D-11) |
| Hanging forever | explicit timeouts everywhere (D-5) |
| Hostile output on the local terminal | escape-sequence sanitiser (R-15) |
| SSH MITM beyond TLS | transport already authenticated by TLS+bearer; host keys ephemeral ⇒ accept-any is documented (ADR-005); keys limited to ed25519/ECDSA |
| Supply chain | minimal deps, `govulncheck`, pinned Actions, cosign keyless verification (Fulcio chain + Rekor proof/SET + pinned identity) **and a required signed SBOM attestation**, protected branches. Fulcio SCT checking **is enforced** in production (`internal/update/verify.go:157-163`; only a test-CA opt-out for SCT-less bundles exists), and bundles must be sigstore v0.3 protobuf format — legacy bundles pass `cosign verify-*` but fail `golab update --install` (corrected 2026-11-24; previously recorded as an "SCT gap"). |
| Typosquatting | distinct names; documentation of canonical install commands; checksums |

**Review gate (WP-21):** threat-model walk-through, `gosec`, dependency audit, fuzz run ≥ 1 h per decoder, manual token-leak hunt (`strings`, `ps`, `strace -f -e trace=execve`, Process Monitor on Windows).
