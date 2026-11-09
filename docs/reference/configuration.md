# Configuration reference: extension host

There are four flags and a handful of environment variables. No
config file. The flag surface is fixed, so an unknown flag fails at
startup rather than being quietly ignored.

## Flags

| Flag | Default | What it does |
|---|---|---|
| `--admin-listen` | `:8082` | Address for `/healthz`, `/readyz`, `/metrics` |
| `--version` | off | Print the build version and exit |
| `--claims-enabled` | `false` | Turn on the claim loop |
| `--dry-run` | `false` | Log results instead of committing (needs claims on) |

`--dry-run` is a rehearsal mode. Claims, leases, and heartbeats run
for real, but results are only logged (byte count and hash), never
committed. Asking for `--dry-run` without claims enabled is a
startup error, not a silent no-op.

## The cutover gate

Claiming turns on two ways: `--claims-enabled`, or the environment
variable below set to exactly `1`. Anything else in that variable,
`true`, `yes`, `0`, fails at startup. And note the sharp edge:
`--claims-enabled=false` doesn't override the variable. If the
variable says `1`, claims are on. Check `/healthz` if you're ever
unsure which mode you're in. It tells you plainly.

## Environment for the enabled path

These names are read in `cmd/extension-host/wireup.go` and
`internal/worker/config.go`. In dark mode none of them are needed,
which is rather the point of dark mode.

| Variable | Required | What it does |
|---|---|---|
| `ESHU_EXTENSIONHOST_CUTOVER_ENABLED` | No | Exactly `1` turns claims on |
| `ESHU_EXTENSIONHOST_COLLECTOR_PATH` | Yes, when enabled | Collector binary to run per claim |
| `ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID` | Yes, when enabled | Producer identity checked against grants |
| `ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION` | Yes, when enabled | Producer version (checked as set, not sent) |
| `ESHU_COMPONENT_COLLECTOR_INSTANCE_ID` | Yes, when enabled | Instance half of the claim match, never derived from the producer id |
| `ESHU_COMPONENT_COLLECTOR_SCOPE_KIND` | No, default `component` | Collector kind to claim, also the scope kind sent to the child |
| `ESHU_COMPONENT_COLLECTOR_CONFIG_HANDLE` | Yes, when enabled | Config handle echoed in each child request |
| `ESHU_COMPONENT_COLLECTOR_CONFIG_PATH` | Yes, when enabled | File holding the JSON extension config object sent on each child request |
| `ESHU_WORKFLOW_CLAIM_PLANE_URL` | Yes, when enabled | Claim plane base URL (`http` or `https`) |
| `ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL` | No, default `1s` | Wait between empty polls |
| `ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL` | No, default `60s` | Requested lease length. Heartbeat is one third of it |

A few of these deserve a sentence each:

- The producer id and version identify who's emitting facts, so the
  claim plane can check grants. The version is checked as set but
  isn't sent anywhere. Yes, that's a little odd. It's what the code
  does.
- The instance id is never derived from the producer id. Set it
  explicitly, or startup complains.
- The config file holds one JSON object — the collector instance id
  plus targets, pinned at deploy time from the fleet worker entry —
  cloned into every child request. A missing file, or one that
  isn't a JSON object, fails at startup, so the child never runs
  blind.
- Startup errors name the missing or bad variable, never its value.
  You'll get told what's absent without anyone's settings landing in
  a log.

## Examples

Health only, the default:

```bash
go run ./cmd/extension-host --admin-listen=:8082
curl http://127.0.0.1:8082/healthz   # {"status":"ok","mode":"dark",...}
curl http://127.0.0.1:8082/metrics   # extension_host_claims_enabled 0
```

Enabled, running a collector binary you supply:

```bash
ESHU_EXTENSIONHOST_COLLECTOR_PATH=/path/to/collector \
  ./extension-host --claims-enabled
```

Add the rest of the required variables from the table before you
try that for real, or startup will list them for you one at a time.
`mode` becomes `enabled` and `extension_host_claims_enabled`
becomes `1` only when claims are actually on.
