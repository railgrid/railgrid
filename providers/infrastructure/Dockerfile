# syntax=docker/dockerfile:1

# Build context is the REPO ROOT (see .github/workflows/images.yaml: context: .)
# because this module depends on github.com/faroshq/kedge-provider-sdk via a
# `replace => ../../provider-sdk` that only resolves when the SDK sits next to
# the provider module. .dockerignore strips go.work so the build uses this
# module's go.mod + replace directly (no workspace mode).

# 1. Build the portal micro-frontend (Vite + TS → portal/dist) in a node stage.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/infrastructure/portal/package.json providers/infrastructure/portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY providers/infrastructure/portal/ ./
RUN npm run build

# 2. Build the Go binary. The binary serves two subcommands — `init`
#    (bootstrap) and `serve` (runtime) — so the WHOLE module source has to be
#    present, not just main.go: init_cmd.go, install/ (which //go:embeds its
#    crds/ + templates/), controller/, backend/, kro/, tenant/, mcpserver/,
#    server/, apis/. assets.go //go:embeds portal/dist, overlaid from the node
#    stage below so the bundle is fresh.
FROM golang:1.26-alpine AS build
WORKDIR /src
# The replaced SDK module must sit at ../../provider-sdk relative to the
# provider module (i.e. /src/provider-sdk vs /src/providers/infrastructure).
COPY provider-sdk/ ./provider-sdk/
COPY providers/infrastructure/go.mod providers/infrastructure/go.sum ./providers/infrastructure/
WORKDIR /src/providers/infrastructure
RUN go mod download
WORKDIR /src
COPY providers/infrastructure/ ./providers/infrastructure/
COPY --from=portal /portal/dist ./providers/infrastructure/portal/dist
WORKDIR /src/providers/infrastructure
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/infrastructure-provider .

# 3. Minimal runtime image. The portal assets are baked into the binary,
#    so there is nothing else to copy.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/infrastructure-provider /infrastructure-provider
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/infrastructure-provider"]
