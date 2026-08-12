# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go //go:embeds portal/dist.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY main.go assets.go init_cmd.go controller_manager.go ./
COPY apis/ ./apis/
COPY api/ ./api/
COPY client/ ./client/
COPY controller/ ./controller/
COPY engine/ ./engine/
COPY scheme/ ./scheme/
COPY session/ ./session/
COPY store/ ./store/
COPY tenant/ ./tenant/
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vibe-studio-provider .

# 3. Minimal runtime image. APIResourceSchemas the `init` subcommand applies
#    are baked at /etc/faros/schemas (FAROS_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/vibe-studio-provider /vibe-studio-provider
COPY deploy/chart/files/schemas /etc/faros/schemas
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/vibe-studio-provider"]
