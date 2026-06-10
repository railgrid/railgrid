# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + Vue → portal/dist) in a node
#    stage. portal/ is a self-contained npm project so we only need its
#    package.json/lockfile + source.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. The binary serves two subcommands — `init` and
#    `serve` — so the whole module source has to be present. assets.go
#    //go:embeds portal/dist, overlaid from the node stage so the bundle is fresh.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/code-provider .

# 3. Minimal runtime image. The portal assets are baked into the binary.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/code-provider /code-provider
EXPOSE 8083
ENV PORT=8083
USER nonroot:nonroot
ENTRYPOINT ["/code-provider"]
