# syntax=docker/dockerfile:1

FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/databricks/portal/package.json providers/databricks/portal/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY providers/databricks/portal/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
ARG VERSION=dev
COPY providers/databricks/go.mod providers/databricks/go.sum ./
# In-tree provider-sdk (go.mod replace => ../../provider-sdk; from
# WORKDIR /src that resolves to /provider-sdk). Build context is the
# REPO ROOT: docker build -f providers/databricks/Dockerfile .
COPY provider-sdk/ /provider-sdk/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY providers/databricks/ ./
COPY --from=portal /portal/dist ./portal/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildVersion=${VERSION}" -o /out/databricks-provider .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/databricks-provider /databricks-provider
COPY providers/databricks/deploy/chart/files/schemas /etc/faros/schemas
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/databricks-provider"]
