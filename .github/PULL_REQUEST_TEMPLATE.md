## Summary

<!-- What does this change do, and why? Link the issue if there is one. -->

## Verification

- [ ] **Test first:** the failing test was written and seen to fail before implementation; the upstream Python test it ports is named in a comment.
- [ ] New/changed endpoints are backed by captured fixtures in `testdata/wire/` (no guessed wire shapes).
- [ ] `make check` is green (`go vet`, `go test -race`, `golangci-lint`, redaction + paths scripts).
- [ ] `make cross` is green for platform-touching changes.

Paste the relevant command output here:

```
# make check
# make cross   (if applicable)
```

## Documentation

- [ ] Re-read the affected `docs/` page(s) and updated them; added a dated note to the frontmatter/changelog section (per `AGENTS.md`).
- [ ] Added a `CHANGELOG.md` entry under `[Unreleased]` for user-visible changes.
- [ ] Updated ADR / DEC log (`docs/03`, `docs/08`) if this changes a design decision.

## Security & live proof

- [ ] **No secrets in the diff** — no tokens, OAuth codes/verifiers, client secrets, `Authorization`/`Cookie` headers, proxy tokens, raw authenticated wire captures, or personal data.
- [ ] Redaction golden test / `make redaction` passes; no new secret reaches logs, argv or errors.
- [ ] Live e2e run by a **human** for auth/interactive-TTY/drivemount/release-affecting changes, or explicitly requested from a maintainer (agents cannot run these — see `AGENTS.md` and `docs/REMEDIATION.md`). Describe who ran it and the result, or state that it is pending.
- [ ] No live VM left allocated; `golab sessions` is empty.

## Review

- [ ] Branch is based on current `main`; full cumulative diff is `git diff main..<branch>` (not `git show <sha>`).
- [ ] Breaking changes (CLI surface, flag semantics, on-disk schema, token format) are called out here and were approved by a maintainer.

<!-- By submitting this PR you agree to the Code of Conduct: https://github.com/20Youssef10/golab/blob/main/CODE_OF_CONDUCT.md -->
