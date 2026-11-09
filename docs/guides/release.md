# Release the extension host

A `v[0-9]*` tag runs `.github/workflows/release.yml`:
per-architecture Trivy checks, a multi-arch image push with
provenance and SBOM records, then a GitHub Release. The
image name is `ghcr.io/eshu-hq/eshu-extension-host`.

The source pin and rollback notes live in
[PROVENANCE.md](../../PROVENANCE.md). Run `make ci` before
you tag; see [contributing](../contributing/README.md).
