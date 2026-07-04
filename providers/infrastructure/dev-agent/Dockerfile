# kedge-dev-agent — the injected control process for development-mode
# components of any infrastructure Template (docs/app-studio-template-sandboxes.md §2).
# The image is scratch on purpose: it exists only to carry the static binary
# that an init container copies into the component's toolchain image
# (`kedge-dev-agent --install /kedge/bin`), so nothing kedge-specific is ever
# baked into dev images. Stdlib-only Go module; the build context is this dir.
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kedge-dev-agent .

FROM scratch
COPY --from=build /out/kedge-dev-agent /kedge-dev-agent
ENTRYPOINT ["/kedge-dev-agent"]
