# syntax=docker/dockerfile:1
# railgrid-dev-agent — the injected control process for development-mode
# components of any infrastructure Template (docs/app-studio-template-sandboxes.md §2).
# The image is scratch on purpose: it carries the static binary that an init
# container copies into the component's toolchain image
# (`railgrid-dev-agent --install /railgrid/bin`), plus the CA roots the
# coordinator needs when it runs from this image. Application dependencies stay in
# generated component manifests and are installed by the component toolchain;
# this image carries no application SDK projection. The build context is this
# directory (providers/infrastructure/dev-agent), matching the per-provider
# contexts used by the CI image workflows.
FROM golang:1.26 AS build
WORKDIR /src
COPY . ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/railgrid-dev-agent .

FROM scratch
# The same image also runs the platform coordinator directly, and the
# coordinator dials public HTTPS origins (the Provider Actions token exchange).
# Scratch has no trust store, so without these roots every public certificate
# fails with "certificate signed by unknown authority" unless an explicit
# actions CA bundle is configured.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/railgrid-dev-agent /railgrid-dev-agent
ENTRYPOINT ["/railgrid-dev-agent"]
