# Releasing golab

> **This document is the authoritative release runbook.** Releases happen
> **only when the requester (user/maintainer) explicitly asks** — never cut one
> on your own, and confirm the inferred version with them before tagging
> (`AGENTS.md` "Release rules", `GOVERNANCE.md`). SemVer rules and the definition
> of a breaking change are in `GOVERNANCE.md` and the CHANGELOG preamble.

## 0. Preconditions (do not skip)

- [ ] The requester explicitly approved a release and the target `vX.Y.Z`.
- [ ] CI is green on `main`: `go vet`, `go test -race`, `golangci-lint`,
      `govulncheck`, `go mod verify`, redaction/paths scripts, cross-build smoke,
      coverage/fuzz gates — see `.github/workflows/ci.yml` and `make check`.
- [ ] The **human-only e2e items in [`docs/REMEDIATION.md`](REMEDIATION.md) are
      closed, or a maintainer has explicitly waived them in writing.** No release
      may be cut while those items are open without such a waiver. They include:
      live `auth`/OAuth consent, interactive `repl`/`ssh`, `drivemount`, the
      >75 MiB chunked-upload SHA-256 check, the 8 🔴 wire-capture items, one CPU
      `run` e2e ending with an empty `golab sessions`, signed-release verification,
      Windows `.old` cleanup, the Termux/Windows console matrix and the
      `HTTP_PROXY` probe. Record the waiver or evidence in the release PR.
      **Exception:** the real updater install test in §5 item 3 is
      **non-waivable** — a release that cannot be installed by its own updater
      is not releasable (see the v0.1.6 legacy-bundle failure and the v0.1.7
      SBOM predicate-type failure in
      [`CONFORMANCE.md` §4](CONFORMANCE.md#4-release-pipeline-and-updater-identity)).
- [ ] Live probes never leave billable VMs allocated; `golab sessions` is empty.
- [ ] Upstream compatibility target in `internal/version` is still accurate
      (check `docs/COMPAT.md` / upstream-sync triage).

## 1. Prepare the version

1. Create a release branch off up-to-date `main`.
2. **Bump the version** in `internal/version/version.go`: update the `Number`
   constant (e.g. `"0.1.0"` → `"0.1.1"`) and, if needed, `Upstream`. Do not change
   `ClientAgent()`'s format — the version suffix is derived from `Number`.
3. **Update `CHANGELOG.md`**: rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD`
   (Keep a Changelog format), add a fresh empty `[Unreleased]` section above it,
   and add the compare link at the bottom
   (`[X.Y.Z]: https://github.com/20Youssef10/golab/compare/vPREV...vX.Y.Z`).
   Include a Verification note stating which human checks passed or were waived.
4. Re-read the affected `docs/` pages (at minimum `docs/COMPAT.md`,
   `docs/CONFORMANCE.md` and this file) and add dated notes where behaviour
   changed, per `AGENTS.md`.
5. Open a **changelog/release PR**, get maintainer review, and **merge it into
   `main`**. CI must be green on the merge commit.

## 2. Tag and trigger the release

The tag must be **annotated** and point at the merged release commit; the
workflow checks `git cat-file -t "$TAG" = tag` and fails on a lightweight tag.

```bash
git fetch origin && git checkout main && git pull --ff-only
git tag -a vX.Y.Z -m "golab vX.Y.Z"
git push origin vX.Y.Z          # never force-push tags; never force-push main
```

Pushing `v*` triggers [`.github/workflows/release.yml`](../.github/workflows/release.yml)
(`on: push: tags: - "v*"`). **Do not rename that workflow file** — its path is
part of the updater's pinned signing identity (§4). The job:

1. refuses lightweight tags;
2. runs `goreleaser check` and `goreleaser release --snapshot --clean` (no publish);
3. runs `goreleaser release --clean` to publish archives + `checksums.txt`;
4. generates a CycloneDX SBOM (`dist/sbom.cdx.json`) and, per archive, publishes
   a cosign keyless signature bundle and a signed SBOM attestation (both with
   cosign v2.6.5's `--new-bundle-format`; the attestation uses the exact predicate
   URI `--type https://cyclonedx.org/schema/bom-1.5`, never the `cyclonedx`
   alias), then uploads all bundles with
   `gh release upload` (no `--clobber`: published signatures are immutable);
5. verifies every published asset class against the real repo identity — all
   checksums (`sha256sum -c`), every archive bundle with `cosign verify-blob`,
   every SBOM bundle with `cosign verify-blob-attestation --type https://cyclonedx.org/schema/bom-1.5`,
   a `jq` format guard asserting each bundle is a v0.3 sigstore bundle
   (`.mediaType` contains `v0.3`, tlog `inclusionProof` present), and a `jq`
   guard decoding each SBOM bundle's DSSE payload and requiring `.predicateType`
   to equal that exact URI. The predicate guard reads `dsseEnvelope` at the
   bundle root **or** under `.content`
   (`(.dsseEnvelope.payload // .content.dsseEnvelope.payload)`) — cosign v2.6.5
   writes it at the root, which is why v0.1.8's run `45428451505` failed with
   `Malformed BOM` before this was fixed. Do not narrow it back to one path.
   Both guards are essential: cosign's verifiers accept
   legacy bundles and both SBOM predicate forms, while `golab update --install`
   rejects the wrong ones.

Monitor the workflow to completion; if any step fails, fix the cause and create a
**new** patch version — never re-tag or force-update a published tag.

## 3. Required signed artifacts

Every release must publish, for `golab_<version>_<os>_<arch>.tar.gz` (tar.gz is
used for **all** targets, including Windows):

| Asset | Purpose |
|---|---|
| `checksums.txt` | SHA-256 digests of every archive (GoReleaser `checksum` config) |
| `golab_<ver>_<os>_<arch>.tar.gz` | platform archive (CGO-free, `-trimpath`) |
| `<archive>.sigstore.json` | cosign `sign-blob` keyless bundle for the archive |
| `<archive>.sbom.sigstore.json` | cosign `attest-blob` DSSE bundle whose predicate is a signed CycloneDX SBOM with in-toto `predicateType` exactly `https://cyclonedx.org/schema/bom-1.5` |

The updater (`internal/update`) requires **all three classes**. A missing or
invalid SBOM bundle fails with `ErrMissingSBOM`; missing/invalid signature or
identity material fails closed — there is no unverified fallback. Keep the exact
names: `checksums.txt`, `*.sigstore.json`, `*.sbom.sigstore.json`. Both bundle
classes must be **sigstore v0.3 protobuf bundles** — cosign v2.6.5 emits these
only with `--new-bundle-format`. Legacy bundles (`base64Signature`, `cert`,
`rekorBundle`) pass `cosign verify-*` but are rejected by
`golab update --install`; the §2 step 5 format guard fails the job if one is
produced. The SBOM attestation's in-toto `predicateType` must likewise be the
exact URI `https://cyclonedx.org/schema/bom-1.5`: the `cyclonedx` alias makes
cosign emit `https://cyclonedx.org/bom`, which passes cosign verification but
makes `golab update --install` fail with "unsupported predicate type". The
producer uses the URI (cosign copies it verbatim) rather than the alias, and the
§2 step 5 predicate guard fails the job on any other value. This is chosen so the
unmodified verifier in already-shipped v0.1.6/v0.1.7 binaries accepts the new
release.

## 4. Updater identity pin

`internal/update/verify.go` verifies Sigstore public-good material (Fulcio CA
chain, Rekor inclusion proof/SET, signing time, artifact digest) against a
short certificate identity pinned to:

```
https://github.com/20Youssef10/golab/.github/workflows/release.yml@…
```

with OIDC issuer `https://token.actions.githubusercontent.com`. The owner/repo
and the **workflow path** are part of that identity. Do not rename
`.github/workflows/release.yml`, move it, or change the repo owner without
updating the pin and its tests (`internal/update/assets_test.go`,
`TestPinnedWorkflowIdentityMatchesGolabRepo`) in the same change. Artifacts
signed under any other identity are rejected by `golab update --install`.

## 5. Verification steps

1. **Asset classes** — confirm the release has all four asset classes from §3
   (the workflow's post-release step already checks this; spot-check manually):
   `gh release view vX.Y.Z --json assets --jq '.assets[].name'`.
2. **Local signature spot-check** (optional but recommended) — verify one
   bundle as the workflow does:
   ```bash
   cosign verify-blob \
     --certificate-identity "https://github.com/20Youssef10/golab/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     --bundle golab_X.Y.Z_linux_amd64.tar.gz.sigstore.json \
     golab_X.Y.Z_linux_amd64.tar.gz
   ```
3. **Real updater test (REQUIRED — NON-WAIVABLE)** — on a **test host** with the
   *previous* version installed, run `golab update --install`. A cosign-green
   workflow is **not** sufficient: v0.1.6 passed every cosign check but its
   legacy bundles made this test fail, and v0.1.7 passed too but its SBOM
   predicateType (`https://cyclonedx.org/bom`, from the `cyclonedx` alias) made
   it fail with "unsupported predicate type". Either failure blocks the release
   and requires a **new tag** (assets are immutable). v0.1.8 never reached this
   check — its workflow failed at the proof guard (run `45428451505`) — and a
   separate install-path defect is being fixed by another agent, so a working
   self-update is not claimed for any published release.
   Confirm it fetches the new release, validates the checksum, signature and
   signed SBOM, and installs the new binary. Then:
   - `golab version` reports the new number and the intended upstream target;
   - `sha256sum -c checksums.txt` passes for the downloaded archive;
   - `golab audit` reports update/credential verification as healthy;
   - a basic command (`golab --help`, a GET-only `golab usage` if credentials are
     present) runs on the test host.
4. At least one Windows and one macOS/Termux host should run the updater check
   for platform-touching releases (running-`.exe` replace, `.old` cleanup,
   execute permissions). If a platform cannot be tested, record that explicitly
   in the release notes.
5. Announce the release only after these checks pass; link the CHANGELOG entry.

## 6. Rollback

- **Do not delete or force-push a published tag/release.** Users may already
  have downloaded it, and the updater's identity/digest checks depend on the
  published artifacts. Deleting also breaks reproducibility and the changelog
  compare links.
- **Preferred: publish a patch release.** Revert the offending change on `main`,
  land a fix, bump to `vX.Y.(Z+1)`, and repeat §§1–5. Add a prominent note in the
  new CHANGELOG entry explaining the rollback.
- **Immediately mitigate a bad release** by marking the GitHub Release as
  pre-release/draft or editing its notes with a warning (do not remove assets),
  and open a priority issue describing the impact. If users must be actively
  blocked from auto-updating to it, raise the updater's minimum accepted version
  or add a version denylist in `internal/update` — as a code change with a new
  patch release, never as a silent server-side switch.
- **If the failure is in signing/SBOM** (wrong identity, missing bundle, wrong
  bundle format, or an SBOM `predicateType` the updater rejects): correct
  `.github/workflows/release.yml` / `.goreleaser.yaml` — for the predicate case,
  set `--type https://cyclonedx.org/schema/bom-1.5` on both `attest-blob` and
  `verify-blob-attestation` rather than changing the verifier, so older shipped
  binaries can install the fix — verify with a fresh `goreleaser check` +
  `goreleaser release --snapshot --clean`, and publish the fix as a new version;
  do not re-sign or overwrite an existing release's assets in place.
- After any rollback, update `docs/REMEDIATION.md` / `docs/CONFORMANCE.md` with
  what failed and close the loop before the next release.
