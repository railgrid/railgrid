# syntax=docker/dockerfile:1

# Built in the standalone faroshq/provider-app-studio mirror (synced from the
# kedge monorepo at providers/app-studio/ — see README). The build context is
# the mirror root, i.e. the contents of providers/app-studio/, so all paths
# below are relative to this module's root.

# 1. Build the App Studio portal micro-frontend (Vite + Vue → portal/dist).
#    portal/ is a self-contained npm project, so only its lockfile + source
#    are needed.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go //go:embeds portal/dist, overlaid from the
#    node stage so the bundle is fresh. The module depends on the published
#    github.com/faroshq/provider-sdk (no local replace), so it resolves from the
#    proxy in a standalone build context.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . ./
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app-studio-provider .

# 3. Minimal runtime image. The portal bundle is baked into the binary; the
#    APIResourceSchemas the `init` subcommand applies are baked at
#    /etc/kedge/schemas (KEDGE_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/app-studio-provider /app-studio-provider
COPY deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/app-studio-provider"]
