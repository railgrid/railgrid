# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. The binary serves `init` + `serve`, so the whole
#    module source has to be present. The kedge-provider-sdk is now a published
#    dependency (no replace), so `go mod download` fetches it from the proxy.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/infrastructure-provider .

# 2b. Fetch the helm CLI. The operator (`controller` subcommand) shells out to
#     helm to install/upgrade the kro release, so the runtime image needs it.
FROM alpine:3.20 AS helm
ARG TARGETARCH
ARG HELM_VERSION=v3.16.4
RUN apk add --no-cache curl tar && \
    curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" | tar -xz && \
    install -m 0755 "linux-${TARGETARCH}/helm" /helm

# 3. Minimal runtime image.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/infrastructure-provider /infrastructure-provider
COPY --from=helm /helm /usr/local/bin/helm
EXPOSE 8081
ENV PORT=8081
# helm needs writable cache/config/data dirs; point them at the world-writable
# /tmp so the operator can run helm as nonroot. The operator also writes the
# runtime kubeconfig to a /tmp temp file (os.CreateTemp).
ENV HELM_CACHE_HOME=/tmp/helm/cache \
    HELM_CONFIG_HOME=/tmp/helm/config \
    HELM_DATA_HOME=/tmp/helm/data
USER nonroot:nonroot
ENTRYPOINT ["/infrastructure-provider"]
