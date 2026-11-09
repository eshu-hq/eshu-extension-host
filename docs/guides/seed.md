# Collector placement (there is no seed command)

Another search saved: this host has no `seed` subcommand.
It doesn't install collectors. It runs whatever executable
you point it at, once per claim, over stdin and stdout.

So "seeding" here is an operator job:

1. Put the collector binary somewhere the host process can
   execute it, inside the same container or VM. The host
   image doesn't carry one for you.
2. Set `ESHU_EXTENSIONHOST_COLLECTOR_PATH` to that path,
   plus the rest of the enabled-mode environment in
   [configuration](../reference/configuration.md).
3. Turn claims on (`--claims-enabled` or
   `ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1`) and confirm
   `/healthz` reports `"mode":"enabled"`.

If the collector itself is distributed by seeding (the
scanner programs work that way), seed it with that repo's
own tooling first, then point this host at the seeded
program. Dry-run mode is a good rehearsal before you commit
real results; see [operate](operate.md).
