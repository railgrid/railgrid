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
COPY providers/agents/main.go providers/agents/heartbeat.go providers/agents/assets.go providers/agents/init_cmd.go ./
COPY providers/agents/apis ./apis
COPY providers/agents/api ./api
COPY providers/agents/channels ./channels
COPY providers/agents/client ./client
COPY providers/agents/engine ./engine
COPY providers/agents/executor ./executor
COPY providers/agents/llm ./llm
COPY providers/agents/store ./store
COPY providers/agents/tenant ./tenant
COPY providers/agents/tools ./tools
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agents-provider .

# 3. Minimal runtime image. APIResourceSchemas the `init` subcommand applies are
#    baked at /etc/faros/schemas (FAROS_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/agents-provider /agents-provider
COPY providers/agents/deploy/chart/files/schemas /etc/faros/schemas
EXPOSE 8087
ENV PORT=8087
USER nonroot:nonroot
ENTRYPOINT ["/agents-provider"]
