// Package update implements the throttled update check and signed self-update
// for golab. It replaces upstream's PyPI probe (src/colab_cli/auto_update.py,
// mirrored in tests/test_update.py) with a pinned GitHub Releases source: the
// owner/repo are supplied by the composition root and are never user-editable.
//
// Every release MUST publish three assets (docs/03 ADR-014, docs/08 §3):
//
//   - checksums.txt with a SHA-256 digest of the platform archive;
//   - <checksums>.sigstore.json: a cosign keyless Sigstore bundle for the
//     archive, with an in-toto statement naming that digest;
//   - <checksums>.sbom.sigstore.json: a second cosign keyless bundle carrying
//     a CycloneDX or SPDX SBOM attestation for the same archive.
//
// Both bundles must be signed by the pinned GitHub Actions release workflow of
// Owner/Repo and verified against the Sigstore public good instance's TUF
// trusted root (Fulcio CA chain, Rekor inclusion proof/SET, signing time,
// OIDC issuer + workflow identity and the artifact digest). Missing or invalid
// verification material refuses the update; there is no unverified fallback.
//
// The package takes plain values (no internal/state, internal/app or
// internal/cli imports); the caller persists LastCheck and LatestVersion. No
// function writes to stdout — the upgrade banner is delivered through a
// callback so the CLI can route it to stderr. All release-server network I/O
// is injectable through Config.HTTPClient; the TUF trusted-root refresh has a
// separate hardcoded mirror and explicit timeout.
package update
