# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + Vue → portal/dist) in a node
#    stage. portal/ is a self-contained npm project so we only need its
#    package.json/lockfile + source.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/code/portal/package.json providers/code/portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY providers/code/portal/ ./
RUN npm run build

# 2. Build the Go binary. The binary serves two subcommands — `init` and
#    `serve` — so the whole module source has to be present. assets.go
#    //go:embeds portal/dist, overlaid from the node stage so the bundle is fresh.
#
# The provider-sdk resolves IN-TREE via a go.mod replace, so the build
# context is the repo root and the sdk is copied alongside the module below
# (docker build -f providers/code/Dockerfile .).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY providers/code/go.mod providers/code/go.sum ./
# In-tree provider-sdk (go.mod replace => ../../provider-sdk; from
# WORKDIR /src that resolves to /provider-sdk). Build context is the
# REPO ROOT: docker build -f providers/code/Dockerfile .
COPY provider-sdk/ /provider-sdk/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY providers/code/ ./
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/code-provider .

# 3. Minimal runtime image. The portal assets are baked into the binary; the
#    APIResourceSchemas the `init` subcommand applies are baked at
#    /etc/faros/schemas (FAROS_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/code-provider /code-provider
COPY providers/code/deploy/chart/files/schemas /etc/faros/schemas
EXPOSE 8083
ENV PORT=8083
USER nonroot:nonroot
ENTRYPOINT ["/code-provider"]
