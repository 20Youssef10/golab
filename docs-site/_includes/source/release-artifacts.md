<!-- Generated from docs/RELEASING.md §3. Required signed artifacts by docs-site/tools/sync-sources.py. Do not edit. -->

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
