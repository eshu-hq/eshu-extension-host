# Local parity for the CI lanes: `make ci` runs what the workflow's lint
# lane runs. The tree holds no Go sources yet, so the Go lane targets
# (vet, lint, fmt, tidy, cap, build, test, race) land with the first Go
# sources (epic #1); until then `make ci` validates the workflow files.
# Needs actionlint on PATH (see CONTRIBUTING.md).

.PHONY: ci lint

ci: lint

lint:
	actionlint
