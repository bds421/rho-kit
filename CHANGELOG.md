# Changelog

## Unreleased

- Clarify v2.0.0 release provenance and security-reporting policy, replacing
  placeholder cryptographic material with the actual GitHub Security Advisory
  and SBOM workflow model.
- Add CODEOWNERS coverage for security-sensitive audit, release, workflow, and
  release-gate files.
- Refresh v2 audit docs for the current 65-module workspace and shipped
  dashboard set.
- Clean up accidental test-only `context.TODO()` and `http.DefaultClient` uses
  outside the intentionally insecure kit-doctor fixtures.
