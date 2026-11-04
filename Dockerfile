# Dark extension-host image: pure-Go admin binary built reproducibly with
# -trimpath. Module path is github.com/eshu-hq/eshu-extension-host; keep
# the COPY roots when this directory is the repository root.
#
# The dark binary carries no claim flow (eshu-hq/eshu#8877 Leaf 2): it
# serves health/readiness/metrics only and fails closed on any claim-flow
# flag. The image therefore needs no credentials, volumes, or network
# egress to start.
#
# Base digests (re-verify with a registry lookup when bumping the tag;
# verified 2026-11-04 during the Leaf 2 scaffold):
#   golang:1.26.10-bookworm -> sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb
#   (multi-arch list; the builder selects amd64)
#   gcr.io/distroless/static:nonroot -> sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
#   (multi-arch OCI index; pinned 2026-11-04, replaces the 18f4cc26 pin
#   which no longer resolves because the nonroot tag moved)
FROM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS build
# VERSION is the release tag (release.yml sets it; local builds report dev).
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# EKS nodes are linux/amd64; pin GOARCH so builds on any host (e.g. arm64
# dev boxes) produce a runnable binary. An unpinned build shipped an arm64
# image that failed with "exec format error" on ops-qa (eshu-hq/eshu#8852).
RUN CGO_ENABLED=0 GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/extension-host ./cmd/extension-host

FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=build /out/extension-host /extension-host
USER 65532:65532
EXPOSE 8082
ENTRYPOINT ["/extension-host"]
