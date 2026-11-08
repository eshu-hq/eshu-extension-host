# Operate the extension host

## The image

```bash
docker build -t eshu-extension-host .
```

One static binary at `/extension-host`, running as user
`65532`, exposing port `8082`. In default mode it needs no
credentials, no volumes, and no network access to start.
Enabling claims adds two needs: the collector binary has to
exist at the configured path, and the process has to reach
the claim plane URL. The image doesn't carry a collector
for you.

## What to watch

`GET /healthz` and `GET /readyz` return the same JSON:
`status`, `mode` (`dark` means off, `enabled` means
claiming), `claims_enabled` (`0` or `1`), and `version`.
There are no dependencies to probe here, so ready simply
means serving. Don't read more into it than that.

`GET /metrics` serves Prometheus text with
`extension_host_build_info`, `extension_host_claims_enabled`,
and `extension_host_uptime_seconds`. The middle one is the
one you'll actually glance at.

Logs are structured JSON on stderr. The events worth knowing
are `runtime.startup.ready`, `runtime.serve.failed`,
`runtime.startup.failed`, `runtime.dryrun.compare`, and
`runtime.claim.failed`. Child stderr is thrown away, so if a
collector is unhappy you won't see its complaints here.
You'll see the claim fail, and that's your cue to run the
collector by hand.

Diagrams live in [diagrams](../diagrams/README.md).
