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
#   golang:1.26.11-bookworm -> sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
#   (multi-arch list; the builder stage below runs on the build platform)
#   gcr.io/distroless/static:nonroot -> sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
#   (multi-arch OCI index; pinned 2026-11-04, replaces the 18f4cc26 pin
#   which no longer resolves because the nonroot tag moved)
# The builder stage runs natively (--platform=$BUILDPLATFORM, no QEMU
# needed: cross-compiling Go is just GOARCH) while GOARCH follows the
# image platform being baked ($TARGETARCH, set by BuildKit from
# --platform; a plain `docker build` defaults it to the daemon arch, and
# the :-amd64 fallback keeps pre-BuildKit builders on the fleet arch).
# A hardcoded GOARCH=amd64 baked an amd64 binary into the arm64 image of
# a multi-arch push (the mirror of the eshu-hq/eshu#8852 "exec format
# error" class, caught by review before the first tag release).
FROM --platform=$BUILDPLATFORM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS build
# VERSION is the release tag (release.yml sets it; local builds report dev).
# The stamp path is exercised by the `image` lane in ci.yml, which builds
# with a known VERSION and asserts /healthz reports it.
ARG VERSION=dev
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/extension-host ./cmd/extension-host

FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=build /out/extension-host /extension-host
USER 65532:65532
EXPOSE 8082
ENTRYPOINT ["/extension-host"]
