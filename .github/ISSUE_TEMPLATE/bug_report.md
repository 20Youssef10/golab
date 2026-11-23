---
name: Bug report
about: Report a reproducible problem with golab
title: "[Bug]: "
labels: bug
assignees: "20Youssef10"
---

## Description

<!-- A clear, one-paragraph description of the problem. -->

## Environment

- `golab version` output (implementation name matters — a globally installed Python `colab` can shadow this binary; also paste `which colab` / `colab version`):
- OS and architecture (e.g. `uname -a`, Windows build, Termux `uname -m`):
- Go version, if built from source (`go version`):
- Upstream compatibility target (`golab version` prints it):

## Command to reproduce

```bash
# Paste the exact command (redact any tokens, paths or account identifiers).
golab ...
```

Include a minimal script or input file if needed.

## Expected behaviour

<!-- What did you expect to happen? Link to upstream google-colab-cli behaviour if relevant. -->

## Actual behaviour

<!-- What actually happened? Include the exact error message and exit code. -->

## Logs (redacted)

```
<!-- Paste relevant output. `[colab] …` diagnostics go to stderr; program output goes to stdout.
     REMOVE all tokens, OAuth codes/verifiers, client secrets, Authorization/Cookie headers,
     X-Colab-Runtime-Proxy-Token / X-Goog-Colab-Token values, account emails and VM URLs. -->
```

- [ ] I have **redacted all tokens, credentials, authenticated wire captures and personal data** from logs and screenshots.
- [ ] I confirm this report contains **no secrets** (see [SECURITY.md](../../SECURITY.md)); security issues go to [Security Advisories](https://github.com/20Youssef10/golab/security/advisories/new), not public issues.

## Additional context

<!-- Does it happen on the latest release? On other platforms? With the official Python CLI too? -->
