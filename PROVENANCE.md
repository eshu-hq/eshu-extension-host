# Source provenance (eshu-hq/eshu#8877 Leaf 2)

## What this scaffold is

Dark deploy surface for the extracted extension host: a health-only host
binary, a digest-pinned Dockerfile, and a tag-gated publish pipeline with
SLSA provenance. Zero claim flow by construction — no enablement path
exists in the binary. Refs #8877 (no closing keyword; the cutover stays
staged behind #9376 -> #9381). The dark Deployment/Service overlay is NOT
in this repo: it lands via the deploy PR (boatsgroup#532), staged behind
the first tag release, and pins the published digest there.

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
  newest = `v1` (`go.mod:159-161`, `EXTRACTION_PREP.md` §5). The rc.15
  factschema pin is historical (eshu has since moved to `v0.1.0-rc.17`
  per eshu#10043); it is recorded here as scaffold-time truth only. The
  dark binary couples to no pin: this module's `go.mod` carries only the
  `go 1.26.10` directive (no external dependencies, asserted by
  the module-import allowlist test and the `go list -m all` lint gate).
- The dark binary vendors none of the above: it is new stdlib-only code,
  so there is no fork to drift. The claim-path wiring (Leaf 6) moves the
  sources; the in-tree removal (Leaf 9) deletes them last.

## Degenerate compatibility matrix (eshu#8877 Leaf 6)

Mirrors `EXTRACTION_PREP.md` §5 of the eshu source pin. Only one host
release exists (`v0.1.0`, `git tag -l`), so newest-host and oldest-host
are the same cell; only one claim-plane major (`1`) exists, so
oldest-core-major and newest-core-major are the same cell:

| Host | Core major | Standing |
| --- | --- | --- |
| newest-host `v0.1.0` | oldest-core-major `1` | covered |
| oldest-host `v0.1.0` | newest-core-major `1` | covered (same cell) |

Re-derivation: `git tag -l` → `v0.1.0` only; eshu `go/go.mod:159-161`
→ `collector v0.2.0-rc.3`, `factschema v0.1.0-rc.17`,
`workflow v0.1.0`; `sdk/go/collector/types.go:10` → `v1alpha1` default,
opt-in `v1alpha2`/`v1alpha3`, single MAJOR invariant.

Expiry triggers (verbatim): box 9 reopens on second host release tag,
workflow.v2 or payload major 2, host go.mod dependency or claim-flow
flag, or eshu go.mod pin bump without table update.
Byte-compared 2026-11-05 against eshu `drive/8877-leaf-6-matrix`
`3660eb1e` (`EXTRACTION_PREP.md` §5, eshu PR #10202); re-compare on any
edit to either copy.

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
  rejects the six listed claim-plane-capable import prefixes, and
  `TestModuleImportsAreAllowlisted` asserts the whole-module invariant this
  sentence stakes: zero non-stdlib external imports, so no claim-plane-capable
  dependency can enter under an unlisted path; intra-module imports are
  allowed only under the own-module carve-out guarded by
  TestOwnModuleAllowlistIsNarrow and the unchanged ban list. The lint lane
  additionally asserts `go list -m all` reports only this module.
- Image provenance: first tag release records `subject-digest` plus SLSA
  and SBOM attestations in GHCR; the deploy overlay (boatsgroup#532)
  pins that digest.
