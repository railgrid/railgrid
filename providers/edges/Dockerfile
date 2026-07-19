# syntax=docker/dockerfile:1
#
# BUILD CONTEXT = this dir (providers/edges), matching the other providers and
# the provider-release workflow (`context: ./providers/<name>`). The provider
# SDK is consumed as a PUBLISHED module (github.com/faroshq/provider-sdk), not a
# `replace => ../../provider-sdk` sibling, so no repo-root context is needed.
# Host/Tilt dev builds still resolve the local SDK via go.work.

# 1. Build the portal micro-frontend (Vite → portal/dist), embedded by the Go
#    binary via //go:embed in assets.go.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary standalone (no go.work — each module resolves via its
#    own go.mod; the SDK is fetched from the module proxy).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . ./
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/edges-provider .

# 3. Minimal runtime image. The KubernetesCluster + LinuxServer APIResourceSchemas
#    the `init` subcommand applies are baked at /etc/kedge/schemas
#    (KEDGE_SCHEMAS_DIR), synced there by `make codegen-edges-provider`.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/edges-provider /edges-provider
COPY deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8084
ENV PORT=8084
USER nonroot:nonroot
ENTRYPOINT ["/edges-provider"]
