# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/agents/portal/package.json providers/agents/portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY providers/agents/portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go //go:embeds portal/dist.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY providers/agents/go.mod providers/agents/go.sum ./
# In-tree provider-sdk (go.mod replace => ../../provider-sdk; from
# WORKDIR /src that resolves to /provider-sdk). Build context is the
# REPO ROOT: docker build -f providers/agents/Dockerfile .
COPY provider-sdk/ /provider-sdk/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# The whole module: controllers, scheme and internal packages ship with
# the binary, so an explicit directory list here rots the moment one is added.
COPY providers/agents/ ./
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agents-provider .

# 3. Minimal runtime image. The two declarative objects `init` applies — the
#    generated APIExport and its APIResourceSchemas — are baked at /etc/railgrid/kcp (RAILGRID_KCP_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/agents-provider /agents-provider
COPY providers/agents/deploy/chart/files /etc/railgrid/kcp
EXPOSE 8087
ENV PORT=8087
USER nonroot:nonroot
ENTRYPOINT ["/agents-provider"]
