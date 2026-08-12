# syntax=docker/dockerfile:1
# faros-dev-agent — the injected control process for development-mode
# components of any infrastructure Template (docs/app-studio-template-sandboxes.md §2).
# The image is scratch on purpose: it exists only to carry the static binary
# that an init container copies into the component's toolchain image
# (`faros-dev-agent --install /faros/bin`). Application dependencies stay in
# generated component manifests and are installed by the component toolchain;
# this image carries no application SDK projection. The build context is this
# directory (providers/infrastructure/dev-agent), matching the per-provider
# contexts used by the CI image workflows.
FROM golang:1.26 AS build
WORKDIR /src
COPY . ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/faros-dev-agent .

FROM scratch
COPY --from=build /out/faros-dev-agent /faros-dev-agent
ENTRYPOINT ["/faros-dev-agent"]
