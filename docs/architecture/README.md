# Architecture

## Place in Eshu

`eshu-extension-host` is Eshu's standalone supervisor for
component collectors. It doesn't replace the host that runs
inside Eshu core yet (status: pre-release). Core runs the
claim plane; this host polls it, runs one collector child
per claim over stdin/stdout, heartbeats the lease, and
commits the result unchanged. Collectors themselves stay
one-shot and never poll.

Diagrams live in [diagrams](../diagrams/README.md). The
[top-level README](../../README.md) keeps one overview
diagram.

## Shape of the host

- Admin listener (default `:8082`) for `/healthz`,
  `/readyz`, `/metrics`.
- Claim-plane client over HTTP; child collectors over stdio.
  Those plus the admin listener are the only interfaces.
- Go `1.26.10` pinned in `go.mod` (module
  `github.com/eshu-hq/eshu-extension-host`), no external
  dependencies, standard-library-only import boundary
  enforced by tests.
- No durable state. See [boundaries](../concepts/boundaries.md).

## Related repos

- [eshu](https://github.com/eshu-hq/eshu): core. It runs the
  claim plane this host polls.
- [eshu-sdk](https://github.com/eshu-hq/eshu-sdk): the
  collector contract. Requests to child collectors carry
  protocol version `collector-sdk/v1alpha1`.
- The component collectors (for example
  [eshu-collector-aws-cloud](https://github.com/eshu-hq/eshu-collector-aws-cloud)):
  the programs this host can run, one per claim.
- Source pin, release, and rollback notes:
  [PROVENANCE.md](../../PROVENANCE.md).
