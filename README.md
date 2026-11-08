# eshu-extension-host

Status: pre-release. By default this binary does no collection work at
all (see "Off by default" below). It does not yet replace the host that
runs inside Eshu core.

One-line identity: the supervisor process that can claim collector
work from Eshu, run one collector program per job over standard input
and output, and send the result back.

## What it is

Some Eshu collectors ship as separate programs ("component
collectors"). They do not poll for work themselves. Something must ask
Eshu for the next job, start the collector, keep the job's lease alive
while it runs, and return what it produced. That job is this host.

Words used in this file:

- Claim: one unit of collector work handed out by Eshu. Holding a
  claim is time-limited.
- Lease: that time limit. A heartbeat is a short "still working"
  message that extends it.
- Claim plane: the Eshu HTTP service that hands out claims and accepts
  results. This host is a client of it.
- Stdio: standard input and output. The host writes one JSON request
  to the collector's stdin and reads one JSON result from its stdout.

## Why it exists

Collector programs stay small and one-shot: read one request, write
one result, exit. This host owns the long-running parts — polling,
leases, retries, and result delivery — once, for whichever collector
binary it is pointed at.

## Off by default

Plainly: a plain start collects nothing. With no enabling flag or
environment value, the binary serves health and metrics only. It does
not open a database, does not contact the claim plane, and does not
start any collector. Collection runs only when an operator turns it on
with `--claims-enabled` or `ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1`.
Any other value for that variable fails at startup.

```mermaid
flowchart LR
  CP[Eshu claim plane] -->|next claim| H[extension-host<br/>this repo]
  H -->|one JSON request on stdin| COL[Collector program<br/>child process]
  COL -->|one JSON result on stdout| H
  H -->|commit result, then complete| CP
```

Text version: the host asks the claim plane for work. For each claim
it starts the collector program, sends one JSON request on stdin,
heartbeats while it runs, reads one JSON result from stdout, commits
that result to the claim plane, then marks the claim complete.

## When to use it

Use it when a deployment must run a component collector as a separate
program under this standalone host, with claims turned on.

Do not use it when:

- You expect it to collect on a plain start. It will not (see above).
- You need a collector itself. This repo holds no collector logic; it
  runs a collector binary supplied at runtime.
- You need stored state. The host keeps none; claims and results live
  in Eshu.

## How to run it

Health-only start (the default posture):

```bash
go run ./cmd/extension-host --admin-listen=:8082
curl http://127.0.0.1:8082/healthz   # {"status":"ok","mode":"dark",...}
curl http://127.0.0.1:8082/metrics   # extension_host_claims_enabled 0
```

"Dark" in the output is this codebase's word for the off-by-default
mode. `mode` becomes `enabled` and `extension_host_claims_enabled`
becomes `1` only when claims are turned on.

Enabled start also needs the environment in Configuration below,
including the path of the collector binary to run:

```bash
ESHU_EXTENSIONHOST_COLLECTOR_PATH=/path/to/collector \
  ./extension-host --claims-enabled
```

Add `--dry-run` to rehearse: claims, leases, and heartbeats still run
for real, but results are only logged (byte count and hash), never
committed. `--dry-run` without claims enabled is a startup error.

## How it works

Per claim, in order (enabled mode only):

1. Ask the claim plane for the next claim matching the configured
   collector kind and instance id. If none is ready, wait and retry.
2. Build one request and start the collector program. The request
   carries `protocol_version` (`collector-sdk/v1alpha1`), a `claim`
   (component and instance ids, collector kind, source system, scope,
   run and work-item ids, attempt, deadline, and config handle), and
   an empty `contract` block. It is written to the child's stdin.
3. Heartbeat the lease while the child runs. The heartbeat interval is
   one third of the lease time.
4. Read stdout with a 4 MiB cap. The output must be exactly one JSON
   value; extra data or oversize output fails the claim. Child stderr
   is discarded, so nothing a child prints can reach logs.
5. Commit the result bytes unchanged, then complete the claim. If the
   run or commit fails, the claim is failed as retryable (it may be
   claimed again) or terminal when the error says the producer grant
   was denied. A producer grant is Eshu's permission record saying a
   named producer may emit a named fact kind.

Data in: claims from the claim plane. Data out: the collector's result
bytes, committed unchanged, plus JSON log lines on stderr. The host
never edits a result. Cancelling (SIGINT or SIGTERM) stops cleanly and
leaves any held lease to expire on the claim plane side.

## Configuration

Flags are fixed: unknown flags fail at startup.

| Flag | Default | Meaning |
|---|---|---|
| `--admin-listen` | `:8082` | Address for `/healthz`, `/readyz`, `/metrics` |
| `--version` | — | Print the build version and exit |
| `--claims-enabled` | `false` | Turn on the claim loop |
| `--dry-run` | `false` | Log results instead of committing (needs claims on) |

Environment for the enabled path (names read in
`cmd/extension-host/wireup.go` and `internal/worker/config.go`):

| Variable | Required | Meaning |
|---|---|---|
| `ESHU_EXTENSIONHOST_CUTOVER_ENABLED` | No | Exactly `1` turns claims on; `--claims-enabled=false` does not override it |
| `ESHU_EXTENSIONHOST_COLLECTOR_PATH` | Yes, when enabled | Collector binary to run per claim |
| `ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_ID` | Yes, when enabled | Producer identity checked against grants |
| `ESHU_EXTENSIONHOST_CUTOVER_PRODUCER_VERSION` | Yes, when enabled | Producer version (checked set, not sent) |
| `ESHU_COMPONENT_COLLECTOR_INSTANCE_ID` | Yes, when enabled | Instance half of the claim match; never derived from the producer id |
| `ESHU_COMPONENT_COLLECTOR_SCOPE_KIND` | No (default `component`) | Collector kind to claim; also the scope kind sent to the child |
| `ESHU_COMPONENT_COLLECTOR_CONFIG_HANDLE` | Yes, when enabled | Config handle echoed in each child request |
| `ESHU_WORKFLOW_CLAIM_PLANE_URL` | Yes, when enabled | Claim plane base URL (`http` or `https`) |
| `ESHU_COMPONENT_COLLECTOR_POLL_INTERVAL` | No (default `1s`) | Wait between empty polls |
| `ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL` | No (default `60s`) | Requested lease length; heartbeat is one third |

Startup errors name the missing or bad variable, never its value.
There is no config file in this repo.

## Deploy

```bash
docker build -t eshu-extension-host .
```

The image is one static binary at `/extension-host`, running as user
`65532`, exposing port `8082`. In the default mode it needs no
credentials, no volumes, and no network access to start. Enabling
claims adds two needs: the collector binary must be present at the
configured path, and the process must reach the claim plane URL.
Release process: a `v[0-9]*` tag runs `.github/workflows/release.yml`
(per-architecture Trivy checks, multi-arch image push with provenance
and SBOM records, then a GitHub Release). Details and the source
record are in [PROVENANCE.md](PROVENANCE.md).

## Eshu dependencies

- Needs: in default mode, nothing. When enabled, the claim plane, a
  collector binary, and the producer/instance settings above.
- Talks to: the claim plane over HTTP, and the collector child over
  stdio. Those are its only two interfaces besides the admin listener.
- Never touches: Postgres or any database, the graph, or collector
  internals. It holds no durable state — deleting the deployment loses
  nothing but any in-flight lease, which expires on its own.

## Operations

- `GET /healthz` and `GET /readyz` return the same JSON:
  `status`, `mode` (`dark` means off, `enabled` means claiming),
  `claims_enabled` (`0` or `1`), and `version`. There are no
  dependencies to probe, so ready means serving.
- `GET /metrics` serves Prometheus text: `extension_host_build_info`,
  `extension_host_claims_enabled`, and
  `extension_host_uptime_seconds`.
- Logs are structured JSON on stderr. Key events:
  `runtime.startup.ready`, `runtime.serve.failed`,
  `runtime.startup.failed`, `runtime.dryrun.compare`, and
  `runtime.claim.failed`.
- Common 3 AM answer: if no collection happens, read `/healthz`
  first. `mode":"dark"` means claims were never turned on — that is a
  setting, not an outage.

## Development

Go version is pinned in `go.mod` (`go 1.26.10`, module
`github.com/eshu-hq/eshu-extension-host`). The module has no external
dependencies; tests enforce a standard-library-only import boundary.

```bash
make ci          # lint + test + race, with the coverage floor
make lint        # vet, golangci-lint, format check, tidy, 500-line cap
make test        # build + full test suite
make race        # race suite + coverage floor (90.0)
make actionlint  # workflow file lint
```

`make ci` needs `golangci-lint` and `actionlint` on `PATH`; CI pins
its own copies (see [CONTRIBUTING.md](CONTRIBUTING.md)).

## Docs and related

- [PROVENANCE.md](PROVENANCE.md) — source pin, release, and rollback
  notes.
- [CONTRIBUTING.md](CONTRIBUTING.md) — local gates and CI lanes.
- `internal/cutovercontract/contract.go` — the claim and runner
  interfaces. `internal/adapter/runner.go` — the stdio child runner.

## Contributing, security, license

See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.
There is no SECURITY.md in this repo. The license is MIT; see
[LICENSE](LICENSE).
