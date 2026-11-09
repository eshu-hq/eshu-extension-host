# Boundaries

## Off by default, and we mean it

A plain start collects nothing. With no enabling flag or
environment value, the binary serves health and metrics
only. It doesn't open a database, doesn't contact the claim
plane, and doesn't start any collector. The codebase calls
this "dark," and `/healthz` says so: `"mode":"dark"`, with
`extension_host_claims_enabled 0` in metrics.

Collection runs only when an operator turns it on with
`--claims-enabled` or `ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1`.
Any other value for that variable fails at startup. There's
no quiet middle state where it's half on. The exact gate
rules, including the sharp edge that
`--claims-enabled=false` doesn't override the variable, are
in [configuration](../reference/configuration.md).

## What it needs, and what it never touches

In default mode it needs nothing at all. When enabled it
needs the claim plane, a collector binary at the configured
path, and the producer and instance settings from the
reference.

It never touches Postgres or any database, the graph, or
collector internals. It holds no durable state, so deleting
the deployment loses nothing but an in-flight lease, which
expires on its own. On SIGINT or SIGTERM it stops cleanly
and leaves any held lease to expire claim-plane side.
