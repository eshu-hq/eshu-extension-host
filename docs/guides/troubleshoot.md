# Troubleshoot the extension host

Nothing is being collected? Read `/healthz` first.
`"mode":"dark"` means claims were never turned on. That's a
setting, not an outage, and it's the most common false alarm
with this binary.

If it's enabled and still quiet:

- Check the poll loop can reach
  `ESHU_WORKFLOW_CLAIM_PLANE_URL` from where the process runs.
- Check the collector path exists and is executable inside
  the container or VM, not just on your laptop.
- Look for `runtime.claim.failed`. A child that prints more
  than one JSON value, or more than 4 MiB, fails its claim.
  So does a denied producer grant, and that one's terminal
  rather than retryable. So is a terminally refused result
  carrier (a 4xx code invalid refusal, e.g. a non-complete
  result state); a 5xx stays retryable.
- Claims not matching? The kind and instance id have to
  match what the claim plane is offering. The instance id
  comes from `ESHU_COMPONENT_COLLECTOR_INSTANCE_ID` and
  nowhere else.

Startup failures name the offending variable. They're
usually right; see [configuration](../reference/configuration.md).

Killing the process is safe. SIGINT or SIGTERM stops it
cleanly, and any lease it's holding just expires on the
claim plane side.
