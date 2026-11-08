# Contributing

Thanks for helping. Read [CONTRIBUTING.md](../../CONTRIBUTING.md)
before opening a pull request; this is the short version.

Go version is pinned in `go.mod` (`go 1.26.10`, module
`github.com/eshu-hq/eshu-extension-host`). The module has no
external dependencies, and tests enforce a
standard-library-only import boundary.

```bash
make ci          # lint + test + race, with the coverage floor
make lint        # vet, golangci-lint, format check, tidy, 500-line cap
make test        # build + full test suite
make race        # race suite + coverage floor (90.0)
make actionlint  # workflow file lint
```

`make ci` needs `golangci-lint` and `actionlint` on `PATH`.
CI pins its own copies; see
[CONTRIBUTING.md](../../CONTRIBUTING.md).

There's no SECURITY.md in this repo. The license is MIT; see
[LICENSE](../../LICENSE).
