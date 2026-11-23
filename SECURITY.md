# Security Policy

golab is an unofficial Go port of Google's Colab CLI. It handles Google OAuth
refresh tokens, runtime proxy tokens, SSH keys and remote code execution on
Colab VMs, so security reports are taken seriously.

## Supported versions

| Version | Supported |
|---|---|
| 0.1.x | ✅ Supported (security fixes on the latest patch release) |
| 0.1.0-alpha.x and earlier pre-releases | ❌ Not supported |
| Development snapshots / `main` | Best effort — please test against the latest release first |

Once v1.0 ships, this project supports the latest minor line and the previous
minor for security fixes.

## Reporting a vulnerability

**Report privately.** Do **not** open a public issue for a security problem, and
especially never paste tokens, credentials or raw authenticated traffic into a
public issue, discussion or pull request.

Use **GitHub Security Advisories** to report privately:

<https://github.com/20Youssef10/golab/security/advisories/new>

A private advisory reaches the maintainer without exposing the report. For
conduct concerns, use the same channel (see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md));
if an advisory cannot be resolved through the project's own process, maintainers
may escalate to GitHub Support.

### Response timeline (project policy, not a guarantee)

These are targets we aim for, not contractual commitments:

| Stage | Target |
|---|---|
| Acknowledge receipt | within 3 days |
| Initial triage and severity assessment | within 7 days |
| Fix or mitigation target | within 30 days, depending on severity and complexity |

If a fix needs longer (for example because it depends on an upstream protocol
change), we will say so in the advisory and agree on a disclosure timeline with
the reporter. Please give us a reasonable window to fix before public disclosure.

## Scope

Security issues we want to hear about include:

- **Secret leakage** — tokens, OAuth codes/verifiers, client secrets or proxy
  tokens reaching logs, argv, environment dumps, error messages, crash output,
  or process listings (the typed `Secret`/redaction rules in `AGENTS.md`).
- **State corruption** — bypassing `internal/state`'s lock + temp + fsync +
  rename atomicity, path traversal via session names, or silently treating
  corrupt state as empty.
- **Injection** — user-controlled strings spliced into code executed on the VM
  kernel, command injection in helpers, or unsafe local/remote path handling.
- **Updater verification** — bypassing checksum or cosign/Sigstore identity
  verification, accepting unsigned or wrong-identity artifacts, missing or
  unverified SBOM attestations, downgrade attacks, or a user-editable update URL.
- **Terminal escapes** — untrusted remote output injecting OSC/DCS/APC/query
  sequences (e.g. OSC-52 clipboard writes) past the `render` sanitiser.
- Authentication/authorization bypasses, privilege issues, and supply-chain
  weaknesses in the release pipeline.

## Out of scope

- Colab **service outages, incidents or account/quota limits** — report those to
  Google, not here.
- **Upstream API changes** that break compatibility without a security impact
  (track those as normal issues; see the upstream-sync policy in `docs/06 §5`).
- The already-documented **third-party OAuth client risk** (risk R-02 in
  `docs/08`): use of Google's bundled cloud-SDK client identity by a third-party
  binary is a known legal/operational caveat, disclosed in the docs, not a new
  vulnerability in this port.
- Spam, support questions, feature requests, or non-security bugs (use the
  issue templates instead).
- Vulnerabilities in third-party services or dependencies that we do not ship
  or invoke — report those to their owners; we will still triage malicious
  dependency issues affecting our build.

## Safe-disclosure notes

- **No tokens in reports.** Redact access/refresh tokens, `Authorization`,
  `Cookie`, `X-Colab-Runtime-Proxy-Token`, `X-Goog-Colab-Token`, OAuth codes,
  verifiers and client secrets. Do not include passwords or full SSH keys.
- For protocol bugs, attach **redacted wire captures**: strip all headers and
  bodies containing credentials, session identifiers or personal data. Prefer a
  description of the request shape over a full transcript.
- Include the output of `golab version`, OS/arch, the exact command, and a
  minimal reproduction. State clearly what you redacted.
- Keep the advisory private until a fix or mitigation is available; we will
  coordinate public disclosure and credit with you.
