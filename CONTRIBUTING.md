# Contributing

## Local gates

`make ci` runs what CI's `lint` lane runs. The tree holds no Go sources
yet, so that is workflow validation with `actionlint` today; the `go vet`,
`golangci-lint run`, gofumpt format, `go mod tidy -diff`, 500-line `.go`
cap, `go build`, test, and race commands land with the first Go sources
(epic #1). Run `make ci` before every push. It needs `actionlint` on
`PATH`; CI pins its copy via `ACTIONLINT_VERSION` in `ci.yml`.

## CI lanes

Job names are frozen because the `main` ruleset requires `ci-complete`:

| Lane | Runs when |
| --- | --- |
| `lint` | Workflow changes (actionlint today; full Go lint set with the first Go sources) |
| `test` | Lands with the first Go sources; then Go sources, fixtures, configs tests read, workflow changes |
| `race` | Lands with the first Go sources; same inputs as `test`, plus a coverage artifact and floor check |
| `shell` | Lands with the first `*.sh`; shell script changes |
| `image` | Lands with the first `Dockerfile`; `Dockerfile`, `.dockerignore`, Go sources, module changes (build only, no push) |
| `govulncheck` | Lands with the first `go.mod`; dependency changes, pushes to `main`, weekly schedule, manual dispatch |
| `gosec` | Lands with the first `go.mod`; pushes to `main`, weekly schedule, manual dispatch; advisory, SARIF to the Security tab |
| `ci-complete` | Always; fails on any needed lane failure, passes on legitimate skips |

Docs-only PRs run no lane and stay mergeable through `ci-complete`.
A manual `workflow_dispatch` run records every active lane.

## Coverage floor

The `race` job will fail below `COVERAGE_FLOOR` in `ci.yml` once Go code
lands. The floor starts at the first measured baseline and may only rise
toward the 90% target as tests land. Never lower it, exclude packages
silently, or delete assertions to move the number.

## No static gates, no hidden debt

No gate compares the tree against a committed count, list, or
generated-parity file, except the 500-line cap on `.go` files that lands
with the first Go sources. Lint runs on the whole repo with no
`--new-from-rev` window. Every exclusion in `.golangci.yml` will cite its
reason and a linked open issue on the same lines.
