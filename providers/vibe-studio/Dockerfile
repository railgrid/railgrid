# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/vibe-studio/portal/package.json providers/vibe-studio/portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm install --no-audit --no-fund
COPY providers/vibe-studio/portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go //go:embeds portal/dist.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY providers/vibe-studio/go.mod providers/vibe-studio/go.sum ./
# In-tree provider-sdk (go.mod replace => ../../provider-sdk; from
# WORKDIR /src that resolves to /provider-sdk). Build context is the
# REPO ROOT: docker build -f providers/vibe-studio/Dockerfile .
COPY provider-sdk/ /provider-sdk/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY providers/vibe-studio/ ./
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vibe-studio-provider .

# 3. Minimal runtime image. APIResourceSchemas the `init` subcommand applies
#    are baked at /etc/faros/schemas (FAROS_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/vibe-studio-provider /vibe-studio-provider
COPY providers/vibe-studio/deploy/chart/files/schemas /etc/faros/schemas
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/vibe-studio-provider"]
