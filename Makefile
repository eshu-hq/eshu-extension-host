# Local parity for CI: `make ci` runs the lint, test, and race lane
# commands from .github/workflows/ci.yml. CI stays authoritative; this
# target is the whole local contract (see README.md, "Build and test").
#
#   make ci            # lint + test + race (with the coverage floor)
#   make lint          # vet, golangci-lint, gofumpt, tidy, 500-line cap
#   make test          # build + full test suite
#   make race          # race suite + coverprofile + coverage floor
#   make actionlint    # workflow lint (mirrors the lint lane tail)
#
# Needs `actionlint` and `golangci-lint` on PATH (see CONTRIBUTING.md);
# CI pins its own copies.

GOLANGCI_LINT_VERSION ?= v2.12.2
COVERAGE_FLOOR ?= 90.0

.PHONY: ci lint test race actionlint tools-lint

ci: lint test race

lint: tools-lint
	go vet ./...
	golangci-lint run ./...
	golangci-lint fmt --diff ./...
	go mod tidy -diff
	git diff --exit-code -- go.mod go.sum
	over=$$(git ls-files '*.go' | while read -r f; do n=$$(wc -l < "$$f"); if [ "$$n" -gt 500 ]; then echo "$$f: $$n lines"; fi; done); \
	if [ -n "$$over" ]; then echo "$$over"; echo "FAIL: .go files over 500 lines"; exit 1; fi
	git diff --check

test:
	go build ./...
	go test ./... -count=1

race:
	go test ./... -race -count=1 -covermode=atomic -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -n 5
	total=$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/, "", $$3); print $$3}'); \
	echo "total=$${total}% floor=$(COVERAGE_FLOOR)%"; \
	awk -v t="$$total" -v f="$(COVERAGE_FLOOR)" 'BEGIN { if (t + 0 < f + 0) { print "FAIL: coverage below floor"; exit 1 } }'

actionlint:
	actionlint .github/workflows/*.yml
