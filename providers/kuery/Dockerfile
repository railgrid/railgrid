# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist) in a node
#    stage. portal/ is a self-contained npm project so we only need its
#    package.json/lockfile + source — no host-side npm install required.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go embeds portal/dist via //go:embed, so
#    the dist/ output from the previous stage has to land at the right
#    relative path before `go build` runs.
#
#    CGO_ENABLED=1: the embedded kuery store uses mattn/go-sqlite3 (same as
#    upstream kuery's image). debian-based build stage so the runtime
#    distroless/base glibc matches.
FROM golang:1.26 AS build
WORKDIR /src
# TODO(sdk-publish): depends on github.com/faroshq/kedge-provider-sdk via a
# `replace => ../../provider-sdk` that only resolves in the monorepo (go.work).
# Standalone image builds need the SDK published (drop the replace) or vendored.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY main.go assets.go init_cmd.go ./
COPY core/ ./core/
COPY engagement/ ./engagement/
COPY mcpserver/ ./mcpserver/
COPY queryapi/ ./queryapi/
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/kuery-provider .

# 3. Runtime image: distroless/base (NOT static) for the glibc the CGO
#    sqlite driver links against. /data is the conventional store mount. The
#    APIResourceSchemas the `init` subcommand applies are baked at
#    /etc/kedge/schemas (KEDGE_SCHEMAS_DIR).
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/kuery-provider /kuery-provider
COPY deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8081
ENV PORT=8081
ENV KUERY_STORE_DSN=/data/kuery.db
USER nonroot:nonroot
ENTRYPOINT ["/kuery-provider"]
