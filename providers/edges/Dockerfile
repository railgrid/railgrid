# syntax=docker/dockerfile:1
#
# BUILD CONTEXT = REPO ROOT (not this dir), because go.mod has
#   replace github.com/faroshq/provider-sdk => ../../provider-sdk
# so the SDK source must be present as a sibling at build time. From the repo
# root:
#
#   docker build -f providers/edges/Dockerfile -t IMAGE .
#
# (The Makefile `docker-build-edges-provider` target does this.)

# 1. Build the portal micro-frontend (Vite → portal/dist), embedded by the Go
#    binary via //go:embed in assets.go.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/edges/portal/package.json providers/edges/portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY providers/edges/portal/ ./
RUN npm run build

# 2. Build the Go binary standalone (no go.work — the workspace file is not
#    copied, so each module resolves via its own go.mod + the local SDK replace).
FROM golang:1.26-alpine AS build
WORKDIR /src
# The SDK first so its layer caches independently of provider churn.
COPY provider-sdk/ ./provider-sdk/
COPY providers/edges/ ./providers/edges/
COPY --from=portal /portal/dist ./providers/edges/portal/dist
WORKDIR /src/providers/edges
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/edges-provider .

# 2. Minimal runtime image. The KubernetesCluster + LinuxServer APIResourceSchemas
#    the `init` subcommand applies are baked at /etc/kedge/schemas
#    (KEDGE_SCHEMAS_DIR), synced there by `make codegen-edges-provider`.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/edges-provider /edges-provider
COPY providers/edges/deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8084
ENV PORT=8084
USER nonroot:nonroot
ENTRYPOINT ["/edges-provider"]
