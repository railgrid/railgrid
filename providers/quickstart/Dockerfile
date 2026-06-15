# syntax=docker/dockerfile:1

# Build context is the REPO ROOT (see .github/workflows/images.yaml: context: .)
# because this module depends on github.com/faroshq/kedge-provider-sdk via a
# `replace => ../../provider-sdk` that only resolves when the SDK sits next to
# the provider module. .dockerignore strips go.work so the build uses this
# module's go.mod + replace directly (no workspace mode).

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/quickstart/portal/package.json providers/quickstart/portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY providers/quickstart/portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go embeds portal/dist via //go:embed; init_cmd.go
#    uses the kedge-provider-sdk install package via the ../../provider-sdk replace.
FROM golang:1.26-alpine AS build
WORKDIR /src
# The replaced SDK module must sit at ../../provider-sdk relative to the
# provider module (i.e. /src/provider-sdk vs /src/providers/quickstart).
COPY provider-sdk/ ./provider-sdk/
COPY providers/quickstart/go.mod providers/quickstart/go.sum ./providers/quickstart/
WORKDIR /src/providers/quickstart
RUN go mod download
WORKDIR /src
COPY providers/quickstart/ ./providers/quickstart/
COPY --from=portal /portal/dist ./providers/quickstart/portal/dist
WORKDIR /src/providers/quickstart
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/quickstart-provider .

# 3. Minimal runtime image. Portal assets are baked into the binary; the
#    APIResourceSchemas the `init` subcommand applies are baked at
#    /etc/kedge/schemas (KEDGE_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/quickstart-provider /quickstart-provider
COPY providers/quickstart/deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/quickstart-provider"]
