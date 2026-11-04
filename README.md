# eshu-extension-host

Eshu extension host trust and bounded execution. Pre-release: holds no cutover authority until its reviewed cutover. Refs #8877, part of #6707.

> Dark posture (Leaf 2): this binary serves operator health only and
> carries no enablement path. It never opens Postgres, never dials a claim
> plane, and never claims work. Any claim-flow flag is a startup error.
> The enabled flow arrives in a later leaf behind #9376 -> #9381.

## Build and test

```bash
make ci      # lint + test + race (with the coverage floor); CI stays authoritative
make lint    # vet, golangci-lint, gofumpt, tidy, 500-line cap
make test    # build + full test suite
make race    # race suite + coverprofile + coverage floor (90.0, may only rise)
```

Needs `golangci-lint` and `actionlint` on `PATH`; CI pins its own copies
(see CONTRIBUTING.md).

## Run the dark binary

```bash
go run ./cmd/extension-host --admin-listen=:8082
curl http://127.0.0.1:8082/healthz   # {"status":"ok","mode":"dark","version":"dev"}
curl http://127.0.0.1:8082/metrics   # extension_host_claims_enabled 0
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--admin-listen` | `:8082` | admin listen address (health, readiness, metrics) |
| `--version` | — | print the stamped build version and exit |

There is no enablement flag. `--claims-enabled`, `--claim-plane-url`,
and `--deployment-mode` are all startup errors by construction.

## Operator signals

- `GET /healthz` — `{"status":"ok","mode":"dark","version":"<stamp>"}`.
- `GET /readyz` — serving readiness (no dependencies, so ready = serving).
- `GET /metrics` — `extension_host_build_info`, `extension_host_uptime_seconds`,
  and `extension_host_claims_enabled 0` (zero claim flow, observably).
- Structured JSON logs on stderr: startup posture
  (`event_name=runtime.startup.ready`, `mode=dark`, `claims_enabled=false`)
  and serve failures (`event_name=runtime.serve.failed`).

## Configuration

No credentials, no volumes, no environment contract. The dark image starts
with no env at all; unknown flags fail closed at parse time. Resource
footprint: one static binary, read-only root filesystem compatible.

## Provenance, release, rollback

- Source pin and stub-state record: [PROVENANCE.md](PROVENANCE.md).
- Release: tag `v[0-9]*` runs `.github/workflows/release.yml` — Trivy
  HIGH/CRITICAL gate, then multi-arch push with SLSA provenance and SPDX
  SBOM attestations, then a GitHub Release. The overlay pins the published
  digest, never a floating tag.
- Rollback: delete-path-suffices per the #9383 disposition (host owns zero
  durable state); the rehearsal lives in `EXTRACTION_PREP.md` §8 of the
  source pin and runs against the exact deployed digest.
