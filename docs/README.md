# Documentation

Welcome in. This is the map for `eshu-extension-host`, the
standalone host that claims collector work from Eshu and
runs one collector program per claim. By default it does no
collection work at all; "dark" is this codebase's word for
off.

Start with getting started if you want to see dark mode for
yourself. Head for the guides when you're turning claims on
or figuring out why nothing is being collected.

| Start here | What's in it |
|---|---|
| [Getting started](getting-started/README.md) | Run health-only, then learn what claiming takes |
| [Guides](guides/README.md) | Operate, troubleshoot, collector placement, and release |
| [Concepts](concepts/README.md) | The claim contract and what the host never touches |
| [Reference](reference/README.md) | Flags, environment, and the cutover gate |
| [Architecture](architecture/README.md) | Where the host sits in Eshu |
| [Diagrams](diagrams/README.md) | Every diagram, with captions and text versions |
| [Decisions](decisions/README.md) | Architecture decision records |
| [Contributing](contributing/README.md) | Local gates before you open a pull request |

A claim is one unit of collector work handed out by Eshu, a
lease is its time limit, a heartbeat extends that limit, and
the claim plane is the Eshu HTTP service handing claims out.
The [top-level README](../README.md) keeps the short version
and one overview diagram.
