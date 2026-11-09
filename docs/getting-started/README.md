# Getting started

The friendly path here is to start dark, confirm health,
and only then think about claims. A plain start serves
health and metrics and touches nothing.

```bash
go run ./cmd/extension-host --admin-listen=:8082
curl http://127.0.0.1:8082/healthz   # {"status":"ok","mode":"dark",...}
curl http://127.0.0.1:8082/metrics   # extension_host_claims_enabled 0
```

## To actually claim work

You need `--claims-enabled` (or
`ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1`) plus the environment
in [configuration](../reference/configuration.md),
including the path of the collector binary to run.
`--dry-run` rehearses the enabled flow without committing
results.

```bash
ESHU_EXTENSIONHOST_COLLECTOR_PATH=/path/to/collector \
  ./extension-host --claims-enabled
```

Add the rest of the required variables from the reference
before you try that for real, or startup will list them for
you one at a time. `mode` becomes `enabled` only when claims
are actually on. Next, read the
[claim contract](../concepts/contracts.md).
