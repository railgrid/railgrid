# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go //go:embeds portal/dist.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go heartbeat.go assets.go init_cmd.go ./
COPY apis ./apis
COPY api ./api
COPY channels ./channels
COPY client ./client
COPY engine ./engine
COPY llm ./llm
COPY store ./store
COPY tenant ./tenant
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agents-provider .

# 3. Minimal runtime image. APIResourceSchemas the `init` subcommand applies are
#    baked at /etc/kedge/schemas (KEDGE_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/agents-provider /agents-provider
COPY deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8087
ENV PORT=8087
USER nonroot:nonroot
ENTRYPOINT ["/agents-provider"]
