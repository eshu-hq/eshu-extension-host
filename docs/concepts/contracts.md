# The claim contract

Collector programs in Eshu stay small and one-shot on
purpose: read one request, write one result, exit. Something
still has to ask Eshu for work, start the right program,
keep the job's lease alive, and hand the result back. That's
this host. It owns polling, leases, retries, and result
delivery, once, for whichever collector binary you point it
at.

See [diagrams](../diagrams/README.md) for the pieces diagram
and the one-claim sequence, with captions and text versions.

## One claim, start to finish (enabled mode)

1. Ask the claim plane for the next claim matching the
   configured collector kind and instance id. If none is
   ready, wait and retry.
2. Build one request and start the collector program. The
   request carries `protocol_version`
   (`collector-sdk/v1alpha1`), a `claim` (component and
   instance ids, collector kind, source system, scope, run
   and work-item ids, attempt, deadline, and config handle),
   and an empty `contract` block. It's written to the
   child's stdin.
3. Heartbeat the lease while the child runs. The interval is
   one third of the lease time.
4. Read stdout with a 4 MiB cap. The output must be exactly
   one JSON value. Extra data or oversize output fails the
   claim. Child stderr is discarded.
5. When the result state is complete, commit the result bytes
   unchanged, then complete the claim. Any other result state
   fails the claim as retryable (FailRetryable plus Release)
   before any commit call. If the run or commit fails, the claim
   is failed as retryable, or terminal when the error says the
   producer grant was denied or the plane terminally refused the
   result carrier (a 4xx code invalid refusal, e.g. a
   complete-state carrier refused on identity grounds; a 5xx
   stays retryable). A
   producer grant is Eshu's permission
   record saying a named producer may emit a named fact kind.
   In dry-run mode the host logs byte count and hash and
   commits nothing.

Data in is claims from the claim plane. Data out is the
collector's result bytes, committed exactly as they arrived,
plus JSON log lines on stderr. The host never edits a
result.

Code pointers: `internal/cutovercontract/contract.go` holds
the claim and runner interfaces, and
`internal/adapter/runner.go` is the stdio child runner.
