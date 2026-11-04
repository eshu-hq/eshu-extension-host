# Contributing

## Local gates

`make ci` runs what CI's `lint`, `test`, and `race` lanes run: `go vet`,
`golangci-lint run`, gofumpt format check, `go mod tidy -diff`, the 500-line
`.go` cap, `go build`, the full test suite, and the race suite with the
coverage floor (90.0, may only rise). `make actionlint` mirrors the workflow
lint in the `lint` lane. Run `make ci` before every push. It needs
`golangci-lint` and `actionlint` on `PATH`; CI pins its own copies
(golangci-lint v2.12.2 via the lint action, actionlint 1.7.12 hardcoded
with a sha256 pin in `ci.yml`).

## CI lanes

Job names are frozen because the `main` ruleset requires `ci-complete`.
Conditions below are the PR-event path; on `push`, `schedule`, and
`workflow_dispatch` every lane except the path-filtered skips runs
(schedule = full lane sweep: govulncheck + gosec file findings issues,
lint/test/race/image also execute).

| Lane | Runs on a PR when |
| --- | --- |
| `changes` | Always (path filter; other lanes consume its outputs) |
| `lint` | Go sources, `misc` catch-all, workflow changes |
| `test` | Go sources, fixtures, shell scripts, `misc`, workflow changes |
| `race` | Go sources, `misc`, workflow changes (plus a coverage artifact and the floor check; excludes the fixtures/shell triggers `test` has) |
| `image` | `Dockerfile`/`.dockerignore`, Go sources, `misc`, workflow changes (build only, no push; asserts digest pins, dark posture, and the VERSION stamp) |
| `govulncheck` | Dependency changes (`go.mod`/`go.sum`), workflow changes; files an issue on scheduled failure |
| `gosec` | Go sources, `misc`, workflow changes; advisory on PRs, SARIF upload on non-PR events only |
| `ci-complete` | Always; fails on any needed lane failure, passes on legitimate skips |

Docs-only PRs run no lane and stay mergeable through `ci-complete`.
There is no `shell` lane: `*.sh` changes select the `test` lane via the
`shell` filter group.

## Coverage floor

The `race` job fails below `COVERAGE_FLOOR` in `ci.yml` (90.0, the first
measured baseline for the dark binary). The floor may only rise toward the
90% target as tests land. Never lower it, exclude packages silently, or
delete assertions to move the number.

## No static gates, no hidden debt

No gate compares the tree against a committed count, list, or
generated-parity file, except the 500-line cap on `.go` files. Lint runs on
the whole repo with no `--new-from-rev` window. Every exclusion in
`.golangci.yml` cites its reason and a linked open issue on the same
lines — currently there are no exclusions, so the rule holds vacuously.