# Source provenance (eshu-hq/eshu#8877 Leaf 2)

## What this scaffold is

Dark deploy surface for the extracted extension host: a health-only host
binary, a digest-pinned Dockerfile, a tag-gated publish pipeline with SLSA
provenance, and a dark Deployment/Service overlay. Zero claim flow by
construction — no enablement path exists in the binary. Refs #8877 (no
closing keyword; the cutover stays staged behind #9376 -> #9381).

## Source pin (in-tree truth this scaffold stages)

- Eshu `origin/main` at scaffold time:
  `b6e74d369d8a0cee88113669a678f63cf31ad4a8` (2026-11-04).
- Staged packages (still in-tree; move lands in a later leaf):
  `go/cmd/collector-component-extension/` (worker binary: `main.go`,
  `service.go`, `config.go`, `source_state.go` plus tests),
  `go/internal/collector/extensionhost/` (`doc.go`, `source.go`,
  `process.go`, `oci.go`, `mapping.go`, `failure.go`, `claim_outcome.go`,
  validators and tests), `go/internal/component/` (`registry.go`,
  `policy.go`, `producer_grant.go`, `grant_decision.go` plus tests).
  Full inventory lives in `EXTRACTION_PREP.md` §1 of the pinned commit.
- Contract pins at the source commit: `eshu-sdk/collector v0.2.0-rc.3`,
  `eshu-sdk/factschema v0.1.0-rc.15`, claim plane `workflow.v1` oldest =
  newest = `v1` (`go.mod:159-161`, `EXTRACTION_PREP.md` §5).
- The dark binary vendors none of the above: it is new stdlib-only code,
  so there is no fork to drift. The claim-path wiring (Leaf 6) moves the
  sources; the in-tree removal (Leaf 9) deletes them last.

## Destination stub state at scaffold time

`eshu-hq/eshu-extension-host` `HEAD 6a5d1848` (verified live 2026-11-04;
root `{README.md, Makefile, CONTRIBUTING.md, .github/}` only, holds no
cutover authority). `EXTRACTION_PREP.md` §6 of the source pin describes
exactly this state — no discrepancy, no reconcile owed.

## Durable-state ownership

The host owns zero durable state (source `EXTRACTION_PREP.md` §4:
fact commit, claim control, and lifecycle stay core-owned). The dark
binary owns no stores at all — no Postgres, no graph, no queue. The
#9383 rollback disposition (delete-path-suffices, conditional on zero
satellite-owned durable state) therefore holds for this surface; it voids
automatically if #9381 wiring ever adds a satellite-owned store.

## Re-verify

- `git -C <eshu> show <pin>:go/cmd/collector-component-extension/EXTRACTION_PREP.md`
  reproduces the §1/§5 inventory above.
- `go test ./... -race` plus `TestImportBoundaryRejectsClaimPlaneImports`
  proves no in-tree or claim-plane-capable import entered this module.
- Image provenance: first tag release records `subject-digest` plus SLSA
  and SBOM attestations in GHCR; the overlay pins that digest.
